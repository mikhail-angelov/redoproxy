package middleware

import (
	"net"
	"net/http"
	"strings"
	"sync"
)

type GetConcurrentRequestsLimit func(host string) (int, error)

func ConcurrentRequestsLimitMiddleware(next http.Handler, getConcurrentRequestsLimit GetConcurrentRequestsLimit) http.Handler {
	var mu sync.Mutex
	active := make(map[string]int)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		if getConcurrentRequestsLimit == nil {
			next.ServeHTTP(w, r)
			return
		}
		host := normalizeHost(r.Host)

		limit, err := getConcurrentRequestsLimit(host)
		if err != nil || limit <= 0 {
			next.ServeHTTP(w, r)
			return
		}

		if !tryAcquireConnectionSlot(active, &mu, host, limit) {
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}

		defer releaseConnectionSlot(active, &mu, host)

		next.ServeHTTP(w, r)
	})
}

func tryAcquireConnectionSlot(active map[string]int, mu *sync.Mutex, host string, limit int) bool {
	mu.Lock()
	defer mu.Unlock()

	if active[host] >= limit {
		return false
	}

	active[host]++
	return true
}

func releaseConnectionSlot(active map[string]int, mu *sync.Mutex, host string) {
	mu.Lock()
	defer mu.Unlock()

	if active[host] <= 1 {
		delete(active, host)
		return
	}

	active[host]--
}

func normalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))

	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}

	return host
}
