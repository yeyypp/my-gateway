package middleware

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"shuai/gateway/internal/log"
	"shuai/gateway/internal/protocol"
)

func TestGetGatewayRequestID(t *testing.T) {
	const (
		forgedGatewayID = "client-supplied-gateway-id"
		clientRequestID = "client-request-id"
	)

	var contextRequestID string
	var contextClientRequestID string
	var forwardedRequestID string
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ok bool
		contextRequestID, ok = r.Context().Value(RequestIDKey).(string)
		if !ok {
			t.Fatal("expected Gateway request ID in request context")
		}
		contextClientRequestID, ok = r.Context().Value(ClientRequestIDKey).(string)
		if !ok {
			t.Fatal("expected client request ID in request context")
		}
		forwardedRequestID = r.Header.Get(protocol.HeaderGatewayRequestID)
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set(protocol.HeaderGatewayRequestID, forgedGatewayID)
	req.Header.Set(protocol.HeaderClientRequestID, clientRequestID)
	recorder := httptest.NewRecorder()

	GetGatewayRequestID(downstream).ServeHTTP(recorder, req)

	result := recorder.Result()
	defer result.Body.Close()

	responseIDs := result.Header.Values(protocol.HeaderGatewayRequestID)
	if len(responseIDs) != 1 {
		t.Fatalf("expected exactly one Gateway request ID, got %q", responseIDs)
	}
	responseRequestID := responseIDs[0]
	if responseRequestID == "" {
		t.Fatal("expected a non-empty Gateway request ID")
	}
	if responseRequestID == forgedGatewayID {
		t.Fatal("client-supplied request ID must not control the Gateway request ID")
	}
	if contextClientRequestID != clientRequestID {
		t.Fatalf("expected client request ID %q in context, got %q", clientRequestID, contextClientRequestID)
	}
	if contextRequestID != responseRequestID {
		t.Fatalf("expected context request ID %q to match response request ID %q", contextRequestID, responseRequestID)
	}
	if forwardedRequestID != responseRequestID {
		t.Fatalf("expected forwarded request ID %q to match response request ID %q", forwardedRequestID, responseRequestID)
	}
}

func TestLogRequestRecordsCorrelationFields(t *testing.T) {
	logPath := t.TempDir() + "/gateway.log"
	if err := log.Init("info", logPath); err != nil {
		t.Fatalf("initialize logger: %v", err)
	}

	handler := GetGatewayRequestID(LogRequest(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set(protocol.HeaderClientRequestID, "client-123")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)
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
	if got := entry["msg"]; got != "gateway request completed" {
		t.Fatalf("expected completion log, got %#v", got)
	}
	if got := entry["client_request_id"]; got != "client-123" {
		t.Fatalf("expected client request ID %q, got %#v", "client-123", got)
	}
	gatewayRequestID, ok := entry["gateway_request_id"].(string)
	if !ok || gatewayRequestID == "" {
		t.Fatalf("expected non-empty Gateway request ID, got %#v", entry["gateway_request_id"])
	}
	if got := entry["method"]; got != http.MethodPost {
		t.Fatalf("expected method %q, got %#v", http.MethodPost, got)
	}
	if got := entry["path"]; got != "/v1/chat/completions" {
		t.Fatalf("expected path %q, got %#v", "/v1/chat/completions", got)
	}
	if got := entry["status"]; got != float64(http.StatusAccepted) {
		t.Fatalf("expected status %d, got %#v", http.StatusAccepted, got)
	}
	if _, ok := entry["latency"]; !ok {
		t.Fatal("expected latency field")
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
