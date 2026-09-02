package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shuai/gateway/internal/config"
	"shuai/gateway/internal/middleware"
	"shuai/gateway/internal/protocol"
)

func TestConfigToRouter(t *testing.T) {
	newUpstream := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var chatReq protocol.ChatCompletionRequest
			if err := json.NewDecoder(r.Body).Decode(&chatReq); err != nil {
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}
			w.Header().Set("X-Upstream", name)
			w.Header().Set("X-Upstream-Model", chatReq.Model)
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
		realModel        string
	}{
		{
			name:             "model-a routes to mock-a",
			selectedUpstream: "mock-a",
			wantUpstream:     "mock-a",
			realModel:        "real-model-a",
		},
		{
			name:             "model-a routes to mock-b",
			selectedUpstream: "mock-b",
			wantUpstream:     "mock-b",
			realModel:        "real-model-b",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{
				Models: []config.Model{
					{LogicModel: "model-a", Upstream: tc.selectedUpstream, RealModel: tc.realModel},
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

			chatReq := protocol.ChatCompletionRequest{
				Model: "model-a",
				Messages: []protocol.Message{
					{Role: "user", Content: "test"},
				},
			}
			ctx := context.WithValue(context.Background(), middleware.ChatReqKey, chatReq)
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
			if got := result.Header.Get("X-Upstream-Model"); got != tc.realModel {
				t.Fatalf("expected upstream model %q, got %q", tc.realModel, got)
			}
		})
	}
}

func TestHealthCheck(t *testing.T) {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	HealthCheck(recorder, req)

	result := recorder.Result()
	defer result.Body.Close()
	if result.StatusCode != http.StatusOK {
		t.Fatalf("expected status code %d, got %d", http.StatusOK, result.StatusCode)
	}
}

func TestGatewayStreamsFirstChunkBeforeSecondChunk(t *testing.T) {
	sendSecond := make(chan struct{})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming is not supported", http.StatusInternalServerError)
			return
		}

		if _, err := io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"test1\"}}]}\n\n"); err != nil {
			return
		}
		flusher.Flush()

		select {
		case <-sendSecond:
		case <-r.Context().Done():
			return
		}

		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"test2\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()

	cfg := config.Config{
		Models: []config.Model{
			{LogicModel: "logic-model", Upstream: "stream-upstream", RealModel: "real-model"},
		},
		Upstreams: []config.Upstream{
			{Name: "stream-upstream", BaseURL: upstream.URL},
		},
	}
	modelRouter, err := buildRouter(cfg)
	if err != nil {
		t.Fatalf("build router: %v", err)
	}

	chatHandler := middleware.GetGatewayRequestID(
		middleware.LogRequest(
			middleware.ValidateJSONRequest(
				middleware.RequestTimeout(5 * time.Second)(modelRouter),
			),
		),
	)
	mux := http.NewServeMux()
	mux.Handle("POST /v1/chat/completions", chatHandler)
	gateway := httptest.NewServer(mux)
	defer gateway.Close()

	client := gateway.Client()
	client.Timeout = 3 * time.Second
	resp, err := client.Post(
		gateway.URL+"/v1/chat/completions",
		"application/json",
		strings.NewReader(`{"model":"logic-model","messages":[{"role":"user","content":"test"}],"stream":true}`),
	)
	if err != nil {
		t.Fatalf("send request through gateway: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("expected Content-Type %q, got %q", "text/event-stream", got)
	}

	scanner := bufio.NewScanner(resp.Body)
	// 这里可以收到数据表示gateway没有缓存
	if !scanner.Scan() {
		t.Fatalf("read first SSE data line: %v", scanner.Err())
	}
	_, data, found := strings.Cut(scanner.Text(), ":")
	if !found {
		t.Fatalf("expected SSE data line, got %q", scanner.Text())
	}
	// 确认第一个sse事件结束
	if !scanner.Scan() || scanner.Text() != "" {
		t.Fatalf("expected first SSE event separator, got %q", scanner.Text())
	}

	var firstChunk protocol.ChatCompletionStreamResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &firstChunk); err != nil {
		t.Fatalf("decode first stream chunk: %v", err)
	}
	if len(firstChunk.Choices) != 1 || firstChunk.Choices[0].Delta.Content != "test1" {
		t.Fatalf("expected first chunk content %q, got %+v", "test1", firstChunk.Choices)
	}

	close(sendSecond)
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("drain stream response: %v", err)
	}
}
