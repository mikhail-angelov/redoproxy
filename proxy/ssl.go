package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

type SSL struct {
	Manager  *autocert.Manager
	Matcher  RouteMatcher
	HTTPAddr string
}

func NewSSL(matcher RouteMatcher, email string, path string, httpAddr string, directoryURL string) *SSL {
	if httpAddr == "" {
		httpAddr = ":80"
	}
	manager := &autocert.Manager{
		Prompt: autocert.AcceptTOS,
		Email:  email,
		Cache:  autocert.DirCache(path),
		HostPolicy: func(ctx context.Context, host string) error {
			if _, ok := matcher.Lookup(host); ok {
				return nil
			}

			return fmt.Errorf("host is not allowed: %s", host)
		},
	}
	if directoryURL != "" {
		manager.Client = &acme.Client{
			DirectoryURL: directoryURL,
		}
	}
	return &SSL{
		Manager:  manager,
		Matcher:  matcher,
		HTTPAddr: httpAddr,
	}
}

func (s *SSL) Handler() http.Handler {
	return s.Manager.HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok\n"))
			return
		}

		target := "https://" + r.Host + r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	}))
}

func (s *SSL) RunHTTPChallengeServer(ctx context.Context) {
	httpServer := &http.Server{
		Addr:              s.HTTPAddr,
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
