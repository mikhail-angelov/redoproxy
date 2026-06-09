package proxy

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type TestMatcher struct {
	Route  ContainerRoute
	Result bool
	Host   string
}

func (tm *TestMatcher) Lookup(host string) (ContainerRoute, bool) {
	tm.Host = host
	return tm.Route, tm.Result
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

		w.Header().Set("X-Backend", "test-backend")
		w.WriteHeader(http.StatusOK)

		_, _ = w.Write([]byte("backend response"))
	}))
	defer backend.Close()
	tm := TestMatcher{
		Route: ContainerRoute{
			Server: backend.URL,
			Domain: "example.com",
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
		t.Fatal(msg)
	default:
	}

	if !backendCalled.Load() {
		t.Fatal("expected backend to be called")
	}

	if tm.Host != "example.com" {
		t.Fatalf("expected matcher host example.com, got %q", tm.Host)
	}

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", res.StatusCode)
	}

	if got := res.Header.Get("X-Backend"); got != "test-backend" {
		t.Fatalf("expected X-Backend header test-backend, got %q", got)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	if string(body) != "backend response" {
		t.Fatalf("expected backend response body, got %q", string(body))
	}

}
func TestReverseProxyInvalidDomain(t *testing.T) {
	var backendCalled atomic.Bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalled.Store(true)
	}))
	defer backend.Close()
	tm := TestMatcher{
		Route: ContainerRoute{
			Server: backend.URL,
			Domain: "example.com",
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

	if backendCalled.Load() {
		t.Fatal("expected backend not to be called")
	}

	if tm.Host != "invalid.com" {
		t.Fatalf("expected matcher host invalid.com, got %q", tm.Host)
	}

	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", res.StatusCode)
	}
}
func TestReverseProxyInvalidUrl(t *testing.T) {
	tm := TestMatcher{
		Route: ContainerRoute{
			Server: "http://%",
			Domain: "example.com",
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
	if tm.Host != "example.com" {
		t.Fatalf("expected matcher host example.com, got %q", tm.Host)
	}

	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected status 502, got %d", res.StatusCode)
	}
}

func TestReverseProxyUpstreamConnectionRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	addr := ln.Addr().String()

	if err := ln.Close(); err != nil {
		t.Fatalf("failed to close listener: %v", err)
	}

	tm := TestMatcher{
		Route: ContainerRoute{
			Server: "http://" + addr,
			Domain: "example.com",
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

	if tm.Host != "example.com" {
		t.Fatalf("expected matcher host example.com, got %q", tm.Host)
	}

	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected status 502, got %d", res.StatusCode)
	}
}

func TestReverseProxyHealth(t *testing.T) {
	tm := TestMatcher{
		Route: ContainerRoute{
			Server: "http://%",
			Domain: "example.com",
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
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}

	if string(body) != "ok" {
		t.Fatalf("expected body ok, got %q", string(body))
	}

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", res.StatusCode)
	}
}
