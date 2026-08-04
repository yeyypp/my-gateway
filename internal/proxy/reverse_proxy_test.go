package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"shuai/gateway/internal/api"
	"testing"
)

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

			proxy, err := BuildReverseProxy(RoutePolicy{UpstreamURL: upstream.URL}, http.DefaultTransport.(*http.Transport).Clone())
			if err != nil {
				t.Fatalf("failed to build reverse proxy: %v", err)
			}

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

	proxy, err := BuildReverseProxy(RoutePolicy{UpstreamURL: upstream.URL}, http.DefaultTransport.(*http.Transport).Clone())
	if err != nil {
		t.Fatalf("failed to build reverse proxy: %v", err)
	}

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

	var respJson api.JsonResponse
	if err := json.NewDecoder(resp.Body).Decode(&respJson); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if respJson.Code != http.StatusBadGateway {
		t.Errorf("expected response code %d, got %d", http.StatusBadGateway, respJson.Code)
	}

	if respJson.Message != "upstream unavailable" {
		t.Errorf("expected response message 'upstream unavailable', got '%s'", respJson.Message)
	}

}
