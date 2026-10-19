package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shuai/gateway/internal/protocol"
	"shuai/gateway/internal/ratelimit"
)

type denyAllRequests struct{}

func (denyAllRequests) Allow(int) bool { return false }

func TestAdmissionErrorEnvelopes(t *testing.T) {
	t.Setenv("LOCAL_CLIENT_KEY_A", "valid-key")
	for _, tc := range []struct {
		name                          string
		middleware                    Middleware
		clientID, authorization, body string
		status                        int
		kind                          protocol.ErrorType
		code                          protocol.ErrorCode
	}{
		{name: "missing credential", middleware: Authorization, status: 401, kind: protocol.ErrorTypeAuthentication, code: protocol.ErrorCodeInvalidAPIKey},
		{name: "invalid credential", middleware: Authorization, authorization: "Bearer invalid", status: 401, kind: protocol.ErrorTypeAuthentication, code: protocol.ErrorCodeInvalidAPIKey},
		{name: "missing identity", middleware: RateLimit, status: 403, kind: protocol.ErrorTypePermission, code: protocol.ErrorCodePermissionDenied},
		{name: "request rate", middleware: rateLimitWithFactory(func() ratelimit.Limiter { return denyAllRequests{} }), clientID: "client", status: 429, kind: protocol.ErrorTypeRateLimit, code: protocol.ErrorCodeRateLimit},
		{name: "concurrency", middleware: ConcurrencyLimitWithLimits(0, 1), clientID: "client", status: 429, kind: protocol.ErrorTypeRateLimit, code: protocol.ErrorCodeConcurrencyLimit},
		{name: "oversized request", middleware: ValidateJSONRequest, body: strings.Repeat("x", maxRequestBodyBytes+1), status: 413, kind: protocol.ErrorTypeInvalidRequest, code: protocol.ErrorCodeRequestTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := SetGatewayRequestID(tc.middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("rejected request reached downstream") })))
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(tc.body))
			req.Header.Set("Authorization", tc.authorization)
			if tc.clientID != "" {
				req = req.WithContext(context.WithValue(req.Context(), ClientID, tc.clientID))
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			var response protocol.ErrorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status || response.Error.Type != tc.kind || response.Error.Code != tc.code || response.Error.Message == "" {
				t.Fatalf("unexpected rejection: %d %s", w.Code, w.Body)
			}
			if !bytes.Contains(w.Body.Bytes(), []byte(`"param":null`)) || w.Header().Get(protocol.HeaderGatewayRequestID) == "" {
				t.Fatalf("missing null param or request ID: %s", w.Body)
			}
		})
	}
}
