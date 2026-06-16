package proxy

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

type sslTestMatcher struct {
	routeGroup *RouteGroup
	result     bool
	host       string
}

func (m *sslTestMatcher) LookupGroup(host string) (*RouteGroup, bool) {
	m.host = host
	return m.routeGroup, m.result
}

func TestNewSSLDefaultsHTTPAddr(t *testing.T) {
	matcher := &sslTestMatcher{
		routeGroup: &RouteGroup{
			Domain: "example.com",
			Upstreams: []Upstream{{
				Server: "http://127.0.0.1:8080",
			}},
		},
		result: true,
	}

	ssl := NewSSL(matcher, "dev@example.com", t.TempDir(), "", "")

	assert.NotNil(t, ssl)
	assert.NotNil(t, ssl.manager)
	assert.NotNil(t, ssl.matcher)
	assert.Equal(t, ":80", ssl.httpAddr)
	assert.Equal(t, "dev@example.com", ssl.manager.Email)
}

func TestNewSSLUsesCustomHTTPAddrAndDirectoryURL(t *testing.T) {
	const directoryURL = "https://acme-staging-v02.api.letsencrypt.org/directory"

	matcher := &sslTestMatcher{
		result: true,
	}

	ssl := NewSSL(matcher, "dev@example.com", t.TempDir(), ":8080", directoryURL)

	assert.Equal(t, ":8080", ssl.httpAddr)
	assert.NotNil(t, ssl.manager.Client)
	assert.Equal(t, directoryURL, ssl.manager.Client.DirectoryURL)
}

func TestSSLHostPolicyAllowsMatchedHost(t *testing.T) {
	matcher := &sslTestMatcher{
		routeGroup: &RouteGroup{
			Domain: "example.com",
			Upstreams: []Upstream{{
				Server: "http://127.0.0.1:8080",
			}},
		},
		result: true,
	}

	ssl := NewSSL(matcher, "dev@example.com", t.TempDir(), ":8080", "")

	err := ssl.manager.HostPolicy(context.Background(), "example.com")
	assert.NoError(t, err)
	assert.Equal(t, "example.com", matcher.host)
}

func TestSSLHostPolicyRejectsUnknownHost(t *testing.T) {
	matcher := &sslTestMatcher{
		result: false,
	}

	ssl := NewSSL(matcher, "dev@example.com", t.TempDir(), ":8080", "")

	err := ssl.manager.HostPolicy(context.Background(), "unknown.example.com")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "host is not allowed")
	assert.Equal(t, "unknown.example.com", matcher.host)
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

	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "text/plain; charset=utf-8", res.Header.Get("Content-Type"))

	body, err := io.ReadAll(res.Body)
	assert.NoError(t, err)
	assert.Equal(t, "ok\n", string(body))
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

	assert.Equal(t, http.StatusMovedPermanently, res.StatusCode)
	assert.Equal(t, "https://example.com/some/path?x=1", res.Header.Get("Location"))
}

func TestSSLUsesLoggingCache(t *testing.T) {
	matcher := &sslTestMatcher{result: true}

	ssl := NewSSL(matcher, "dev@example.com", t.TempDir(), ":8080", "")

	assert.NotNil(t, ssl.manager.Cache)
	_, ok := ssl.manager.Cache.(loggingAutocertCache)
	assert.True(t, ok)
}

func TestSSLTLSConfigWrapsGetCertificate(t *testing.T) {
	matcher := &sslTestMatcher{result: true}

	ssl := NewSSL(matcher, "dev@example.com", t.TempDir(), ":8080", "")

	cfg := ssl.TLSConfig()

	assert.NotNil(t, cfg)
	assert.NotNil(t, cfg.GetCertificate)
}

func TestSSLLogCertificateReadyDoesNotPanic(t *testing.T) {
	matcher := &sslTestMatcher{result: true}

	ssl := NewSSL(matcher, "dev@example.com", t.TempDir(), ":8080", "")

	cert := &tls.Certificate{}

	ssl.logCertificateReady("example.com", cert)
	ssl.logCertificateReady("example.com", cert)
}
