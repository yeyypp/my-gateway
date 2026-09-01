package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"shuai/gateway/internal/protocol"
)

type MockAPIHandler struct {
	Name  string
	Model string
}

func NewMockAPIHandler(name, model string) *MockAPIHandler {
	return &MockAPIHandler{
		Name:  name,
		Model: model,
	}
}

func (m *MockAPIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
