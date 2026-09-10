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

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type contextKey string

const (
	ChatReqKey         = contextKey("chatReqKey")
	RequestBodyKey     = contextKey("requestBodyKey")
	RequestIDKey       = contextKey("requestIDKey")
	ClientRequestIDKey = contextKey("clientRequestIDKey")
)

// maxRequestBodyBytes bounds the request body the Gateway reads from a client.
const maxRequestBodyBytes = 1 << 20

type responseStatusWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *responseStatusWriter) WriteHeader(statusCode int) {
	if w.statusCode != 0 {
		return
	}
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *responseStatusWriter) Write(content []byte) (int, error) {
	if w.statusCode == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(content)
}

func (w *responseStatusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func GetReqIDFromCTX(ctx context.Context) []zap.Field {
	fields := make([]zap.Field, 0, 2)
	if requestID, ok := ctx.Value(RequestIDKey).(string); ok && requestID != "" {
		fields = append(fields, zap.String("gateway_request_id", requestID))
	}
	if clientRequestID, ok := ctx.Value(ClientRequestIDKey).(string); ok && clientRequestID != "" {
		fields = append(fields, zap.String("client_request_id", clientRequestID))
	}
	return fields
}

// GetRequestBodyFromCTX returns the request body bytes that ValidateJSONRequest
// read and validated. Later stages must rewrite these bytes instead of reading
// r.Body again, so the dependency on the validation stage stays explicit.
func GetRequestBodyFromCTX(ctx context.Context) ([]byte, bool) {
	body, ok := ctx.Value(RequestBodyKey).([]byte)
	return body, ok
}

func ValidateJSONRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Limit the request body size to prevent abuse
		limitedBody := http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		defer limitedBody.Close()

		content, err := io.ReadAll(limitedBody)
		if err != nil {
			if _, ok := err.(*http.MaxBytesError); ok {
				log.Error("request body is larger than the Gateway limit", GetReqIDFromCTX(r.Context())...)
				handlers.SendError(w, protocol.APIError{
					Message: "request body too large",
					Type:    protocol.ErrorTypeInvalidRequest,
					Code:    protocol.ErrorCodeRequestTooLarge,
				}, http.StatusRequestEntityTooLarge)
			} else {
				fields := append(GetReqIDFromCTX(r.Context()), zap.Error(err))
				log.Error("read request body failed", fields...)
				handlers.SendError(w, protocol.APIError{
					Message: "read request body failed",
					Type:    protocol.ErrorTypeInvalidRequest,
					Code:    protocol.ErrorCodeRequestBodyReadFailed,
				}, http.StatusBadRequest)
			}
			return
		}

		// Restore r.Body so the request stays a complete, readable HTTP request,
		// and publish the validated bytes for the stages that rewrite it.
		r.Body = io.NopCloser(bytes.NewReader(content))
		r.ContentLength = int64(len(content))

		var chatReq protocol.ChatCompletionRequest
		err = json.Unmarshal(content, &chatReq)
		if err != nil {
			fields := append(GetReqIDFromCTX(r.Context()), zap.Error(err))
			log.Error("unmarshal chat request failed", fields...)
			handlers.SendError(w, protocol.APIError{
				Message: "invalid json",
				Type:    protocol.ErrorTypeInvalidRequest,
				Code:    protocol.ErrorCodeInvalidJSON,
			}, http.StatusBadRequest)
			return
		}

		if len(chatReq.Messages) == 0 {
			log.Error("chat request messages are empty", GetReqIDFromCTX(r.Context())...)
			handlers.SendError(w, protocol.APIError{
				Message: "messages must not be empty",
				Type:    protocol.ErrorTypeInvalidRequest,
				Param:   "messages",
				Code:    protocol.ErrorCodeInvalidValue,
			}, http.StatusBadRequest)
			return
		}

		ctx := context.WithValue(r.Context(), ChatReqKey, chatReq)
		ctx = context.WithValue(ctx, RequestBodyKey, content)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func RequestTimeout(timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			chatReq, ok := r.Context().Value(ChatReqKey).(protocol.ChatCompletionRequest)
			if !ok {
				handlers.SendError(w, protocol.APIError{
					Message: "chatReq is required",
					Type:    protocol.ErrorTypeInvalidRequest,
					Code:    protocol.ErrorCodeMissingRequiredParameter,
				}, http.StatusBadRequest)
				return
			}

			if chatReq.Stream {
				controller := http.NewResponseController(w)
				controller.SetWriteDeadline(time.Time{})
				next.ServeHTTP(w, r)
				return
			}

			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func GetGatewayRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := uuid.New().String()
		r.Header.Set(protocol.HeaderGatewayRequestID, requestID)
		w.Header().Set(protocol.HeaderGatewayRequestID, requestID)
		ctx := context.WithValue(r.Context(), RequestIDKey, requestID)
		if clientRequestID := r.Header.Get(protocol.HeaderClientRequestID); clientRequestID != "" {
			ctx = context.WithValue(ctx, ClientRequestIDKey, clientRequestID)
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func LogRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		statusWriter := &responseStatusWriter{ResponseWriter: w}

		next.ServeHTTP(statusWriter, r)

		statusCode := statusWriter.statusCode
		if statusCode == 0 {
			statusCode = http.StatusOK
		}
		fields := append(GetReqIDFromCTX(r.Context()),
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.Int("status", statusCode),
			zap.Duration("latency", time.Since(start)),
		)
		log.Info("gateway request completed", fields...)
	})
}
