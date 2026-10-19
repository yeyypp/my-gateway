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

func (w *mockResponseStatusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
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
			Message: "Invalid JSON request body.",
			Type:    protocol.ErrorTypeInvalidRequest,
			Code:    protocol.ErrorCodeInvalidJSON,
		}, http.StatusBadRequest)
		return
	}

	id, created := "chatcmpl-"+uuid.New().String(), time.Now().Unix()
	content := fmt.Sprintf("response from %s", m.Name)
	if chatCompletReq.Stream {
		m.sendStream(w, r, id, created, content)
		return
	}
	resp := protocol.Completion{
		ID:      id,
		Object:  "chat.completion",
		Created: created,
		Model:   m.Model,
		Choices: []protocol.CompletionChoice{
			{
				Index: 0,
				Message: protocol.Message{
					Role:    "assistant",
					Content: content,
				},
				FinishReason: "stop",
			},
		},
	}
	SendJSON(w, resp)
}

func (m *MockAPIHandler) sendStream(w http.ResponseWriter, r *http.Request, id string, created int64, content string) {
	controller := http.NewResponseController(w)
	// Check support before committing the SSE response.
	underlying := w
	for {
		if _, ok := underlying.(http.Flusher); ok {
			break
		}
		if wrapped, ok := underlying.(interface{ Unwrap() http.ResponseWriter }); ok {
			underlying = wrapped.Unwrap()
			continue
		}
		SendError(w, protocol.ErrorForStatus(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	stop := "stop"
	choices := []protocol.ChunkChoice{
		{Delta: protocol.ChunkDelta{Role: "assistant"}},
		{Delta: protocol.ChunkDelta{Content: content}},
		{Delta: protocol.ChunkDelta{}, FinishReason: &stop},
	}
	for _, choice := range choices {
		if r.Context().Err() != nil {
			return
		}
		chunk := protocol.CompletionChunk{
			ID: id, Object: "chat.completion.chunk", Created: created, Model: m.Model,
			Choices: []protocol.ChunkChoice{choice},
		}
		data, err := json.Marshal(chunk)
		if err != nil {
			log.Error("encode mock stream chunk failed", zap.Error(err))
			return
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			log.Error("write mock stream chunk failed", zap.Error(err))
			return
		}
		if err := controller.Flush(); err != nil {
			log.Error("flush mock stream chunk failed", zap.Error(err))
			return
		}
	}
	if r.Context().Err() != nil {
		return
	}
	if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
		log.Error("write mock stream completion failed", zap.Error(err))
		return
	}
	if err := controller.Flush(); err != nil {
		log.Error("flush mock stream completion failed", zap.Error(err))
	}
}

func (m *MockAPIHandler) CheckHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}
