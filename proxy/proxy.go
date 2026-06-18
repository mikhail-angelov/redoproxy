package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/mikhail-angelov/redoproxy/proxy/middleware"
)

type RouteMatcher interface {
	LookupGroup(host string) (*RouteGroup, bool)
}

type HTTPProxy struct {
	Matcher RouteMatcher
	Server  *http.Server

	handler   http.Handler
	transport http.RoundTripper
}

func NewHttpProxy(addr string, matcher RouteMatcher) *HTTPProxy {
	p := &HTTPProxy{
		Matcher:   matcher,
		transport: newUpstreamTransport(),
	}

	var handler http.Handler = http.HandlerFunc(p.handleHTTP)
	handler = middleware.BodySizeLimitMiddleware(handler, p.getMaxRequestBodySize)
	handler = middleware.ConcurrentRequestsLimitMiddleware(handler, p.getConcurrentRequestsLimit)
	handler = middleware.LoggingMiddleware(handler)
	handler = middleware.HealthMiddleware(handler)
	handler = middleware.RequestIDMiddleware(handler)

	p.handler = handler

	p.Server = &http.Server{
		Addr:              addr,
		Handler:           p,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	return p
}

func newUpstreamTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          1000,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
}

func (p *HTTPProxy) Run(ctx context.Context) error {
	errCh := make(chan error, 1)

	go func() {
		slog.Info("reverse proxy started", "addr", p.Server.Addr)
		errCh <- p.Server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := p.Server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("failed to shutdown proxy server: %w", err)
		}

		return ctx.Err()

	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func (p *HTTPProxy) RunTLS(ctx context.Context) error {
	errCh := make(chan error, 1)

	go func() {
		slog.Info("https reverse proxy started", "addr", p.Server.Addr)
		errCh <- p.Server.ListenAndServeTLS("", "")
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := p.Server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("failed to shutdown https proxy server: %w", err)
		}

		return ctx.Err()

	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func (p *HTTPProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.handler.ServeHTTP(w, r)
}

func (p *HTTPProxy) handleHTTP(w http.ResponseWriter, r *http.Request) {

	group, ok := p.Matcher.LookupGroup(r.Host)
	if !ok {
		http.Error(w, "route not found", http.StatusNotFound)
		return
	}

	upstream, ok := group.Next(r)
	if !ok {
		http.Error(w, "no upstreams available", http.StatusBadGateway)
		return
	}

	target, err := url.Parse(upstream.Server)
	if err != nil || target.Scheme == "" || target.Host == "" {
		slog.Error("invalid route target", "request_id", middleware.RequestIDFromContext(r.Context()), "host", r.Host, "target", upstream.Server, "err", err)
		http.Error(w, "invalid route target", http.StatusBadGateway)
		return
	}

	rp := &httputil.ReverseProxy{
		Transport: p.transport,
		Rewrite: func(preq *httputil.ProxyRequest) {
			preq.SetURL(target)
			preq.Out.Host = preq.In.Host

			if ip, _, err := net.SplitHostPort(preq.In.RemoteAddr); err == nil {
				preq.Out.Header.Set("X-Real-Ip", ip)
			}
			requestID := middleware.RequestIDFromContext(preq.In.Context())
			if requestID != "" {
				preq.Out.Header.Set(middleware.RequestIDHeader, requestID)
			}

			preq.SetXForwarded()
		},
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				slog.Warn(
					"request body is too large",
					"request_id", middleware.RequestIDFromContext(req.Context()),
					"host", req.Host,
					"target", upstream.Server,
					"limit", maxBytesErr.Limit,
					"err", err,
				)
				http.Error(w, "request body is too large", http.StatusRequestEntityTooLarge)
				return
			}

			slog.Error(
				"upstream request failed",
				"request_id", middleware.RequestIDFromContext(req.Context()),
				"host", r.Host,
				"target", upstream.Server,
				"err", err,
			)
			http.Error(w, "bad gateway", http.StatusBadGateway)
		},
	}

	rp.ServeHTTP(w, r)
}

func (p *HTTPProxy) getMaxRequestBodySize(host string) (int64, error) {

	route, ok := p.Matcher.LookupGroup(host)
	if !ok {
		return 0, fmt.Errorf("invalid host: %s", host)
	}
	return route.MaxBodySize, nil
}
func (p *HTTPProxy) getConcurrentRequestsLimit(host string) (int, error) {

	route, ok := p.Matcher.LookupGroup(host)
	if !ok {
		return 0, fmt.Errorf("invalid host: %s", host)
	}
	return route.ConcurrentRequestsLimit, nil
}
