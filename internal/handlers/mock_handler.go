package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"shuai/gateway/internal/log"
	"shuai/gateway/internal/protocol"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type MockAPIHandler struct {
	Name  string
	Model string
}

type mockResponseStatusWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *mockResponseStatusWriter) WriteHeader(statusCode int) {
	if w.statusCode != 0 {
		return
	}
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *mockResponseStatusWriter) Write(content []byte) (int, error) {
	if w.statusCode == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(content)
}

func NewMockAPIHandler(name, model string) *MockAPIHandler {
	return &MockAPIHandler{
		Name:  name,
		Model: model,
	}
}

func (m *MockAPIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	statusWriter := &mockResponseStatusWriter{ResponseWriter: w}
	upstreamRequestID := uuid.New().String()
	statusWriter.Header().Set(protocol.HeaderUpstreamRequestID, upstreamRequestID)
	defer func() {
		statusCode := statusWriter.statusCode
		if statusCode == 0 {
			statusCode = http.StatusOK
		}
		fields := []zap.Field{
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.String("upstream", m.Name),
			zap.String("model", m.Model),
			zap.String("upstream_request_id", upstreamRequestID),
			zap.Int("status", statusCode),
			zap.Duration("latency", time.Since(start)),
		}
		if gatewayRequestID := r.Header.Get(protocol.HeaderGatewayRequestID); gatewayRequestID != "" {
			fields = append(fields, zap.String("gateway_request_id", gatewayRequestID))
		}
		log.Info("mock request completed", fields...)
	}()
	w = statusWriter

	var chatCompletReq protocol.ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&chatCompletReq); err != nil {
		SendError(w, protocol.APIError{
			Message: err.Error(),
			Type:    protocol.ErrorTypeInvalidRequest,
			Code:    protocol.ErrorCodeInvalidJSON,
		}, http.StatusBadRequest)
		return
	}

	if chatCompletReq.Stream {
		SendError(w, protocol.APIError{
			Message: "streaming is not supported",
			Type:    protocol.ErrorTypeInvalidRequest,
			Param:   "stream",
			Code:    protocol.ErrorCodeUnsupportedValue,
		}, http.StatusBadRequest)
		return
	}

	resp := protocol.ChatCompletionResponse{
		ID:      "test id",
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   m.Model,
		Choices: []protocol.Choice{
			{
				Index: 0,
				Message: protocol.Message{
					Role:    "assistant",
					Content: fmt.Sprintf("response from %s", m.Name),
				},
				FinishReason: "stop",
			},
		},
	}
	SendJSON(w, resp)
}

func (m *MockAPIHandler) CheckHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}
