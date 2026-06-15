package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		rec := newStatusRecorder(w)

		next.ServeHTTP(rec, r)

		if r.URL.Path != "/health" {
			logAccess(r, rec.status, rec.bytes, time.Since(start))
		}
	})
}

func logAccess(r *http.Request, status int, bytes int, duration time.Duration) {
	slog.Info(
		"access",
		"method", r.Method,
		"host", r.Host,
		"path", r.URL.Path,
		"query", r.URL.RawQuery,
		"status", status,
		"bytes", bytes,
		"duration_ms", duration.Milliseconds(),
		"remote_addr", r.RemoteAddr,
		"user_agent", r.UserAgent(),
		"request_id", r.Header.Get("X-Request-Id"),
	)
}
