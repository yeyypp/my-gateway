package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"shuai/gateway/internal/config"
	"shuai/gateway/internal/middleware"
)

func TestConfigToRouter(t *testing.T) {
	newUpstream := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Upstream", name)
			w.WriteHeader(http.StatusNoContent)
		}))
	}

	upstreamA := newUpstream("mock-a")
	upstreamB := newUpstream("mock-b")
	t.Cleanup(upstreamA.Close)
	t.Cleanup(upstreamB.Close)

	tests := []struct {
		name             string
		selectedUpstream string
		wantUpstream     string
	}{
		{
			name:             "model-a routes to mock-a",
			selectedUpstream: "mock-a",
			wantUpstream:     "mock-a",
		},
		{
			name:             "model-a routes to mock-b",
			selectedUpstream: "mock-b",
			wantUpstream:     "mock-b",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{
				Models: []config.Model{
					{Name: "model-a", Upstream: tc.selectedUpstream},
				},
				Upstreams: []config.Upstream{
					{Name: "mock-a", BaseURL: upstreamA.URL},
					{Name: "mock-b", BaseURL: upstreamB.URL},
				},
			}

			modelRouter, err := buildRouter(cfg)
			if err != nil {
				t.Fatalf("build router: %v", err)
			}

			ctx := context.WithValue(context.Background(), middleware.ModelContextKey, "model-a")
			req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/chat/completions", nil)
			recorder := httptest.NewRecorder()

			modelRouter.ServeHTTP(recorder, req)

			result := recorder.Result()
			defer result.Body.Close()
			if result.StatusCode != http.StatusNoContent {
				t.Fatalf("expected status %d, got %d", http.StatusNoContent, result.StatusCode)
			}
			if got := result.Header.Get("X-Upstream"); got != tc.wantUpstream {
				t.Fatalf("expected upstream %q, got %q", tc.wantUpstream, got)
			}
		})
	}
}
