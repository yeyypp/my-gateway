package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"shuai/gateway/internal/handlers"
	"shuai/gateway/internal/middleware"
)

func newTestProxy(t *testing.T, upstreamURL string) http.Handler {
	t.Helper()

	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)

	proxy, err := BuildReverseProxy(
		RoutePolicy{UpstreamURL: upstreamURL},
		transport,
	)
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}
	return proxy
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

	proxy := newTestProxy(t, upstream.URL)

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

	var respJSON handlers.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&respJSON); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if respJSON.Error.Message != "upstream unavailable" {
		t.Errorf("expected response message %q, got %q", "upstream unavailable", respJSON.Error.Message)
	}
	if respJSON.Error.Type != "gateway_error" {
		t.Errorf("expected error type %q, got %q", "gateway_error", respJSON.Error.Type)
	}
	if respJSON.Error.Code != "upstream_unavailable" {
		t.Errorf("expected error code %q, got %q", "upstream_unavailable", respJSON.Error.Code)
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
			connection:         r.Header.Get("Connection"),
			keepAlive:          r.Header.Get("Keep-Alive"),
			proxyAuthorization: r.Header.Get("Proxy-Authorization"),
			debugHop:           r.Header.Get("X-Debug-Hop"),
		}

		w.Header().Set("Connection", "X-Response-Hop")
		w.Header().Set("X-Response-Hop", "should-be-removed")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream-Request-ID", "upstream-123")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	targetURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("failed to parse upstream URL: %v", err)
	}

	proxy := newTestProxy(t, upstream.URL)

	req := httptest.NewRequest(http.MethodPost, "http://gateway.example/process", strings.NewReader(`{"mode":"ok"}`))
	req.Header.Set("Connection", "X-Debug-Hop")
	req.Header.Set("X-Debug-Hop", "should-be-removed")
	req.Header.Set("Proxy-Authorization", "Basic secret")
	req.Header.Set("Keep-Alive", "timeout=5, max=100")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-secret")
	req.Header.Set("User-Agent", "gateway-header-test")
	req.Header.Set("X-End-To-End", "end-to-end-value")

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
	if val := resp.Header.Get("X-Response-Hop"); val != "" {
		t.Errorf("hop-by-hop response header X-Response-Hop should not be forwarded, got %q", val)
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

	timeoutHandler := middleware.ValidateJSONRequest(middleware.RequestTimeout(300 * time.Millisecond)(proxy))

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/process", strings.NewReader(`{"test":"test"}`))

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

	var jsonResp handlers.ErrorResponse
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
	if jsonResp.Error.Type != "gateway_error" {
		t.Fatalf("expected error type %q, got %q", "gateway_error", jsonResp.Error.Type)
	}
	if jsonResp.Error.Code != "upstream_timeout" {
		t.Fatalf("expected error code %q, got %q", "upstream_timeout", jsonResp.Error.Code)
	}
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status code should be 504")
	}

}

func TestClientCancel(t *testing.T) {
	errChan := make(chan error, 1)
	upstreamWorking := make(chan struct{})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	var jsonResp handlers.ErrorResponse
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
	if jsonResp.Error.Type != "invalid_request_error" {
		t.Fatalf("expected error type %q, got %q", "invalid_request_error", jsonResp.Error.Type)
	}
	if jsonResp.Error.Code != "invalid_json" {
		t.Fatalf("expected error code %q, got %q", "invalid_json", jsonResp.Error.Code)
	}
	if got := result.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected Content-Type %q, got %q", "application/json", got)
	}
}
