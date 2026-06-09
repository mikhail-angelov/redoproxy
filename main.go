package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mikhail-angelov/redoproxy/proxy"
)

const letsEncryptStagingDirectoryURL = "https://acme-staging-v02.api.letsencrypt.org/directory"

func main() {
	slog.Info("redoproxy started")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tlsEnabled := getenv("TLS_ENABLED", "true") == "true"
	port := getenv("PORT", "8080")
	networkName := strings.TrimSpace(getenv("DOCKER_NETWORK", ""))
	interval, err := time.ParseDuration(getenv("DISCOVERY_INTERVAL", "60s"))
	httpPort := getenv("HTTP_PORT", "80")
	httpsPort := getenv("HTTPS_PORT", "443")
	acmeStaging := getenv("ACME_STAGING", "false")

	if err != nil {
		slog.Error("invalid DISCOVERY_INTERVAL", "err", err)
		os.Exit(1)
	}

	slog.Info(
		"config loaded",
		"tls_enabled", tlsEnabled,
		"port", port,
		"acme_staging", acmeStaging,
		"http_port", httpPort,
		"https_port", httpsPort,
		"docker_network", networkName,
		"discovery_interval", interval.String(),
	)

	discovery := proxy.NewDiscovery(interval, networkName)
	if err := discovery.CheckAndBootstrapDockerClient(); err != nil {
		slog.Error("Cannot bootstrap docker client", "err", err)
		os.Exit(1)
	}
	go func() {
		if err := discovery.Run(ctx); err != nil {
			slog.Error("discovery stopped", "err", err)
		}
	}()

	if !tlsEnabled {
		httpProxy := proxy.NewHttpProxy(":"+port, discovery)
		if err := httpProxy.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("cannot start proxy server", "err", err)
		}
		return
	}

	acmeEmail := getenv("ACME_EMAIL", "")
	acmeCacheDir := getenv("ACME_CACHE_DIR", "/data/autocert")
	acmeDirectoryURL := ""
	if acmeStaging == "true" {
		acmeDirectoryURL = letsEncryptStagingDirectoryURL
	}
	ssl := proxy.NewSSL(discovery, acmeEmail, acmeCacheDir, ":"+httpPort, acmeDirectoryURL)
	ssl.RunHTTPChallengeServer(ctx)

	httpsProxy := proxy.NewHttpProxy(":"+httpsPort, discovery)
	httpsProxy.Server.TLSConfig = ssl.Manager.TLSConfig()

	if err := httpsProxy.RunTLS(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("cannot start https proxy server", "err", err)
		os.Exit(1)
	}
	slog.Info("redoproxy stopped")
}

func getenv(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}
