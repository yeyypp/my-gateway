package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"shuai/gateway/internal/handlers"
	"time"
)

func ValidateJSONRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.Method
		if method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}

		// Limit the request body size to prevent abuse
		limitedBody := http.MaxBytesReader(w, r.Body, 1024*1024) // Limit request body to 1MB
		defer limitedBody.Close()

		content, err := io.ReadAll(limitedBody)
		if err != nil {
			if _, ok := err.(*http.MaxBytesError); ok {
				log.Printf("gateway_error category=request_too_large status=%d method=%s path=%s", http.StatusRequestEntityTooLarge, r.Method, r.URL.Path)
				handlers.ResponseJSON(w, http.StatusRequestEntityTooLarge, "request body too large", nil)
			} else {
				log.Printf("gateway_error category=request_body_read_failed status=%d method=%s path=%s err=%v", http.StatusBadRequest, r.Method, r.URL.Path, err)
				handlers.ResponseJSON(w, http.StatusBadRequest, "failed to read request body", nil)
			}
			return
		}

		if !json.Valid(content) {
			log.Printf("gateway_error category=invalid_json status=%d method=%s path=%s", http.StatusBadRequest, r.Method, r.URL.Path)
			handlers.ResponseJSON(w, http.StatusBadRequest, "invalid JSON format", nil)
			return
		}

		// Replace the request body with a new ReadCloser containing the validated content
		r.Body = io.NopCloser(bytes.NewBuffer(content))
		r.ContentLength = int64(len(content))

		next.ServeHTTP(w, r)
	})
}

func RequestTimeout(timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()

			start := time.Now()
			next.ServeHTTP(w, r.WithContext(ctx))
			duration := time.Since(start)

			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				log.Printf("proxy_error category=upstream_timeout status=%d method=%s path=%s duration_ms=%d",
					http.StatusGatewayTimeout,
					r.Method,
					r.URL.Path,
					duration.Milliseconds(),
				)
			}
		})
	}
}
