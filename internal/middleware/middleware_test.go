package middleware

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"shuai/gateway/internal/log"
	"shuai/gateway/internal/protocol"
	"shuai/gateway/internal/ratelimit"
)

func TestValidateJSONRequestPublishesValidatedBody(t *testing.T) {
	const body = `{"model":"model-a","messages":[{"role":"user","content":"test"}],"temperature":0.7}`

	var published []byte
	var publishedOK bool
	var restored []byte
	var restoredErr error
	var routedModel string

	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		published, publishedOK = GetRequestBodyFromCTX(r.Context())
		if chatReq, ok := r.Context().Value(ChatReqKey).(protocol.ChatCompletionRequest); ok {
			routedModel = chatReq.Model
		}
		restored, restoredErr = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	recorder := httptest.NewRecorder()

	ValidateJSONRequest(downstream).ServeHTTP(recorder, req)

	result := recorder.Result()
	defer result.Body.Close()
	if result.StatusCode != http.StatusNoContent {
		t.Fatalf("expected status code %d, got %d", http.StatusNoContent, result.StatusCode)
	}
	if !publishedOK {
		t.Fatal("expected the validated request body in the request context")
	}
	if string(published) != body {
		t.Fatalf("expected published body %s, got %s", body, published)
	}
	if routedModel != "model-a" {
		t.Fatalf("expected routed model %q in context, got %q", "model-a", routedModel)
	}
	if restoredErr != nil {
		t.Fatalf("read restored body: %v", restoredErr)
	}
	if string(restored) != body {
		t.Fatalf("expected restored r.Body %s, got %s", body, restored)
	}
}

func TestSetGatewayRequestID(t *testing.T) {
	const (
		forgedGatewayID = "client-supplied-gateway-id"
		clientRequestID = "client-request-id"
	)

	var contextRequestID string
	var contextClientRequestID string
	var forwardedRequestID string
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ok bool
		contextRequestID, ok = r.Context().Value(GatewayReqIDKey).(string)
		if !ok {
			t.Fatal("expected Gateway request ID in request context")
		}
		contextClientRequestID, ok = r.Context().Value(ClientReqIDKey).(string)
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

	SetGatewayRequestID(downstream).ServeHTTP(recorder, req)

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

	handler := SetGatewayRequestID(LogRequest(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestSetGatewayRequestIDGeneratesDifferentIDs(t *testing.T) {
	handler := SetGatewayRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestSetGatewayRequestIDIsReturnedForValidationFailure(t *testing.T) {
	downstreamCalled := false
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downstreamCalled = true
	})
	handler := SetGatewayRequestID(ValidateJSONRequest(downstream))

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

func TestAuthorization(t *testing.T) {
	t.Setenv("LOCAL_CLIENT_KEY_A", "sk-client-a")
	t.Setenv("LOCAL_CLIENT_KEY_B", "sk-client-b")

	tests := []struct {
		name          string
		authorization string
		wantStatus    int
		wantClientID  string
	}{
		{name: "missing authorization", wantStatus: http.StatusUnauthorized},
		{name: "malformed authorization", authorization: "Basic credentials", wantStatus: http.StatusUnauthorized},
		{name: "unknown bearer token", authorization: "Bearer unknown", wantStatus: http.StatusUnauthorized},
		{name: "client A", authorization: "Bearer sk-client-a", wantStatus: http.StatusAccepted, wantClientID: "client-A"},
		{name: "client B", authorization: "Bearer sk-client-b", wantStatus: http.StatusAccepted, wantClientID: "client-B"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			downstreamCalls := 0
			gotClientID := ""
			downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				downstreamCalls++
				gotClientID = GetClientIDFromCTX(r.Context())
				w.WriteHeader(http.StatusAccepted)
			})
			handler := SetGatewayRequestID(Authorization(downstream))
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
			}
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, req)

			result := recorder.Result()
			defer result.Body.Close()
			if result.StatusCode != tc.wantStatus {
				t.Fatalf("expected status %d, got %d", tc.wantStatus, result.StatusCode)
			}
			if got := result.Header.Get(protocol.HeaderGatewayRequestID); got == "" {
				t.Fatal("expected a Gateway request ID on the response")
			}
			if tc.wantClientID == "" {
				if downstreamCalls != 0 {
					t.Fatalf("expected rejected request not to reach downstream, got %d calls", downstreamCalls)
				}
				return
			}
			if downstreamCalls != 1 {
				t.Fatalf("expected one downstream call, got %d", downstreamCalls)
			}
			if gotClientID != tc.wantClientID {
				t.Fatalf("expected client ID %q, got %q", tc.wantClientID, gotClientID)
			}
		})
	}
}

type fixedBudgetLimiter struct {
	mu        sync.Mutex
	remaining int
}

func (l *fixedBudgetLimiter) Allow(tokens int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.remaining < tokens {
		return false
	}
	l.remaining -= tokens
	return true
}

func TestRateLimitIsolatesClientsAndRejectsBeforeDownstream(t *testing.T) {
	t.Setenv("LOCAL_CLIENT_KEY_A", "sk-client-a")
	t.Setenv("LOCAL_CLIENT_KEY_B", "sk-client-b")

	downstreamClients := make([]string, 0, 2)
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downstreamClients = append(downstreamClients, GetClientIDFromCTX(r.Context()))
		w.WriteHeader(http.StatusNoContent)
	})
	handler := SetGatewayRequestID(Authorization(rateLimitWithFactory(func() ratelimit.Limiter {
		return &fixedBudgetLimiter{remaining: 1}
	})(downstream)))

	request := func(key string) *http.Response {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder.Result()
	}

	firstA := request("sk-client-a")
	defer firstA.Body.Close()
	if firstA.StatusCode != http.StatusNoContent {
		t.Fatalf("expected client A's first request to succeed, got %d", firstA.StatusCode)
	}

	secondA := request("sk-client-a")
	defer secondA.Body.Close()
	if secondA.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected client A's second request to be rate limited, got %d", secondA.StatusCode)
	}
	if got := secondA.Header.Get(protocol.HeaderGatewayRequestID); got == "" {
		t.Fatal("expected the rate-limited response to carry a Gateway request ID")
	}
	var errorResponse protocol.ErrorResponse
	if err := json.NewDecoder(secondA.Body).Decode(&errorResponse); err != nil {
		t.Fatalf("decode rate-limit response: %v", err)
	}
	if errorResponse.Error.Code != protocol.ErrorCodeRateLimit {
		t.Fatalf("expected rate-limit error code %q, got %q", protocol.ErrorCodeRateLimit, errorResponse.Error.Code)
	}
	if len(downstreamClients) != 1 {
		t.Fatalf("expected rate-limited request not to reach downstream, got clients %q", downstreamClients)
	}

	firstB := request("sk-client-b")
	defer firstB.Body.Close()
	if firstB.StatusCode != http.StatusNoContent {
		t.Fatalf("expected client B not to share client A's exhausted budget, got %d", firstB.StatusCode)
	}
	wantClients := []string{"client-A", "client-B"}
	if len(downstreamClients) != len(wantClients) {
		t.Fatalf("expected downstream clients %q, got %q", wantClients, downstreamClients)
	}
	for i := range wantClients {
		if downstreamClients[i] != wantClients[i] {
			t.Fatalf("expected downstream clients %q, got %q", wantClients, downstreamClients)
		}
	}
}

func TestLogRequestRecordsAdmissionRejections(t *testing.T) {
	t.Setenv("LOCAL_CLIENT_KEY_A", "sk-client-a")

	tests := []struct {
		name          string
		authorization string
		wantStatus    int
		inner         func(http.Handler) http.Handler
	}{
		{
			name:       "unauthorized",
			wantStatus: http.StatusUnauthorized,
			inner:      Authorization,
		},
		{
			name:          "rate limited",
			authorization: "Bearer sk-client-a",
			wantStatus:    http.StatusTooManyRequests,
			inner: func(next http.Handler) http.Handler {
				return Authorization(rateLimitWithFactory(func() ratelimit.Limiter {
					return &fixedBudgetLimiter{}
				})(next))
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logPath := t.TempDir() + "/gateway.log"
			if err := log.Init("info", logPath); err != nil {
				t.Fatalf("initialize logger: %v", err)
			}

			downstreamCalled := false
			downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				downstreamCalled = true
				w.WriteHeader(http.StatusNoContent)
			})
			handler := SetGatewayRequestID(LogRequest(tc.inner(downstream)))
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
			}
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, req)
			if downstreamCalled {
				t.Fatal("expected admission rejection before downstream")
			}
			result := recorder.Result()
			defer result.Body.Close()
			if result.StatusCode != tc.wantStatus {
				t.Fatalf("expected status %d, got %d", tc.wantStatus, result.StatusCode)
			}
			requestID := result.Header.Get(protocol.HeaderGatewayRequestID)
			if requestID == "" {
				t.Fatal("expected a Gateway request ID on the response")
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
			if got := entry["status"]; got != float64(tc.wantStatus) {
				t.Fatalf("expected logged status %d, got %#v", tc.wantStatus, got)
			}
			if got := entry["gateway_request_id"]; got != requestID {
				t.Fatalf("expected logged request ID %q, got %#v", requestID, got)
			}
		})
	}
}
