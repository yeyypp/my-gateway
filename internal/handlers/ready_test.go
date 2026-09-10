package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"shuai/gateway/internal/config"
)

func TestReadyCheckUsesConfiguredHealthURLs(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/custom-health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(healthy.Close)

	unhealthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(unhealthy.Close)

	tests := []struct {
		name      string
		upstreams []config.Upstream
		wantCode  int
	}{
		{
			name: "no configured probes is ready",
			upstreams: []config.Upstream{
				{Name: "external", BaseURL: "https://example.com"},
			},
			wantCode: http.StatusOK,
		},
		{
			name: "one healthy upstream is ready",
			upstreams: []config.Upstream{
				{Name: "healthy", HealthURL: healthy.URL + "/custom-health"},
			},
			wantCode: http.StatusOK,
		},
		{
			name: "all probed upstreams unavailable is not ready",
			upstreams: []config.Upstream{
				{Name: "unhealthy", HealthURL: unhealthy.URL + "/readyz"},
				{Name: "ignored", BaseURL: "https://example.com"},
			},
			wantCode: http.StatusServiceUnavailable,
		},
		{
			name: "at least one healthy upstream is ready",
			upstreams: []config.Upstream{
				{Name: "healthy", HealthURL: healthy.URL + "/custom-health"},
				{Name: "unhealthy", HealthURL: unhealthy.URL + "/readyz"},
			},
			wantCode: http.StatusOK,
		},
		{
			name: "upstream with an unset API key environment is not ready",
			upstreams: []config.Upstream{
				{Name: "deepseek", BaseURL: "https://api.deepseek.com", APIKeyEnv: "GATEWAY_TEST_MISSING_KEY"},
			},
			wantCode: http.StatusServiceUnavailable,
		},
		{
			name: "upstream with resolved credentials and no probe is ready",
			upstreams: []config.Upstream{
				{
					Name:      "deepseek",
					BaseURL:   "https://api.deepseek.com",
					APIKeyEnv: "GATEWAY_TEST_API_KEY",
					APIKey:    "sk-test-secret",
				},
			},
			wantCode: http.StatusOK,
		},
		{
			name: "healthy probed upstream outvotes a missing API key",
			upstreams: []config.Upstream{
				{Name: "deepseek", BaseURL: "https://api.deepseek.com", APIKeyEnv: "GATEWAY_TEST_MISSING_KEY"},
				{Name: "healthy", HealthURL: healthy.URL + "/custom-health"},
			},
			wantCode: http.StatusOK,
		},
		{
			name: "missing API key does not rescue an unhealthy probe",
			upstreams: []config.Upstream{
				{Name: "deepseek", BaseURL: "https://api.deepseek.com", APIKeyEnv: "GATEWAY_TEST_MISSING_KEY"},
				{Name: "unhealthy", HealthURL: unhealthy.URL + "/readyz"},
			},
			wantCode: http.StatusServiceUnavailable,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			readyCheck := NewReadyCheck(tc.upstreams)
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/readyz", nil)

			readyCheck.CheckUpstreams(recorder, req)

			result := recorder.Result()
			defer result.Body.Close()
			if result.StatusCode != tc.wantCode {
				t.Fatalf("expected status code %d, got %d", tc.wantCode, result.StatusCode)
			}
		})
	}
}

func TestReadyCheckRecoversWhenUpstreamRecovers(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusServiceUnavailable)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
	}))
	t.Cleanup(upstream.Close)

	readyCheck := NewReadyCheck([]config.Upstream{{
		Name:      "recovering",
		HealthURL: upstream.URL + "/readyz",
	}})
	probe := func() int {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		readyCheck.CheckUpstreams(recorder, req)
		result := recorder.Result()
		defer result.Body.Close()
		return result.StatusCode
	}

	if got := probe(); got != http.StatusServiceUnavailable {
		t.Fatalf("expected unavailable status %d, got %d", http.StatusServiceUnavailable, got)
	}
	status.Store(http.StatusOK)
	if got := probe(); got != http.StatusOK {
		t.Fatalf("expected recovered status %d, got %d", http.StatusOK, got)
	}
}

func TestReadyCheckHonorsCanceledContext(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	readyCheck := NewReadyCheck(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := readyCheck.checkIfUpstreamReady(ctx, config.Upstream{
		Name:      "canceled",
		HealthURL: upstream.URL + "/readyz",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestReadyCheckReportsMissingAPIKeyWithoutProbing(t *testing.T) {
	var probes atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	readyCheck := NewReadyCheck(nil)
	err := readyCheck.checkIfUpstreamReady(context.Background(), config.Upstream{
		Name:      "deepseek",
		BaseURL:   "https://api.deepseek.com",
		HealthURL: upstream.URL + "/readyz",
		APIKeyEnv: "GATEWAY_TEST_MISSING_KEY",
	})
	if err == nil {
		t.Fatal("expected an error for an unset API key environment variable")
	}
	if !strings.Contains(err.Error(), "GATEWAY_TEST_MISSING_KEY") {
		t.Fatalf("expected the error to name the missing env var, got %v", err)
	}
	if got := probes.Load(); got != 0 {
		t.Fatalf("a missing API key must not probe the upstream, got %d probes", got)
	}
}
