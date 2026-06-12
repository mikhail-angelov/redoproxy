package proxy

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

type RouteMatcher interface {
	Lookup(host string) (ContainerRoute, bool)
}

type HTTPProxy struct {
	Matcher RouteMatcher
	Server  *http.Server
}

func NewHttpProxy(addr string, matcher RouteMatcher) *HTTPProxy {
	p := &HTTPProxy{
		Matcher: matcher,
	}
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
	start := time.Now()

	rec := newStatusRecorder(w)
	p.handleHTTP(rec, r)

	if r.URL.Path != "/health" {
		logAccess(r, rec.status, rec.bytes, time.Since(start))
	}
}

func (p *HTTPProxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}

	route, ok := p.Matcher.Lookup(r.Host)
	if !ok {
		http.Error(w, "route not found", http.StatusNotFound)
		return
	}

	target, err := url.Parse(route.Server)
	if err != nil || target.Scheme == "" || target.Host == "" {
		slog.Error("invalid route target", "host", r.Host, "target", route.Server, "err", err)
		http.Error(w, "invalid route target", http.StatusBadGateway)
		return
	}

	rp := &httputil.ReverseProxy{
		Rewrite: func(preq *httputil.ProxyRequest) {
			preq.SetURL(target)
			preq.Out.Host = preq.In.Host

			if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
				preq.Out.Header.Set("X-Real-Ip", ip)
			}
			requestId := preq.In.Header.Get("X-Request-Id")
			if requestId == "" {
				preq.Out.Header.Set("X-Request-Id", generateRequestId())
			}

			preq.SetXForwarded()
		},
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			slog.Error(
				"upstream request failed",
				"host", r.Host,
				"target", route.Server,
				"err", err,
			)
			http.Error(w, "bad gateway", http.StatusBadGateway)
		},
	}

	rp.ServeHTTP(w, r)
}

func generateRequestId() string {
	b := make([]byte, 16)
	rand.Read(b)
	// Format as UUID v4
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
