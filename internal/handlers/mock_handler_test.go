package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"shuai/gateway/internal/protocol"
	"strings"
	"testing"
)

func TestRejectsInvalidJSON(t *testing.T) {
	handler := NewMockAPIHandler("mock-a", "model-a")
	req := httptest.NewRequest("POST", "http://test", strings.NewReader("{"))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	result := recorder.Result()
	defer result.Body.Close()

	if result.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status code %d, got %d", http.StatusBadRequest, result.StatusCode)
	}
	if got := result.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected Content-Type %q, got %q", "application/json", got)
	}

	var errResp protocol.ErrorResponse
	if err := json.NewDecoder(result.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	if errResp.Error.Message == "" {
		t.Fatal("expected a non-empty error message")
	}
	if errResp.Error.Type != "invalid_request_error" {
		t.Fatalf("expected error type %q, got %q", "invalid_request_error", errResp.Error.Type)
	}
	if errResp.Error.Code != "invalid_json" {
		t.Fatalf("expected error code %q, got %q", "invalid_json", errResp.Error.Code)
	}

}

func TestNormalRequest(t *testing.T) {
	handler := NewMockAPIHandler("mock-a", "model-a")
	chatReq := protocol.ChatCompletionRequest{
		Model: "model-a",
		Messages: []protocol.Message{{
			Role:    "user",
			Content: "test",
		}},
		Stream: false,
	}
	byteContents, err := json.Marshal(chatReq)
	if err != nil {
		t.Fatalf("failed to marshal chat request: %v", err)
	}
	req := httptest.NewRequest("POST", "http://test", bytes.NewBuffer(byteContents))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	result := recorder.Result()
	defer result.Body.Close()

	if result.StatusCode != http.StatusOK {
		t.Fatalf("expected status code %d, got %d", http.StatusOK, result.StatusCode)
	}
	if got := result.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected Content-Type %q, got %q", "application/json", got)
	}

	var chatResp protocol.ChatCompletionResponse
	if err := json.NewDecoder(result.Body).Decode(&chatResp); err != nil {
		t.Fatalf("failed to decode chat response: %v", err)
	}
	if chatResp.ID == "" {
		t.Fatal("expected a non-empty response ID")
	}
	if chatResp.Object != "chat.completion" {
		t.Fatalf("expected response object %q, got %q", "chat.completion", chatResp.Object)
	}
	if chatResp.Created <= 0 {
		t.Fatalf("expected a positive creation timestamp, got %d", chatResp.Created)
	}
	if chatResp.Model != "model-a" {
		t.Fatalf("expected response model %q, got %q", "model-a", chatResp.Model)
	}
	if len(chatResp.Choices) != 1 {
		t.Fatalf("expected exactly one response choice, got %d", len(chatResp.Choices))
	}

	choice := chatResp.Choices[0]
	if choice.Index != 0 {
		t.Fatalf("expected choice index %d, got %d", 0, choice.Index)
	}
	if choice.Message.Role != "assistant" {
		t.Fatalf("expected choice message role %q, got %q", "assistant", choice.Message.Role)
	}
	if choice.Message.Content != "response from mock-a" {
		t.Fatalf("expected choice message content %q, got %q", "response from mock-a", choice.Message.Content)
	}
	if choice.FinishReason != "stop" {
		t.Fatalf("expected finish reason %q, got %q", "stop", choice.FinishReason)
	}
}
