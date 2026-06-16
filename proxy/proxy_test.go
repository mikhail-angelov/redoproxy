package proxy

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mikhail-angelov/redoproxy/proxy/middleware"
	"github.com/stretchr/testify/assert"
)

type TestMatcher struct {
	RouteGroup *RouteGroup
	Result     bool
	Host       string

	calls atomic.Int32
}

func (tm *TestMatcher) LookupGroup(host string) (*RouteGroup, bool) {
	tm.Host = host
	tm.calls.Add(1)
	return tm.RouteGroup, tm.Result
}

func TestReverseProxy(t *testing.T) {
	var backendCalled atomic.Bool
	errCh := make(chan string, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalled.Store(true)
		if r.Method != http.MethodGet {
			errCh <- "expected GET method, got " + r.Method
			return
		}

		if r.URL.Path != "/test" {
			errCh <- "expected path /test, got " + r.URL.Path
			return
		}

		if r.URL.Query().Get("mode") != "ok" {
			errCh <- "expected query mode=ok, got " + r.URL.RawQuery
			return
		}
		//test new headers

		if got := r.Header.Get("X-Forwarded-Host"); got != "example.com" {
			errCh <- "expected X-Forwarded-Host example.com, got " + got
			return
		}
		if got := r.Header.Get("X-Forwarded-Proto"); got != "http" {
			errCh <- "expected X-Forwarded-Proto http, got " + got
			return
		}

		if got := r.Header.Get("X-Real-Ip"); got == "" {
			errCh <- "expected X-Real-Ip to be set"
			return
		}

		if got := r.Header.Get("X-Request-Id"); got == "" {
			errCh <- "expected X-Request-Id to be set"
			return
		}

		w.Header().Set("X-Backend", "test-backend")
		w.WriteHeader(http.StatusOK)

		_, _ = w.Write([]byte("backend response"))
	}))
	defer backend.Close()
	tm := TestMatcher{
		RouteGroup: &RouteGroup{
			Domain: "example.com",
			Upstreams: []Upstream{{
				Server: backend.URL,
			}},
			MaxBodySize:             1,
			ConcurrentRequestsLimit: 1,
		},
		Result: true,
	}
	proxy := NewHttpProxy(":0", &tm)

	getReq := httptest.NewRequest(http.MethodGet, "http://example.com/test?mode=ok", nil)

	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, getReq)

	res := rr.Result()
	defer func() {
		_ = res.Body.Close()
	}()

	select {
	case msg := <-errCh:
		assert.Fail(t, msg)
	default:
	}

	assert.True(t, backendCalled.Load())
	assert.True(t, tm.calls.Load() >= 1)
	assert.Equal(t, "example.com", tm.Host)
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "test-backend", res.Header.Get("X-Backend"))
	body, err := io.ReadAll(res.Body)

	assert.NoError(t, err)
	assert.Equal(t, "backend response", string(body))
}
func TestReverseProxyInvalidDomain(t *testing.T) {
	var backendCalled atomic.Bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalled.Store(true)
	}))
	defer backend.Close()
	tm := TestMatcher{
		RouteGroup: &RouteGroup{
			Domain: "example.com",
			Upstreams: []Upstream{{
				Server: backend.URL,
			}},
		},
		Result: false,
	}
	proxy := NewHttpProxy(":0", &tm)

	getReq := httptest.NewRequest(http.MethodGet, "http://invalid.com/test", nil)

	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, getReq)

	res := rr.Result()
	defer func() {
		_ = res.Body.Close()
	}()

	assert.False(t, backendCalled.Load())
	assert.Equal(t, "invalid.com", tm.Host)
	assert.Equal(t, http.StatusNotFound, res.StatusCode)
}
func TestReverseProxyInvalidUrl(t *testing.T) {
	tm := TestMatcher{
		RouteGroup: &RouteGroup{
			Domain: "example.com",
			Upstreams: []Upstream{{
				Server: "http://%",
			}},
			MaxBodySize:             1,
			ConcurrentRequestsLimit: 1,
		},
		Result: true,
	}
	proxy := NewHttpProxy(":0", &tm)

	getReq := httptest.NewRequest(http.MethodGet, "http://example.com/test", nil)

	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, getReq)

	res := rr.Result()
	defer func() {
		_ = res.Body.Close()
	}()

	assert.Equal(t, "example.com", tm.Host)
	assert.Equal(t, http.StatusBadGateway, res.StatusCode)
}

func TestReverseProxyUpstreamConnectionRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)

	addr := ln.Addr().String()

	err = ln.Close()
	assert.NoError(t, err)

	tm := TestMatcher{
		RouteGroup: &RouteGroup{
			Domain: "example.com",
			Upstreams: []Upstream{{
				Server: "http://" + addr,
			}},
			MaxBodySize:             1,
			ConcurrentRequestsLimit: 1,
		},
		Result: true,
	}

	proxy := NewHttpProxy(":0", &tm)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/test", nil)

	rr := httptest.NewRecorder()

	proxy.ServeHTTP(rr, req)

	res := rr.Result()
	defer func() {
		_ = res.Body.Close()
	}()
	assert.NoError(t, err)
	assert.Equal(t, "example.com", tm.Host)
	assert.Equal(t, http.StatusBadGateway, res.StatusCode)
}

func TestReverseProxyPreservesRequestID(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test-request-id", r.Header.Get(middleware.RequestIDHeader))
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	tm := TestMatcher{
		RouteGroup: &RouteGroup{
			Domain: "example.com",
			Upstreams: []Upstream{{
				Server: backend.URL,
			}},
		},
		Result: true,
	}

	proxy := NewHttpProxy(":0", &tm)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/test", nil)
	req.Header.Set(middleware.RequestIDHeader, "test-request-id")

	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "test-request-id", rr.Header().Get(middleware.RequestIDHeader))
}

func TestReverseProxyHealth(t *testing.T) {
	tm := TestMatcher{
		RouteGroup: &RouteGroup{
			Domain: "example.com",
			Upstreams: []Upstream{{
				Server: "http://%",
			}},
		},
		Result: true,
	}
	proxy := NewHttpProxy(":0", &tm)

	getReq := httptest.NewRequest(http.MethodGet, "http://example.com/health", nil)

	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, getReq)

	res := rr.Result()
	defer func() {
		_ = res.Body.Close()
	}()

	body, err := io.ReadAll(res.Body)

	assert.NoError(t, err)
	assert.Equal(t, "ok", string(body))
	assert.Equal(t, int32(0), tm.calls.Load())
	assert.Equal(t, http.StatusOK, res.StatusCode)
}

func TestReverseProxyRejectsTooLargeRequestBody(t *testing.T) {
	var backendCalled atomic.Bool

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalled.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	tm := TestMatcher{
		RouteGroup: &RouteGroup{
			Domain: "example.com",
			Upstreams: []Upstream{{
				Server: backend.URL,
			}},
			MaxBodySize: 1,
		},
		Result: true,
	}
	proxy := NewHttpProxy(":0", &tm)
	req := httptest.NewRequest(http.MethodGet, "http://example.com/upload", strings.NewReader("test me"))
	rr := httptest.NewRecorder()

	proxy.ServeHTTP(rr, req)
	res := rr.Result()
	defer func() {
		_ = res.Body.Close()
	}()
	assert.False(t, backendCalled.Load())
	assert.Equal(t, http.StatusRequestEntityTooLarge, res.StatusCode)
}
func TestReverseProxyRejectsStreamingBodyOverLimit(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	tm := TestMatcher{
		RouteGroup: &RouteGroup{
			Domain: "example.com",
			Upstreams: []Upstream{{
				Server: backend.URL,
			}},
			MaxBodySize: 1,
		},
		Result: true,
	}

	proxy := NewHttpProxy(":0", &tm)

	req := httptest.NewRequest(http.MethodPost, "http://example.com/upload", strings.NewReader("test me"))
	req.ContentLength = -1

	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rr.Code)
}
