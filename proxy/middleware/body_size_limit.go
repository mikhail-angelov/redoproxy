package middleware

import (
	"net/http"
)

type GetBodySizeLimit func(host string) (int64, error)

func BodySizeLimitMiddleware(next http.Handler, getBodySizeLimit GetBodySizeLimit) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		if getBodySizeLimit == nil {
			next.ServeHTTP(w, r)
			return
		}
		maxBodySize, err := getBodySizeLimit(r.Host)
		if err != nil || maxBodySize <= 0 {
			next.ServeHTTP(w, r)
			return
		}
		if r.ContentLength > maxBodySize {
			//ContentLength available
			http.Error(w, "request body is too large", http.StatusRequestEntityTooLarge)
			return
		}

		//if stream
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
		}
		next.ServeHTTP(w, r)
	})
}
