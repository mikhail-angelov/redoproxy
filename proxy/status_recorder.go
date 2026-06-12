package proxy

import (
	"bufio"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

type StatusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func newStatusRecorder(w http.ResponseWriter) *StatusRecorder {
	return &StatusRecorder{
		ResponseWriter: w,
		status:         http.StatusOK,
	}
}

func (r *StatusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}

	r.wroteHeader = true
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *StatusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.wroteHeader = true
		r.status = http.StatusOK
	}

	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func (r *StatusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *StatusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("underlying response writer does not support hijacking")
	}
	return h.Hijack()
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
