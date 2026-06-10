package proxy

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type sslTestMatcher struct {
	route  ContainerRoute
	result bool
	host   string
}

func (m *sslTestMatcher) Lookup(host string) (ContainerRoute, bool) {
	m.host = host
	return m.route, m.result
}

func TestNewSSLDefaultsHTTPAddr(t *testing.T) {
	matcher := &sslTestMatcher{
		route: ContainerRoute{
			Domain: "example.com",
			Server: "http://127.0.0.1:8080",
		},
		result: true,
	}

	ssl := NewSSL(matcher, "dev@example.com", t.TempDir(), "", "")

	if ssl == nil {
		t.Fatal("expected ssl not to be nil")
	}

	if ssl.manager == nil {
		t.Fatal("expected manager not to be nil")
	}

	if ssl.matcher == nil {
		t.Fatal("expected matcher not to be nil")
	}

	if ssl.httpAddr != ":80" {
		t.Fatalf("expected default httpAddr :80, got %q", ssl.httpAddr)
	}

	if ssl.manager.Email != "dev@example.com" {
		t.Fatalf("expected email dev@example.com, got %q", ssl.manager.Email)
	}
}

func TestNewSSLUsesCustomHTTPAddrAndDirectoryURL(t *testing.T) {
	const directoryURL = "https://acme-staging-v02.api.letsencrypt.org/directory"

	matcher := &sslTestMatcher{
		result: true,
	}

	ssl := NewSSL(matcher, "dev@example.com", t.TempDir(), ":8080", directoryURL)

	if ssl.httpAddr != ":8080" {
		t.Fatalf("expected httpAddr :8080, got %q", ssl.httpAddr)
	}

	if ssl.manager.Client == nil {
		t.Fatal("expected ACME client to be set")
	}

	if ssl.manager.Client.DirectoryURL != directoryURL {
		t.Fatalf("expected directory URL %q, got %q", directoryURL, ssl.manager.Client.DirectoryURL)
	}
}

func TestSSLHostPolicyAllowsMatchedHost(t *testing.T) {
	matcher := &sslTestMatcher{
		route: ContainerRoute{
			Domain: "example.com",
			Server: "http://127.0.0.1:8080",
		},
		result: true,
	}

	ssl := NewSSL(matcher, "dev@example.com", t.TempDir(), ":8080", "")

	err := ssl.manager.HostPolicy(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("expected host to be allowed, got error: %v", err)
	}

	if matcher.host != "example.com" {
		t.Fatalf("expected matcher host example.com, got %q", matcher.host)
	}
}

func TestSSLHostPolicyRejectsUnknownHost(t *testing.T) {
	matcher := &sslTestMatcher{
		result: false,
	}

	ssl := NewSSL(matcher, "dev@example.com", t.TempDir(), ":8080", "")

	err := ssl.manager.HostPolicy(context.Background(), "unknown.example.com")
	if err == nil {
		t.Fatal("expected host policy error")
	}

	if !strings.Contains(err.Error(), "host is not allowed") {
		t.Fatalf("expected host is not allowed error, got %v", err)
	}

	if matcher.host != "unknown.example.com" {
		t.Fatalf("expected matcher host unknown.example.com, got %q", matcher.host)
	}
}

func TestSSLHandlerHealth(t *testing.T) {
	matcher := &sslTestMatcher{
		result: false,
	}

	ssl := NewSSL(matcher, "dev@example.com", t.TempDir(), ":8080", "")

	req := httptest.NewRequest(http.MethodGet, "http://example.com/health", nil)

	rr := httptest.NewRecorder()
	ssl.Handler().ServeHTTP(rr, req)

	res := rr.Result()
	defer func() {
		_ = res.Body.Close()
	}()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", res.StatusCode)
	}

	if got := res.Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("expected text/plain content type, got %q", got)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	if string(body) != "ok\n" {
		t.Fatalf("expected body ok, got %q", string(body))
	}
}

func TestSSLHandlerRedirectsHTTPToHTTPS(t *testing.T) {
	matcher := &sslTestMatcher{
		result: false,
	}

	ssl := NewSSL(matcher, "dev@example.com", t.TempDir(), ":8080", "")

	req := httptest.NewRequest(http.MethodGet, "http://example.com/some/path?x=1", nil)

	rr := httptest.NewRecorder()
	ssl.Handler().ServeHTTP(rr, req)

	res := rr.Result()
	defer func() {
		_ = res.Body.Close()
	}()

	if res.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("expected status 301, got %d", res.StatusCode)
	}

	if got := res.Header.Get("Location"); got != "https://example.com/some/path?x=1" {
		t.Fatalf("expected redirect location https://example.com/some/path?x=1, got %q", got)
	}
}

func TestSSLUsesLoggingCache(t *testing.T) {
	matcher := &sslTestMatcher{result: true}

	ssl := NewSSL(matcher, "dev@example.com", t.TempDir(), ":8080", "")

	if ssl.manager.Cache == nil {
		t.Fatal("expected cache to be set")
	}

	if _, ok := ssl.manager.Cache.(loggingAutocertCache); !ok {
		t.Fatalf("expected loggingAutocertCache, got %T", ssl.manager.Cache)
	}
}

func TestSSLTLSConfigWrapsGetCertificate(t *testing.T) {
	matcher := &sslTestMatcher{result: true}

	ssl := NewSSL(matcher, "dev@example.com", t.TempDir(), ":8080", "")

	cfg := ssl.TLSConfig()

	if cfg == nil {
		t.Fatal("expected TLS config")
	}

	if cfg.GetCertificate == nil {
		t.Fatal("expected GetCertificate to be set")
	}
}

func TestSSLLogCertificateReadyDoesNotPanic(t *testing.T) {
	matcher := &sslTestMatcher{result: true}

	ssl := NewSSL(matcher, "dev@example.com", t.TempDir(), ":8080", "")

	cert := &tls.Certificate{}

	ssl.logCertificateReady("example.com", cert)
	ssl.logCertificateReady("example.com", cert)
}
