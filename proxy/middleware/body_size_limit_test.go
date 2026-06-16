package middleware

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBodySizeLimitMiddlewarePassesWhenBodyWithinLimit(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	handler := BodySizeLimitMiddleware(next, func(host string) (int64, error) {
		return 10, nil
	})
	req := httptest.NewRequest("POST", "/upload", strings.NewReader("test"))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rr.Code)
}
func TestBodySizeLimitMiddlewarePassesWhenWithLimit(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)

		var maxBytesErr *http.MaxBytesError
		assert.True(t, errors.As(err, &maxBytesErr))

		http.Error(w, "request body is too large", http.StatusRequestEntityTooLarge)
	})
	handler := BodySizeLimitMiddleware(next, func(host string) (int64, error) {
		return 1, nil
	})
	req := httptest.NewRequest("POST", "/upload", strings.NewReader("test"))
	req.ContentLength = -1 // simulate streaming/chunked request
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rr.Code)
}
