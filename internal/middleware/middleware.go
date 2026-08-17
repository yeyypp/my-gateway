package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"shuai/gateway/internal/handlers"
	"time"
)

type contextKey string

const (
	ModelContextKey = contextKey("model")
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
				handlers.SendError(w, handlers.APIError{
					Message: "request body too large",
				}, http.StatusRequestEntityTooLarge)
			} else {
				log.Printf("gateway_error category=request_body_read_failed status=%d method=%s path=%s err=%v", http.StatusBadRequest, r.Method, r.URL.Path, err)
				handlers.SendError(w, handlers.APIError{
					Message: "read request body failed",
				}, http.StatusBadRequest)
			}
			return
		}

		// Replace the request body with a new ReadCloser containing the validated content
		r.Body = io.NopCloser(bytes.NewReader(content))
		r.ContentLength = int64(len(content))

		if !json.Valid(content) {
			log.Printf("gateway_error category=invalid_json status=%d method=%s path=%s", http.StatusBadRequest, r.Method, r.URL.Path)
			handlers.SendError(w, handlers.APIError{
				Message: "invalid json",
			}, http.StatusBadRequest)
			return
		}

		// check the model and save to r context
		var selector struct {
			Model string `json:"model"`
		}
		err = json.Unmarshal(content, &selector)
		if err != nil {
			handlers.SendError(w, handlers.APIError{
				Message: fmt.Sprintf("invalid json, err:%s", err.Error()),
			}, http.StatusBadRequest)
			return
		}

		ctx := context.WithValue(r.Context(), ModelContextKey, selector.Model)

		next.ServeHTTP(w, r.WithContext(ctx))
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
