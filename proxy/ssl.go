package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

type SSL struct {
	manager  *autocert.Manager
	matcher  RouteMatcher
	httpAddr string

	certMu     sync.Mutex
	certLogged map[string]bool
}

func NewSSL(matcher RouteMatcher, email string, path string, httpAddr string, directoryURL string) *SSL {
	if httpAddr == "" {
		httpAddr = ":80"
	}
	cache := loggingAutocertCache{
		inner: autocert.DirCache(path),
	}
	manager := &autocert.Manager{
		Prompt: autocert.AcceptTOS,
		Email:  email,
		Cache:  cache,
		HostPolicy: func(ctx context.Context, host string) error {
			group, ok := matcher.LookupGroup(host)
			if ok {
				targets := make([]string, len(group.Upstreams))
				for i, u := range group.Upstreams {
					targets[i] = u.Server
				}
				slog.Info(
					"acme host allowed",
					"host", host,
					"targets", targets,
				)
				return nil
			}

			slog.Warn("acme host rejected", "host", host)
			return fmt.Errorf("host is not allowed: %s", host)
		},
	}
	if directoryURL != "" {
		manager.Client = &acme.Client{
			DirectoryURL: directoryURL,
		}
	}
	slog.Info(
		"acme manager configured",
		"http_addr", httpAddr,
		"cache_dir", path,
		"email_set", email != "",
		"directory_url", directoryURL,
		"production", directoryURL == "",
	)
	return &SSL{
		manager:    manager,
		matcher:    matcher,
		httpAddr:   httpAddr,
		certLogged: make(map[string]bool),
	}
}

func (s *SSL) TLSConfig() *tls.Config {
	cfg := s.manager.TLSConfig()

	getCertificate := cfg.GetCertificate
	cfg.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		serverName := normalizeLookupHost(hello.ServerName)

		slog.Info(
			"acme certificate requested",
			"server_name", serverName,
		)

		cert, err := getCertificate(hello)
		if err != nil {
			slog.Error(
				"acme certificate request failed",
				"server_name", serverName,
				"err", err,
			)
			return nil, err
		}

		s.logCertificateReady(serverName, cert)

		return cert, nil
	}

	return cfg
}

func (s *SSL) logCertificateReady(serverName string, cert *tls.Certificate) {
	if serverName == "" || cert == nil {
		return
	}

	s.certMu.Lock()
	if s.certLogged[serverName] {
		s.certMu.Unlock()
		return
	}
	s.certLogged[serverName] = true
	s.certMu.Unlock()

	notAfter := certificateNotAfter(cert)

	if notAfter.IsZero() {
		slog.Info(
			"acme certificate ready",
			"server_name", serverName,
		)
		return
	}

	slog.Info(
		"acme certificate ready",
		"server_name", serverName,
		"not_after", notAfter.Format(time.RFC3339),
	)
}

func certificateNotAfter(cert *tls.Certificate) time.Time {
	if cert == nil {
		return time.Time{}
	}

	if cert.Leaf != nil {
		return cert.Leaf.NotAfter
	}

	if len(cert.Certificate) == 0 {
		return time.Time{}
	}

	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return time.Time{}
	}

	return leaf.NotAfter
}

func (s *SSL) Handler() http.Handler {
	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok\n"))
			return
		}

		target := "https://" + r.Host + r.URL.RequestURI()

		slog.Info(
			"redirect http to https",
			"host", r.Host,
			"path", r.URL.Path,
			"target", target,
		)

		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})

	acmeHandler := s.manager.HTTPHandler(fallback)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/.well-known/acme-challenge/") {
			slog.Info(
				"acme http-01 challenge request",
				"host", r.Host,
				"path", r.URL.Path,
				"remote_addr", r.RemoteAddr,
			)
		}

		acmeHandler.ServeHTTP(w, r)
	})
}

func (s *SSL) RunHTTPChallengeServer(ctx context.Context) {
	httpServer := &http.Server{
		Addr:              s.httpAddr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		slog.Info("http acme server started", "addr", httpServer.Addr)

		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http acme server stopped", "err", err)
		}
	}()

	go func() {
		<-ctx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("failed to shutdown http acme server", "err", err)
		}
	}()
}

type loggingAutocertCache struct {
	inner autocert.Cache
}

func (c loggingAutocertCache) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := c.inner.Get(ctx, key)
	if err != nil {
		if errors.Is(err, autocert.ErrCacheMiss) {
			slog.Info("acme cache miss", "key", key)
		} else {
			slog.Warn("acme cache get failed", "key", key, "err", err)
		}

		return nil, err
	}

	slog.Info("acme cache hit", "key", key, "bytes", len(data))

	return data, nil
}

func (c loggingAutocertCache) Put(ctx context.Context, key string, data []byte) error {
	if err := c.inner.Put(ctx, key, data); err != nil {
		slog.Error("acme cache put failed", "key", key, "bytes", len(data), "err", err)
		return err
	}

	slog.Info("acme cache put", "key", key, "bytes", len(data))

	return nil
}

func (c loggingAutocertCache) Delete(ctx context.Context, key string) error {
	if err := c.inner.Delete(ctx, key); err != nil {
		slog.Error("acme cache delete failed", "key", key, "err", err)
		return err
	}

	slog.Info("acme cache delete", "key", key)

	return nil
}
