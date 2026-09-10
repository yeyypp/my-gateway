package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"shuai/gateway/internal/log"
	"shuai/gateway/internal/middleware"
	"shuai/gateway/internal/protocol"

	"go.uber.org/zap"
)

// testChatBody is a valid Chat Completions request body that also carries
// fields the gateway's minimal request struct does not model.
const testChatBody = `{"model":"logic-model","messages":[{"role":"user","content":"test"}],"temperature":0.7,"tools":[{"type":"function"}],"response_format":{"type":"json_object"}}`

func newTestProxy(t *testing.T, upstreamURL string) http.Handler {
	return newTestProxyWithAPIKey(t, upstreamURL, "")
}

func newTestProxyWithAPIKey(t *testing.T, upstreamURL, apiKey string) http.Handler {
	t.Helper()

	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)

	proxy, err := BuildReverseProxy(
		RoutePolicy{UpstreamURL: upstreamURL, UpstreamModel: "real-model"},
		transport,
		apiKey,
	)
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}
	// Reproduce the request state that ValidateJSONRequest leaves behind for the
	// stages that route and rewrite the request.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if _, ok := ctx.Value(middleware.ChatReqKey).(protocol.ChatCompletionRequest); !ok {
			chatReq := protocol.ChatCompletionRequest{
				Model: "logic-model",
				Messages: []protocol.Message{
					{Role: "user", Content: "test"},
				},
			}
			ctx = context.WithValue(ctx, middleware.ChatReqKey, chatReq)
		}
		if _, ok := middleware.GetRequestBodyFromCTX(ctx); !ok {
			ctx = context.WithValue(ctx, middleware.RequestBodyKey, []byte(testChatBody))
		}
		proxy.ServeHTTP(w, r.WithContext(ctx))
	})
}

func TestPropagateStatusCode(t *testing.T) {
	testCases := []struct {
		name       string
		expectCode int
	}{
		{
			name:       "200 OK",
			expectCode: http.StatusOK,
		},
		{
			name:       "400 Bad Request",
			expectCode: http.StatusBadRequest,
		},
		{
			name:       "500 Internal Server Error",
			expectCode: http.StatusInternalServerError,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.expectCode)
			}))
			defer upstream.Close()

			proxy := newTestProxy(t, upstream.URL)

			gateway := httptest.NewServer(proxy)
			defer gateway.Close()

			resp, err := http.Post(gateway.URL, "application/json", strings.NewReader(testChatBody))
			if err != nil {
				t.Fatalf("failed to send request to gateway: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.expectCode {
				t.Errorf("expected status code %d, got %d", tc.expectCode, resp.StatusCode)
			}
		})
	}
}

func TestProxyPreservesUnmodeledRequestFields(t *testing.T) {
	received := make(chan map[string]json.RawMessage, 1)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			received <- nil
			return
		}
		received <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	gateway := httptest.NewServer(newTestProxy(t, upstream.URL))
	defer gateway.Close()

	resp, err := http.Post(gateway.URL, "application/json", strings.NewReader(testChatBody))
	if err != nil {
		t.Fatalf("failed to send request to gateway: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status code %d, got %d", http.StatusOK, resp.StatusCode)
	}

	body := <-received
	if body == nil {
		t.Fatal("upstream did not receive a decodable JSON body")
	}

	// The gateway owns the model field only, so every other field must survive
	// the rewrite verbatim.
	want := map[string]string{
		"model":           `"real-model"`,
		"messages":        `[{"role":"user","content":"test"}]`,
		"temperature":     `0.7`,
		"tools":           `[{"type":"function"}]`,
		"response_format": `{"type":"json_object"}`,
	}
	for field, wantRaw := range want {
		got, ok := body[field]
		if !ok {
			t.Errorf("upstream request lost field %q", field)
			continue
		}
		if string(got) != wantRaw {
			t.Errorf("field %q: expected %s, got %s", field, wantRaw, got)
		}
	}
	if len(body) != len(want) {
		t.Errorf("expected %d fields upstream, got %d: %v", len(want), len(body), body)
	}
}

func TestChangeModelRewritesOnlyTheTopLevelModel(t *testing.T) {
	testCases := []struct {
		name      string
		body      string
		realModel string
		want      string
		wantErr   bool
	}{
		{
			name:      "unmodeled fields survive",
			body:      `{"model":"logic-model","messages":[{"role":"user","content":"test"}],"temperature":0.7,"tools":[{"type":"function"}]}`,
			realModel: "real-model",
			want:      `{"messages":[{"role":"user","content":"test"}],"model":"real-model","temperature":0.7,"tools":[{"type":"function"}]}`,
		},
		{
			name:      "nested model fields stay untouched",
			body:      `{"model":"logic-model","messages":[{"role":"user","content":"test"}],"metadata":{"model":"keep-me"}}`,
			realModel: "real-model",
			want:      `{"messages":[{"role":"user","content":"test"}],"metadata":{"model":"keep-me"},"model":"real-model"}`,
		},
		{
			name:      "invalid json is rejected",
			body:      `{"model":`,
			realModel: "real-model",
			wantErr:   true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := changeModel([]byte(tc.body), tc.realModel)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got body %s", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("changeModel: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("expected body %s, got %s", tc.want, got)
			}
		})
	}
}

func TestProxyRejectsRequestWithoutValidatedBody(t *testing.T) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)

	proxy, err := BuildReverseProxy(
		RoutePolicy{UpstreamURL: "http://127.0.0.1:1", UpstreamModel: "real-model"},
		transport,
		"",
	)
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	// A request that never passed through ValidateJSONRequest has no validated
	// body, so the Gateway must fail loudly instead of guessing at r.Body.
	ctx := context.WithValue(context.Background(), middleware.ChatReqKey, protocol.ChatCompletionRequest{
		Model: "logic-model",
	})
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/chat/completions", strings.NewReader(testChatBody))
	recorder := httptest.NewRecorder()

	proxy.ServeHTTP(recorder, req)

	result := recorder.Result()
	defer result.Body.Close()
	if result.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected status code %d, got %d", http.StatusInternalServerError, result.StatusCode)
	}

	var respJSON protocol.ErrorResponse
	if err := json.NewDecoder(result.Body).Decode(&respJSON); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if respJSON.Error.Type != protocol.ErrorTypeGateway {
		t.Fatalf("expected error type %q, got %q", protocol.ErrorTypeGateway, respJSON.Error.Type)
	}
	if respJSON.Error.Code != protocol.ErrorCodeRequestEncodingFailed {
		t.Fatalf("expected error code %q, got %q", protocol.ErrorCodeRequestEncodingFailed, respJSON.Error.Code)
	}
}

func TestUpstreamAuthorizationInjection(t *testing.T) {
	testCases := []struct {
		name              string
		apiKey            string
		clientAuth        string
		wantAuthorization string
	}{
		{
			name:              "configured API key replaces the client credential",
			apiKey:            "sk-gateway-upstream",
			clientAuth:        "Bearer sk-client",
			wantAuthorization: "Bearer sk-gateway-upstream",
		},
		{
			name:              "client credential never reaches an upstream without its own key",
			clientAuth:        "Bearer sk-client",
			wantAuthorization: "",
		},
		{
			name:              "no credentials at all",
			wantAuthorization: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			received := make(chan string, 1)

			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received <- r.Header.Get("Authorization")
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()

			proxy := newTestProxyWithAPIKey(t, upstream.URL, tc.apiKey)
			gateway := httptest.NewServer(proxy)
			defer gateway.Close()

			req, err := http.NewRequest(http.MethodPost, gateway.URL, strings.NewReader(testChatBody))
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			if tc.clientAuth != "" {
				req.Header.Set("Authorization", tc.clientAuth)
			}

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("failed to send request to gateway: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected status code %d, got %d", http.StatusOK, resp.StatusCode)
			}

			if got := <-received; got != tc.wantAuthorization {
				t.Fatalf("expected upstream Authorization %q, got %q", tc.wantAuthorization, got)
			}
		})
	}
}

func TestUpstreamUnavailable(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	proxy := middleware.GetGatewayRequestID(newTestProxy(t, upstream.URL))

	upstream.Close() // Close the upstream server to simulate unavailability

	gateway := httptest.NewServer(proxy)
	defer gateway.Close()

	resp, err := http.Post(gateway.URL, "application/json", strings.NewReader(testChatBody))
	if err != nil {
		t.Fatalf("failed to send request to gateway: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected status code %d, got %d", http.StatusBadGateway, resp.StatusCode)
	}
	requestIDs := resp.Header.Values("X-Gateway-Request-ID")
	if len(requestIDs) != 1 || requestIDs[0] == "" {
		t.Fatalf("expected exactly one non-empty Gateway request ID, got %q", requestIDs)
	}

	var respJSON protocol.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&respJSON); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if respJSON.Error.Message != "upstream unavailable" {
		t.Errorf("expected response message %q, got %q", "upstream unavailable", respJSON.Error.Message)
	}
	if respJSON.Error.Type != protocol.ErrorTypeGateway {
		t.Errorf("expected error type %q, got %q", protocol.ErrorTypeGateway, respJSON.Error.Type)
	}
	if respJSON.Error.Code != protocol.ErrorCodeUpstreamUnavailable {
		t.Errorf("expected error code %q, got %q", protocol.ErrorCodeUpstreamUnavailable, respJSON.Error.Code)
	}

}

// implemented by codex, too tired
func TestProxyRequestAndResponseHeaders(t *testing.T) {
	type capturedRequest struct {
		contentType        string
		authorization      string
		userAgent          string
		host               string
		endToEnd           string
		gatewayRequestID   string
		clientRequestID    string
		connection         string
		keepAlive          string
		proxyAuthorization string
		debugHop           string
	}

	receivedRequest := make(chan capturedRequest, 1)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedRequest <- capturedRequest{
			contentType:        r.Header.Get("Content-Type"),
			authorization:      r.Header.Get("Authorization"),
			userAgent:          r.Header.Get("User-Agent"),
			host:               r.Host,
			endToEnd:           r.Header.Get("X-End-To-End"),
			gatewayRequestID:   r.Header.Get("X-Gateway-Request-ID"),
			clientRequestID:    r.Header.Get(protocol.HeaderClientRequestID),
			connection:         r.Header.Get("Connection"),
			keepAlive:          r.Header.Get("Keep-Alive"),
			proxyAuthorization: r.Header.Get("Proxy-Authorization"),
			debugHop:           r.Header.Get("X-Debug-Hop"),
		}

		w.Header().Set("Connection", "X-Response-Hop")
		w.Header().Set("X-Response-Hop", "should-be-removed")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Gateway-Request-ID", "fake-upstream-gateway-id")
		w.Header().Set("X-Request-ID", "provider-123")
		w.Header().Set("X-Upstream-Request-ID", "upstream-123")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	targetURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("failed to parse upstream URL: %v", err)
	}

	proxy := middleware.GetGatewayRequestID(newTestProxy(t, upstream.URL))
	logPath := t.TempDir() + "/gateway.log"
	if err := log.Init("info", logPath); err != nil {
		t.Fatalf("initialize logger: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "http://gateway.example/process", strings.NewReader(`{"mode":"ok"}`))
	req.Header.Set("Connection", "X-Debug-Hop")
	req.Header.Set("X-Debug-Hop", "should-be-removed")
	req.Header.Set("Proxy-Authorization", "Basic secret")
	req.Header.Set("Keep-Alive", "timeout=5, max=100")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-secret")
	req.Header.Set("User-Agent", "gateway-header-test")
	req.Header.Set("X-End-To-End", "end-to-end-value")
	req.Header.Set(protocol.HeaderClientRequestID, "client-123")

	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, req)
	resp := recorder.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status code %d, got %d", http.StatusOK, resp.StatusCode)
	}

	gotRequest := <-receivedRequest
	if gotRequest.contentType != "application/json" {
		t.Errorf("expected upstream Content-Type %q, got %q", "application/json", gotRequest.contentType)
	}
	if gotRequest.authorization != "" {
		t.Errorf("client Authorization must not be forwarded upstream, got %q", gotRequest.authorization)
	}
	if gotRequest.userAgent != "gateway-header-test" {
		t.Errorf("expected upstream User-Agent %q, got %q", "gateway-header-test", gotRequest.userAgent)
	}
	if gotRequest.host != targetURL.Host {
		t.Errorf("expected upstream Host %q, got %q", targetURL.Host, gotRequest.host)
	}
	if gotRequest.endToEnd != "end-to-end-value" {
		t.Errorf("expected upstream X-End-To-End %q, got %q", "end-to-end-value", gotRequest.endToEnd)
	}
	if gotRequest.clientRequestID != "" {
		t.Fatalf("client request ID must not be forwarded upstream, got %q", gotRequest.clientRequestID)
	}

	removedRequestHeaders := map[string]string{
		"Connection":          gotRequest.connection,
		"Keep-Alive":          gotRequest.keepAlive,
		"Proxy-Authorization": gotRequest.proxyAuthorization,
		"X-Debug-Hop":         gotRequest.debugHop,
	}
	for name, value := range removedRequestHeaders {
		if value != "" {
			t.Errorf("hop-by-hop request header %s should not be forwarded, got %q", name, value)
		}
	}

	if val := resp.Header.Get("Content-Type"); val != "application/json" {
		t.Errorf("expected response Content-Type %q, got %q", "application/json", val)
	}
	if val := resp.Header.Get("X-Upstream-Request-ID"); val != "upstream-123" {
		t.Errorf("expected response X-Upstream-Request-ID %q, got %q", "upstream-123", val)
	}
	if val := resp.Header.Get("X-Request-ID"); val != "provider-123" {
		t.Errorf("expected provider X-Request-ID %q, got %q", "provider-123", val)
	}
	gatewayRequestIDs := resp.Header.Values("X-Gateway-Request-ID")
	if len(gatewayRequestIDs) != 1 || gatewayRequestIDs[0] == "" {
		t.Fatalf("expected exactly one non-empty Gateway request ID, got %q", gatewayRequestIDs)
	}
	if gatewayRequestIDs[0] == "fake-upstream-gateway-id" {
		t.Fatal("upstream response must not control the Gateway request ID")
	}
	if gotRequest.gatewayRequestID != gatewayRequestIDs[0] {
		t.Fatalf(
			"expected upstream request ID %q to match Gateway response ID %q",
			gotRequest.gatewayRequestID,
			gatewayRequestIDs[0],
		)
	}
	if gatewayRequestIDs[0] == resp.Header.Get("X-Request-ID") {
		t.Fatal("expected Gateway and provider request IDs to remain distinct")
	}
	if val := resp.Header.Get("X-Response-Hop"); val != "" {
		t.Errorf("hop-by-hop response header X-Response-Hop should not be forwarded, got %q", val)
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
	if got := entry["msg"]; got != "upstream response received" {
		t.Fatalf("expected upstream response log, got %#v", got)
	}
	if got := entry["gateway_request_id"]; got != gatewayRequestIDs[0] {
		t.Fatalf("expected Gateway request ID %q in log, got %#v", gatewayRequestIDs[0], got)
	}
	if got := entry["client_request_id"]; got != "client-123" {
		t.Fatalf("expected client request ID %q in log, got %#v", "client-123", got)
	}
	if got := entry["upstream_request_id"]; got != "provider-123" {
		t.Fatalf("expected upstream request ID %q in log, got %#v", "provider-123", got)
	}
	if got := entry["status"]; got != float64(http.StatusOK) {
		t.Fatalf("expected status %d in log, got %#v", http.StatusOK, got)
	}
}

func TestRequestTimeout(t *testing.T) {
	upstreamDelay := 500 * time.Millisecond

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(upstreamDelay)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	proxy := newTestProxy(t, upstream.URL)

	timeoutHandler := middleware.GetGatewayRequestID(
		middleware.ValidateJSONRequest(middleware.RequestTimeout(300 * time.Millisecond)(proxy)),
	)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/process",
		strings.NewReader(`{"model":"model-a","messages":[{"role":"user","content":"test"}]}`),
	)

	start := time.Now()
	timeoutHandler.ServeHTTP(recorder, req)
	elapsed := time.Since(start)
	if elapsed >= upstreamDelay {
		t.Fatalf(
			"gateway should return before upstream delay, elapsed=%v upstreamDelay=%v",
			elapsed,
			upstreamDelay,
		)
	}

	resp := recorder.Result()
	defer resp.Body.Close()

	var jsonResp protocol.ErrorResponse
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&jsonResp); err != nil {
		t.Fatalf("failed to decode json, err:%v\n", err)
	}

	if jsonResp.Error.Message != "upstream timeout" {
		t.Fatalf(
			"expected response message %q, got %q",
			"upstream timeout",
			jsonResp.Error.Message,
		)
	}
	if jsonResp.Error.Type != protocol.ErrorTypeGateway {
		t.Fatalf("expected error type %q, got %q", protocol.ErrorTypeGateway, jsonResp.Error.Type)
	}
	if jsonResp.Error.Code != protocol.ErrorCodeUpstreamTimeout {
		t.Fatalf("expected error code %q, got %q", protocol.ErrorCodeUpstreamTimeout, jsonResp.Error.Code)
	}
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status code should be 504")
	}
	requestIDs := resp.Header.Values("X-Gateway-Request-ID")
	if len(requestIDs) != 1 || requestIDs[0] == "" {
		t.Fatalf("expected exactly one non-empty Gateway request ID, got %q", requestIDs)
	}

}

func TestClientCancel(t *testing.T) {
	errChan := make(chan error, 1)
	upstreamWorking := make(chan struct{})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			errChan <- err
			return
		}
		close(upstreamWorking)
		select {
		case <-time.After(2 * time.Second):
			w.WriteHeader(http.StatusOK)
			errChan <- errors.New("upstream was not canceled")
			return
		case <-r.Context().Done():
			errChan <- r.Context().Err()
			return
		}
	}))
	defer upstream.Close()

	proxy := newTestProxy(t, upstream.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = context.WithValue(ctx, middleware.ChatReqKey, protocol.ChatCompletionRequest{
		Model: "logic-model",
		Messages: []protocol.Message{
			{Role: "user", Content: "test"},
		},
	})

	req := httptest.NewRequestWithContext(ctx, "POST", "http://test", strings.NewReader(testChatBody))
	recorder := httptest.NewRecorder()
	handler := middleware.RequestTimeout(5 * time.Second)(proxy)

	var wg sync.WaitGroup
	wg.Go(func() {
		handler.ServeHTTP(recorder, req)
	})

	select {
	case <-upstreamWorking:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("req did not reach upstream")
	}

	select {
	case err := <-errChan:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err should be canceled, get:%v\n", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not receive cancellation")
	}
	wg.Wait()
}

func TestInvalidJSON(t *testing.T) {
	errChan := make(chan error, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		errChan <- errors.New("req should not reach upstream")
	}))
	defer upstream.Close()

	proxy := newTestProxy(t, upstream.URL)

	handler := middleware.ValidateJSONRequest(proxy)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "http://test", strings.NewReader("{'mode"))
	handler.ServeHTTP(recorder, req)
	result := recorder.Result()
	defer result.Body.Close()

	select {
	case err := <-errChan:
		t.Fatal(err)
	default:
	}

	var jsonResp protocol.ErrorResponse
	decoder := json.NewDecoder(result.Body)
	if err := decoder.Decode(&jsonResp); err != nil {
		t.Fatalf("failed to decode json, err:%v\n", err)
	}

	if result.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status code %d, got %d", http.StatusBadRequest, result.StatusCode)
	}
	if jsonResp.Error.Message != "invalid json" {
		t.Fatalf("expected response message %q, got %q", "invalid json", jsonResp.Error.Message)
	}
	if jsonResp.Error.Type != protocol.ErrorTypeInvalidRequest {
		t.Fatalf("expected error type %q, got %q", protocol.ErrorTypeInvalidRequest, jsonResp.Error.Type)
	}
	if jsonResp.Error.Code != protocol.ErrorCodeInvalidJSON {
		t.Fatalf("expected error code %q, got %q", protocol.ErrorCodeInvalidJSON, jsonResp.Error.Code)
	}
	if got := result.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected Content-Type %q, got %q", "application/json", got)
	}
}

func TestSSETrackBodyClassifiesReadErrors(t *testing.T) {
	testCases := []struct {
		name    string
		readErr error
		wantLog bool
	}{
		{name: "normal EOF", readErr: io.EOF, wantLog: false},
		{name: "client cancellation", readErr: context.Canceled, wantLog: false},
		{name: "upstream interruption", readErr: errors.New("upstream connection reset"), wantLog: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			logPath := t.TempDir() + "/gateway.log"
			if err := log.Init("info", logPath); err != nil {
				t.Fatalf("initialize logger: %v", err)
			}

			src := &fixedErrorReadCloser{err: tc.readErr}
			initialFields := []zap.Field{zap.String("gateway_request_id", "gateway-123")}
			body := newSSETrackBody(src, initialFields)

			if _, err := body.Read(make([]byte, 1)); !errors.Is(err, tc.readErr) {
				t.Fatalf("read: expected error %v, got %v", tc.readErr, err)
			}
			if len(body.fields) != len(initialFields) {
				t.Fatalf("read must not mutate stored fields: expected length %d, got %d", len(initialFields), len(body.fields))
			}
			if err := body.Close(); err != nil {
				t.Fatalf("close wrapped body: %v", err)
			}
			if !src.closed {
				t.Fatal("close must be forwarded to source body")
			}

			if err := log.Sync(); err != nil {
				t.Fatalf("sync logger: %v", err)
			}
			content, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read log: %v", err)
			}

			if !tc.wantLog {
				if len(strings.TrimSpace(string(content))) != 0 {
					t.Fatalf("expected no interruption log, got %s", content)
				}
				return
			}

			var entry map[string]any
			if err := json.Unmarshal(content, &entry); err != nil {
				t.Fatalf("decode interruption log: %v", err)
			}
			if got := entry["msg"]; got != "read from upstream interrupted" {
				t.Fatalf("expected interruption message, got %#v", got)
			}
			if got := entry["error"]; got != tc.readErr.Error() {
				t.Fatalf("expected error %q, got %#v", tc.readErr, got)
			}
			if got := entry["gateway_request_id"]; got != "gateway-123" {
				t.Fatalf("expected Gateway request ID in log, got %#v", got)
			}
		})
	}
}

type fixedErrorReadCloser struct {
	err    error
	closed bool
}

func (r *fixedErrorReadCloser) Read([]byte) (int, error) {
	return 0, r.err
}

func (r *fixedErrorReadCloser) Close() error {
	r.closed = true
	return nil
}
