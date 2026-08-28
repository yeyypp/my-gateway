package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"shuai/gateway/internal/handlers"
	"shuai/gateway/internal/log"
	"shuai/gateway/internal/protocol"

	"go.uber.org/zap"
)

type contextKey string

const (
	ChatReqKey = contextKey("chatReqKey")
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
				log.Error("request body is larger than 1 MiB")
				handlers.SendError(w, protocol.APIError{
					Message: "request body too large",
					Type:    protocol.ErrorTypeInvalidRequest,
					Code:    protocol.ErrorCodeRequestTooLarge,
				}, http.StatusRequestEntityTooLarge)
			} else {
				log.Error("read request body failed", zap.Error(err))
				handlers.SendError(w, protocol.APIError{
					Message: "read request body failed",
					Type:    protocol.ErrorTypeInvalidRequest,
					Code:    protocol.ErrorCodeRequestBodyReadFailed,
				}, http.StatusBadRequest)
			}
			return
		}

		// Replace the request body with a new ReadCloser containing the validated content
		r.Body = io.NopCloser(bytes.NewReader(content))
		r.ContentLength = int64(len(content))

		var chatReq protocol.ChatCompletionRequest
		err = json.Unmarshal(content, &chatReq)
		if err != nil {
			log.Error("unmarshal chat request failed", zap.Error(err))
			handlers.SendError(w, protocol.APIError{
				Message: "invalid json",
				Type:    protocol.ErrorTypeInvalidRequest,
				Code:    protocol.ErrorCodeInvalidJSON,
			}, http.StatusBadRequest)
			return
		}

		if len(chatReq.Messages) == 0 {
			log.Error("chat request messages are empty")
			handlers.SendError(w, protocol.APIError{
				Message: "messages must not be empty",
				Type:    protocol.ErrorTypeInvalidRequest,
				Param:   "messages",
				Code:    protocol.ErrorCodeInvalidValue,
			}, http.StatusBadRequest)
			return
		}

		if chatReq.Stream {
			log.Error("streaming is not supported", zap.Bool("stream", chatReq.Stream))
			handlers.SendError(w, protocol.APIError{
				Message: "streaming is not supported",
				Type:    protocol.ErrorTypeInvalidRequest,
				Param:   "stream",
				Code:    protocol.ErrorCodeUnsupportedValue,
			}, http.StatusBadRequest)
			return
		}

		ctx := context.WithValue(r.Context(), ChatReqKey, chatReq)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func RequestTimeout(timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
