package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
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
	var chatCompletReq chatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&chatCompletReq); err != nil {
		SendError(w, err)
		return
	}

	if chatCompletReq.Stream {
		SendError(w, fmt.Errorf("not support stream"))
		return
	}

	resp := chatCompletionResponse{
		Created: time.Now().Unix(),
		Model:   m.Model,
	}
	SendJSON(w, resp)
}

type chatCompletionRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
	Usage   usage        `json:"usage"`
}

type chatChoice struct {
	Index        int         `json:"index"`
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
