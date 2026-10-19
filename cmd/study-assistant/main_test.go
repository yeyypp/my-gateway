package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"shuai/gateway/internal/protocol"
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

		chatResp := protocol.Completion{
			ID:     "test-id",
			Object: "chat.completion",
			Model:  "model-a",
			Choices: []protocol.CompletionChoice{
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
		w.Header().Set("X-Gateway-Request-ID", "gateway-success-123")
		if err := json.NewEncoder(w).Encode(chatResp); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer gateway.Close()

	if err := runAnalyze(context.Background(), gateway.Client(), gateway.URL, filePath, model, &output, false); err != nil {
		t.Fatal(err.Error())
	}
	if got := output.String(); got != "X-Gateway-Request-ID:gateway-success-123\nresponse from mock-a" {
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
				Type:    protocol.ErrorTypeInvalidRequest,
				Param:   "model",
				Code:    protocol.ErrorCodeModelNotFound,
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
		false,
	)
	if err == nil {
		t.Fatal("expected a non-nil error")
	}

	wantError := `gateway request failed: http_status=400 Bad Request, type=invalid_request_error, code=model_not_found, param="model", message="model is not supported"`
	if err.Error() != wantError {
		t.Fatalf("expected error %q, got %q", wantError, err.Error())
	}
	if output.Len() != 0 {
		t.Fatalf("expected no success output, got %q", output.String())
	}
}

func TestRunAnalyzeReturnsReadableErrorForNonEnvelopeResponse(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(filePath, []byte("test content"), 0644); err != nil {
		t.Fatalf("write input file: %v", err)
	}

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Gateway-Request-ID", "gateway-error-123")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream failed"))
	}))
	defer gateway.Close()
	var output bytes.Buffer

	err := runAnalyze(
		context.Background(),
		gateway.Client(),
		gateway.URL,
		filePath,
		"model-a",
		&output,
		false,
	)
	if err == nil {
		t.Fatal("expected a non-nil error")
	}

	wantError := `gateway request failed: http_status=500 Internal Server Error, response="upstream failed"`
	if err.Error() != wantError {
		t.Fatalf("expected error %q, got %q", wantError, err.Error())
	}
	if got := output.String(); got != "X-Gateway-Request-ID:gateway-error-123\n" {
		t.Fatalf("unexpected output: %q", got)
	}
}

type failFirstWriter struct {
	err   error
	calls int
}

func (w *failFirstWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == 1 {
		return 0, w.err
	}
	return len(p), nil
}

func TestRunAnalyzeReturnsGatewayRequestIDOutputError(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(filePath, []byte("test content"), 0644); err != nil {
		t.Fatalf("write input file: %v", err)
	}

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Gateway-Request-ID", "gateway-output-error-123")
		if err := json.NewEncoder(w).Encode(protocol.Completion{
			Choices: []protocol.CompletionChoice{{
				Message: protocol.Message{Role: "assistant", Content: "response"},
			}},
		}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer gateway.Close()

	writeErr := errors.New("write request ID output")
	out := &failFirstWriter{err: writeErr}
	err := runAnalyze(context.Background(), gateway.Client(), gateway.URL, filePath, "model-a", out, false)
	if err == nil {
		t.Fatal("expected a non-nil Gateway request ID output error")
	}
	if out.calls != 1 {
		t.Fatalf("expected output to stop after the request ID write failed, got %d writes", out.calls)
	}
}
