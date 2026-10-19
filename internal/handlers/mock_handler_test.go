package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"shuai/gateway/internal/log"
	"shuai/gateway/internal/protocol"

	"github.com/openai/openai-go/v3"
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
	if errResp.Error.Type != protocol.ErrorTypeInvalidRequest {
		t.Fatalf("expected error type %q, got %q", protocol.ErrorTypeInvalidRequest, errResp.Error.Type)
	}
	if errResp.Error.Code != protocol.ErrorCodeInvalidJSON {
		t.Fatalf("expected error code %q, got %q", protocol.ErrorCodeInvalidJSON, errResp.Error.Code)
	}

}

func TestNormalRequest(t *testing.T) {
	logPath := t.TempDir() + "/mock.log"
	if err := log.Init("info", logPath); err != nil {
		t.Fatalf("initialize logger: %v", err)
	}

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
	req.Header.Set(protocol.HeaderGatewayRequestID, "gateway-123")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	result := recorder.Result()
	defer result.Body.Close()

	if result.StatusCode != http.StatusOK {
		t.Fatalf("expected status code %d, got %d", http.StatusOK, result.StatusCode)
	}
	upstreamRequestID := result.Header.Get(protocol.HeaderUpstreamRequestID)
	if upstreamRequestID == "" {
		t.Fatal("expected a non-empty upstream request ID")
	}
	if got := result.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected Content-Type %q, got %q", "application/json", got)
	}

	var chatResp openai.ChatCompletion
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

	if err := log.Sync(); err != nil {
		t.Fatalf("sync logger: %v", err)
	}
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var entry map[string]any
	if err := json.Unmarshal(content, &entry); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	if got := entry["msg"]; got != "mock request completed" {
		t.Fatalf("expected mock completion log, got %#v", got)
	}
	if got := entry["gateway_request_id"]; got != "gateway-123" {
		t.Fatalf("expected Gateway request ID %q, got %#v", "gateway-123", got)
	}
	if got := entry["upstream_request_id"]; got != upstreamRequestID {
		t.Fatalf("expected upstream request ID %q, got %#v", upstreamRequestID, got)
	}
	if got := entry["upstream"]; got != "mock-a" {
		t.Fatalf("expected upstream %q, got %#v", "mock-a", got)
	}
	if got := entry["model"]; got != "model-a" {
		t.Fatalf("expected model %q, got %#v", "model-a", got)
	}
	if got := entry["status"]; got != float64(http.StatusOK) {
		t.Fatalf("expected status %d, got %#v", http.StatusOK, got)
	}
	if _, ok := entry["latency"]; !ok {
		t.Fatal("expected latency field")
	}
}

func TestMockHealthCheck(t *testing.T) {
	handler := NewMockAPIHandler("mock-a", "model-a")
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	handler.CheckHealth(recorder, req)

	result := recorder.Result()
	defer result.Body.Close()
	if result.StatusCode != http.StatusOK {
		t.Fatalf("expected status code %d, got %d", http.StatusOK, result.StatusCode)
	}
}
