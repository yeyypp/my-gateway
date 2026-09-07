package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func TestGatewaySendFirstChunkBeforeSecondChunk(t *testing.T) {
	sendSecond := make(chan struct{})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "upstream response writer does not support flushing", http.StatusInternalServerError)
			return
		}

		sendContent := func(content string) error {
			resp := protocol.ChatCompletionStreamResponse{
				Choices: []protocol.ChoiceChunk{
					{
						Delta: protocol.Delta{
							Content: content,
						},
					},
				},
			}

			jsonData, err := json.Marshal(resp)
			if err != nil {
				return err
			}
			if _, err := io.WriteString(w, "data: "+string(jsonData)+"\n\n"); err != nil {
				return err
			}
			flusher.Flush()
			return nil
		}

		if err := sendContent("test1"); err != nil {
			t.Errorf("write and flush first upstream chunk: %v", err)
			return
		}
		select {
		case <-sendSecond:
		case <-r.Context().Done():
			return
		}
		if _, err := io.WriteString(w, "data: [DONE]"); err != nil {
			t.Errorf("write upstream stream terminator: %v", err)
			return
		}
		flusher.Flush()
	}))
	defer upstream.Close()

	cfg := config.Config{
		Models: []config.Model{
			{
				LogicModel: "logic-model",
				Upstream:   "test",
				RealModel:  "real-model",
			},
		},
		Upstreams: []config.Upstream{
			{
				Name:    "test",
				BaseURL: upstream.URL,
			},
		},
	}

	modelRouter, err := buildRouter(cfg)
	if err != nil {
		t.Fatalf("build Gateway router: %v", err)
	}
	handler := middleware.GetGatewayRequestID(
		middleware.LogRequest(
			middleware.ValidateJSONRequest(
				middleware.RequestTimeout(5 * time.Second)(modelRouter),
			),
		),
	)

	chatReq := protocol.ChatCompletionRequest{
		Model: "logic-model",
		Messages: []protocol.Message{
			{
				Role:    "user",
				Content: "test",
			},
		},
		Stream: true,
	}
	jsonData, err := json.Marshal(chatReq)
	if err != nil {
		t.Fatalf("encode streaming chat request: %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		server.URL+"/v1/chat/completions",
		bytes.NewReader(jsonData),
	)
	if err != nil {
		t.Fatalf("create streaming Gateway request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("send streaming request through Gateway: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected Gateway status %d, got %d", http.StatusOK, resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("expected Gateway Content-Type %q, got %q", "text/event-stream", got)
	}

	scanner := bufio.NewScanner(resp.Body)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			t.Fatalf("read first SSE data line from Gateway: %v", err)
		}
		t.Fatal("Gateway stream ended before the first SSE data line")
	}
	line := strings.TrimSuffix(scanner.Text(), "\r")
	before, after, found := strings.Cut(line, ":")
	if !found || strings.TrimSpace(before) != "data" {
		t.Fatalf("expected first SSE line to use the data field, got %q", line)
	}

	var firstChunk protocol.ChatCompletionStreamResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(after)), &firstChunk); err != nil {
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

func TestCorrectTimeout(t *testing.T) {
	const writeTimeout = 100 * time.Millisecond
	sendSecond := make(chan struct{})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "upstream response writer does not support flushing", http.StatusInternalServerError)
			return
		}

		sendContent := func(content string) error {
			resp := protocol.ChatCompletionStreamResponse{
				Choices: []protocol.ChoiceChunk{
					{
						Delta: protocol.Delta{
							Content: content,
						},
					},
				},
			}

			jsonData, err := json.Marshal(resp)
			if err != nil {
				return err
			}
			if _, err := io.WriteString(w, "data: "+string(jsonData)+"\n\n"); err != nil {
				return err
			}
			flusher.Flush()
			return nil
		}

		if err := sendContent("test1"); err != nil {
			t.Errorf("write and flush first upstream chunk: %v", err)
			return
		}

		select {
		case <-sendSecond:
		case <-r.Context().Done():
			return
		}

		if err := sendContent("test2"); err != nil {
			t.Errorf("send test2 failed: %v", err)
			return
		}

		if _, err := io.WriteString(w, "data: [DONE]\n\n"); err != nil {
			t.Errorf("write upstream stream terminator: %v", err)
			return
		}
		flusher.Flush()
	}))
	defer upstream.Close()

	cfg := config.Config{
		Models: []config.Model{
			{
				LogicModel: "logic-model",
				Upstream:   "test",
				RealModel:  "real-model",
			},
		},
		Upstreams: []config.Upstream{
			{
				Name:    "test",
				BaseURL: upstream.URL,
			},
		},
	}

	modelRouter, err := buildRouter(cfg)
	if err != nil {
		t.Fatalf("build Gateway router: %v", err)
	}
	handler := middleware.GetGatewayRequestID(
		middleware.LogRequest(
			middleware.ValidateJSONRequest(
				middleware.RequestTimeout(writeTimeout)(modelRouter),
			),
		),
	)

	server := httptest.NewUnstartedServer(handler)
	server.Config.WriteTimeout = writeTimeout
	server.Start()
	defer server.Close()

	chatReq := protocol.ChatCompletionRequest{
		Model: "logic-model",
		Messages: []protocol.Message{
			{
				Role:    "user",
				Content: "test",
			},
		},
		Stream: true,
	}
	jsonData, err := json.Marshal(chatReq)
	if err != nil {
		t.Fatalf("encode streaming chat request: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/chat/completions", bytes.NewReader(jsonData))
	if err != nil {
		t.Fatalf("create streaming Gateway request: %v", err)
	}

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("send streaming request through Gateway: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected Gateway status %d, got %d", http.StatusOK, resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("expected Gateway Content-Type %q, got %q", "text/event-stream", got)
	}

	var contents []string
	doneReceived := false
	secondReleased := false
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if len(line) == 0 {
			continue
		}
		before, after, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(before) != "data" {
			t.Fatalf("expected first SSE line to use the data field, got %q", line)
		}
		data := strings.TrimSpace(after)
		if data == "[DONE]" {
			doneReceived = true
			break
		}
		var chunk protocol.ChatCompletionStreamResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("decode stream chunk: %v", err)
		}
		if len(chunk.Choices) != 1 {
			t.Fatalf("expected one choice in stream chunk, got %+v", chunk.Choices)
		}
		contents = append(contents, chunk.Choices[0].Delta.Content)
		if !secondReleased {
			if contents[0] != "test1" {
				t.Fatalf("expected first stream chunk content %q, got %q", "test1", contents[0])
			}
			time.Sleep(2 * writeTimeout)
			close(sendSecond)
			secondReleased = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read streaming Gateway response: %v", err)
	}
	if len(contents) != 2 || contents[0] != "test1" || contents[1] != "test2" {
		t.Fatalf("expected stream contents %q, got %q", []string{"test1", "test2"}, contents)
	}
	if !doneReceived {
		t.Fatal("expected Gateway stream to include data: [DONE]")
	}
}

func TestCancelAfterFirstStreamChunk(t *testing.T) {
	gotFirstChunk := make(chan struct{})
	isFirstChunk := true
	errChan := make(chan error, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "upstream response writer does not support flushing", http.StatusInternalServerError)
			return
		}

		sendContent := func(content string) error {
			resp := protocol.ChatCompletionStreamResponse{
				Choices: []protocol.ChoiceChunk{
					{
						Delta: protocol.Delta{
							Content: content,
						},
					},
				},
			}

			jsonData, err := json.Marshal(resp)
			if err != nil {
				return err
			}
			if _, err := io.WriteString(w, "data: "+string(jsonData)+"\n\n"); err != nil {
				return err
			}
			flusher.Flush()
			return nil
		}

		if err := sendContent("test1"); err != nil {
			t.Errorf("write and flush first upstream chunk: %v", err)
			return
		}

		select {
		case <-time.After(2 * time.Second):
			errChan <- errors.New("client did not receive the first chunk within 2 seconds")
			return
		case <-gotFirstChunk:
		case <-r.Context().Done():
			errChan <- r.Context().Err()
			return
		}

		select {
		case <-time.After(2 * time.Second):
			errChan <- errors.New("upstream request was not canceled within 2 seconds")
		case <-r.Context().Done():
			errChan <- r.Context().Err()
		}
	}))
	defer upstream.Close()

	cfg := config.Config{
		Models: []config.Model{
			{
				LogicModel: "logic-model",
				Upstream:   "test",
				RealModel:  "real-model",
			},
		},
		Upstreams: []config.Upstream{
			{
				Name:    "test",
				BaseURL: upstream.URL,
			},
		},
	}
	modelRouter, err := buildRouter(cfg)
	if err != nil {
		t.Fatalf("build Gateway router: %v", err)
	}
	handler := middleware.GetGatewayRequestID(
		middleware.LogRequest(
			middleware.ValidateJSONRequest(
				middleware.RequestTimeout(5 * time.Second)(modelRouter),
			),
		),
	)
	server := httptest.NewServer(handler)
	defer server.Close()

	chatReq := protocol.ChatCompletionRequest{
		Model: "logic-model",
		Messages: []protocol.Message{
			{
				Role:    "user",
				Content: "test",
			},
		},
		Stream: true,
	}
	jsonData, err := json.Marshal(chatReq)
	if err != nil {
		t.Fatalf("encode streaming chat request: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/chat/completions", bytes.NewReader(jsonData))
	if err != nil {
		t.Fatalf("create streaming Gateway request: %v", err)
	}

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("send streaming request through Gateway: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected Gateway status %d, got %d", http.StatusOK, resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("expected Gateway Content-Type %q, got %q", "text/event-stream", got)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if len(line) == 0 {
			continue
		}
		before, after, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(before) != "data" {
			t.Fatalf("expected first SSE line to use the data field, got %q", line)
		}
		data := strings.TrimSpace(after)
		if data == "[DONE]" {
			break
		}
		var chunk protocol.ChatCompletionStreamResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("decode stream chunk: %v", err)
		}
		if len(chunk.Choices) != 1 {
			t.Fatalf("expected one choice in stream chunk, got %+v", chunk.Choices)
		}
		content := chunk.Choices[0].Delta.Content
		if content != "test1" {
			t.Fatalf("expected first stream chunk content %q, got %q", "test1", content)
		}

		if isFirstChunk {
			isFirstChunk = false
			cancel()
			close(gotFirstChunk)
		}
	}
	if err := scanner.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected reading Gateway stream to stop with %v, got %v", context.Canceled, err)
	}

	select {
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Upstream to observe cancellation")
	case err := <-errChan:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected Upstream request context error %v, got %v", context.Canceled, err)
		}
	}
}
