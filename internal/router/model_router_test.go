package router

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"shuai/gateway/internal/handlers"
	"shuai/gateway/internal/middleware"
)

func TestModelRouterRoutesKnownModels(t *testing.T) {
	routes := map[string]http.Handler{
		"model-a": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Selected-Model", "model-a")
			w.WriteHeader(http.StatusNoContent)
		}),
		"model-b": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Selected-Model", "model-b")
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	modelRouter := NewModelRouter(routes)

	for _, model := range []string{"model-a", "model-b"} {
		t.Run(model, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), middleware.ModelContextKey, model)
			req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/chat/completions", nil)
			recorder := httptest.NewRecorder()

			modelRouter.ServeHTTP(recorder, req)

			result := recorder.Result()
			defer result.Body.Close()
			if result.StatusCode != http.StatusNoContent {
				t.Fatalf("expected status code %d, got %d", http.StatusNoContent, result.StatusCode)
			}
			if got := result.Header.Get("X-Selected-Model"); got != model {
				t.Fatalf("expected route %q, got %q", model, got)
			}
		})
	}
}

func TestModelRouterRejectsInvalidModels(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		withModel bool
		wantError handlers.APIError
	}{
		{
			name: "missing model context",
			wantError: handlers.APIError{
				Message: "model is required",
				Type:    "invalid_request_error",
				Param:   "model",
				Code:    "missing_required_parameter",
			},
		},
		{
			name:      "empty model",
			withModel: true,
			wantError: handlers.APIError{
				Message: "model is required",
				Type:    "invalid_request_error",
				Param:   "model",
				Code:    "missing_required_parameter",
			},
		},
		{
			name:      "unknown model",
			model:     "model-unknown",
			withModel: true,
			wantError: handlers.APIError{
				Message: "model is not supported",
				Type:    "invalid_request_error",
				Param:   "model",
				Code:    "model_not_found",
			},
		},
	}

	modelRouter := NewModelRouter(map[string]http.Handler{})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.withModel {
				ctx = context.WithValue(ctx, middleware.ModelContextKey, tc.model)
			}
			req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/chat/completions", nil)
			recorder := httptest.NewRecorder()

			modelRouter.ServeHTTP(recorder, req)

			result := recorder.Result()
			defer result.Body.Close()
			if result.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected status code %d, got %d", http.StatusBadRequest, result.StatusCode)
			}

			var got handlers.ErrorResponse
			decoder := json.NewDecoder(result.Body)
			if err := decoder.Decode(&got); err != nil {
				t.Fatalf("failed to decode error response: %v", err)
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
