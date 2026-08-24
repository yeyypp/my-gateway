package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"shuai/gateway/internal/protocol"
	"testing"
)

func TestRunAnalyzeSendsRequestAndPrintsResponse(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "test.txt")
	expectContent := "test content"
	if err := os.WriteFile(filePath, []byte(expectContent), 0644); err != nil {
		t.Fatal(err)
	}
	model := "model-a"
	var output bytes.Buffer

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected method %q, got %q", http.MethodPost, r.Method)
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("expected path %q, got %q", "/v1/chat/completions", r.URL.Path)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type %q, got %q", "application/json", r.Header.Get("Content-Type"))
			return
		}

		var chatReq protocol.ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&chatReq); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if chatReq.Model != model {
			t.Errorf("expected model %q, got %q", model, chatReq.Model)
		}
		if chatReq.Stream {
			t.Error("expected stream=false")
		}
		if len(chatReq.Messages) != 1 {
			t.Errorf("expected one message, got %d", len(chatReq.Messages))
		} else {
			message := chatReq.Messages[0]

			if message.Role != "user" {
				t.Errorf("expected role %q, got %q", "user", message.Role)
			}
			if message.Content != expectContent {
				t.Errorf("expected content %q, got %q", expectContent, message.Content)
			}
		}

		chatResp := protocol.ChatCompletionResponse{
			ID:     "test-id",
			Object: "chat.completion",
			Model:  "model-a",
			Choices: []protocol.Choice{
				{
					Index: 0,
					Message: protocol.Message{
						Role:    "assistant",
						Content: "response from mock-a",
					},
					FinishReason: "stop",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(chatResp); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer gateway.Close()

	if err := runAnalyze(context.Background(), gateway.Client(), gateway.URL, filePath, model, &output); err != nil {
		t.Fatal(err.Error())
	}
	if got := output.String(); got != "response from mock-a" {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestRunAnalyzeReturnsErrorForNon2xxResponse(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(filePath, []byte("test content"), 0644); err != nil {
		t.Fatalf("write input file: %v", err)
	}

	var output bytes.Buffer
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)

		errResp := protocol.ErrorResponse{
			Error: protocol.APIError{
				Message: "model is not supported",
				Type:    "invalid_request_error",
				Param:   "model",
				Code:    "model_not_found",
			},
		}
		if err := json.NewEncoder(w).Encode(errResp); err != nil {
			t.Errorf("encode error response: %v", err)
		}
	}))
	defer gateway.Close()

	err := runAnalyze(
		context.Background(),
		gateway.Client(),
		gateway.URL,
		filePath,
		"model-unknown",
		&output,
	)
	if err == nil {
		t.Fatal("expected a non-nil error")
	}

	wantError := "err msg:model is not supported, type:invalid_request_error"
	if err.Error() != wantError {
		t.Fatalf("expected error %q, got %q", wantError, err.Error())
	}
	if output.Len() != 0 {
		t.Fatalf("expected no success output, got %q", output.String())
	}
}
