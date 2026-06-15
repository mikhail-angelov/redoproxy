package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConnectionLimitMiddlewarePassesWhenNoLimit(t *testing.T) {
	called := false

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := ConcurrentRequestsLimitMiddleware(next, func(host string) (int, error) {
		return 0, nil
	})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/test", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestConnectionLimitMiddlewareRejectsWhenLimitExceeded(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusOK)
	})

	handler := ConcurrentRequestsLimitMiddleware(next, func(host string) (int, error) {
		return 1, nil
	})

	req1 := httptest.NewRequest(http.MethodGet, "http://example.com/test", nil)
	rr1 := httptest.NewRecorder()

	go handler.ServeHTTP(rr1, req1)

	<-started

	req2 := httptest.NewRequest(http.MethodGet, "http://example.com/test", nil)
	rr2 := httptest.NewRecorder()

	handler.ServeHTTP(rr2, req2)

	assert.Equal(t, http.StatusTooManyRequests, rr2.Code)

	close(release)
}

func TestConnectionLimitMiddlewareReleasesSlot(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := ConcurrentRequestsLimitMiddleware(next, func(host string) (int, error) {
		return 1, nil
	})

	req1 := httptest.NewRequest(http.MethodGet, "http://example.com/test", nil)
	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, req1)

	req2 := httptest.NewRequest(http.MethodGet, "http://example.com/test", nil)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)

	assert.Equal(t, http.StatusOK, rr1.Code)
	assert.Equal(t, http.StatusOK, rr2.Code)
}
