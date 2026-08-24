package middleware

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shuai/gateway/internal/protocol"
)

func TestValidateJSONRequestRejectsUnsupportedChatRequests(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantError protocol.APIError
	}{
		{
			name: "empty messages",
			body: `{
				"model":"model-a",
				"messages":[]
			}`,
			wantError: protocol.APIError{
				Message: "messages must not be empty",
				Type:    "invalid_request_error",
				Param:   "messages",
				Code:    "invalid_value",
			},
		},
		{
			name: "stream true",
			body: `{
				"model":"model-a",
				"messages":[{"role":"user","content":"test"}],
				"stream":true
			}`,
			wantError: protocol.APIError{
				Message: "streaming is not supported",
				Type:    "invalid_request_error",
				Param:   "stream",
				Code:    "unsupported_value",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			downstreamCalled := false
			downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				downstreamCalled = true
				w.WriteHeader(http.StatusNoContent)
			})
			handler := ValidateJSONRequest(downstream)

			req := httptest.NewRequest(
				http.MethodPost,
				"/v1/chat/completions",
				strings.NewReader(tc.body),
			)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, req)

			if downstreamCalled {
				t.Fatal("expected request to be rejected before reaching downstream")
			}

			result := recorder.Result()
			defer result.Body.Close()

			if result.StatusCode != http.StatusBadRequest {
				t.Fatalf(
					"expected status code %d, got %d",
					http.StatusBadRequest,
					result.StatusCode,
				)
			}
			if got := result.Header.Get("Content-Type"); got != "application/json" {
				t.Fatalf("expected Content-Type %q, got %q", "application/json", got)
			}

			decoder := json.NewDecoder(result.Body)
			var got protocol.ErrorResponse
			if err := decoder.Decode(&got); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if got.Error != tc.wantError {
				t.Fatalf("expected error %+v, got %+v", tc.wantError, got.Error)
			}
			if err := decoder.Decode(&struct{}{}); err != io.EOF {
				t.Fatalf("expected exactly one JSON response, got trailing data: %v", err)
			}
		})
	}
}
