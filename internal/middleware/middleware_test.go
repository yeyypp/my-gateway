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

func TestGetGatewayRequestID(t *testing.T) {
	const clientSuppliedID = "client-supplied-id"

	var contextRequestID string
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ok bool
		contextRequestID, ok = r.Context().Value(RequestIDKey).(string)
		if !ok {
			t.Fatal("expected Gateway request ID in request context")
		}
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("X-Gateway-Request-ID", clientSuppliedID)
	recorder := httptest.NewRecorder()

	GetGatewayRequestID(downstream).ServeHTTP(recorder, req)

	result := recorder.Result()
	defer result.Body.Close()

	responseIDs := result.Header.Values("X-Gateway-Request-ID")
	if len(responseIDs) != 1 {
		t.Fatalf("expected exactly one Gateway request ID, got %q", responseIDs)
	}
	responseRequestID := responseIDs[0]
	if responseRequestID == "" {
		t.Fatal("expected a non-empty Gateway request ID")
	}
	if responseRequestID == clientSuppliedID {
		t.Fatal("client-supplied request ID must not control the Gateway request ID")
	}
	if contextRequestID != responseRequestID {
		t.Fatalf("expected context request ID %q to match response request ID %q", contextRequestID, responseRequestID)
	}
}

func TestGetGatewayRequestIDGeneratesDifferentIDs(t *testing.T) {
	handler := GetGatewayRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	requestID := func() string {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		handler.ServeHTTP(recorder, req)

		result := recorder.Result()
		defer result.Body.Close()
		return result.Header.Get("X-Gateway-Request-ID")
	}

	firstID := requestID()
	secondID := requestID()
	if firstID == "" || secondID == "" {
		t.Fatalf("expected non-empty Gateway request IDs, got first=%q second=%q", firstID, secondID)
	}
	if firstID == secondID {
		t.Fatalf("expected different Gateway request IDs, got %q twice", firstID)
	}
}

func TestGetGatewayRequestIDIsReturnedForValidationFailure(t *testing.T) {
	downstreamCalled := false
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downstreamCalled = true
	})
	handler := GetGatewayRequestID(ValidateJSONRequest(downstream))

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{"))
	handler.ServeHTTP(recorder, req)

	if downstreamCalled {
		t.Fatal("expected invalid JSON to be rejected before reaching downstream")
	}
	result := recorder.Result()
	defer result.Body.Close()
	if result.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status code %d, got %d", http.StatusBadRequest, result.StatusCode)
	}
	requestIDs := result.Header.Values("X-Gateway-Request-ID")
	if len(requestIDs) != 1 || requestIDs[0] == "" {
		t.Fatalf("expected exactly one non-empty Gateway request ID, got %q", requestIDs)
	}
}

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
				Type:    protocol.ErrorTypeInvalidRequest,
				Param:   "messages",
				Code:    protocol.ErrorCodeInvalidValue,
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
				Type:    protocol.ErrorTypeInvalidRequest,
				Param:   "stream",
				Code:    protocol.ErrorCodeUnsupportedValue,
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
