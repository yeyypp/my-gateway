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
)

func newTestProxy(t *testing.T, upstreamURL string) http.Handler {
	t.Helper()

	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)

	proxy, err := BuildReverseProxy(
		RoutePolicy{UpstreamURL: upstreamURL, UpstreamModel: "real-model"},
		transport,
	)
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Context().Value(middleware.ChatReqKey).(protocol.ChatCompletionRequest); !ok {
			chatReq := protocol.ChatCompletionRequest{
				Model: "logic-model",
				Messages: []protocol.Message{
					{Role: "user", Content: "test"},
				},
			}
			ctx := context.WithValue(r.Context(), middleware.ChatReqKey, chatReq)
			r = r.WithContext(ctx)
		}
		proxy.ServeHTTP(w, r)
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

			resp, err := http.Post(gateway.URL, "application/json", nil)
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

func TestUpstreamUnavailable(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	proxy := middleware.GetGatewayRequestID(newTestProxy(t, upstream.URL))

	upstream.Close() // Close the upstream server to simulate unavailability

	gateway := httptest.NewServer(proxy)
	defer gateway.Close()

	resp, err := http.Post(gateway.URL, "application/json", nil)
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
	if gotRequest.authorization != "Bearer test-secret" {
		t.Errorf("expected upstream Authorization %q, got %q", "Bearer test-secret", gotRequest.authorization)
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

	req := httptest.NewRequestWithContext(ctx, "POST", "http://test", nil)
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
