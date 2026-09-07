package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"shuai/gateway/internal/config"
	"shuai/gateway/internal/middleware"
	"shuai/gateway/internal/protocol"
)

func newStreamingGatewayHandler(t *testing.T, upstreamURL string, requestTimeout time.Duration) http.Handler {
	t.Helper()

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
				BaseURL: upstreamURL,
			},
		},
	}

	modelRouter, err := buildRouter(cfg)
	if err != nil {
		t.Fatalf("build Gateway router: %v", err)
	}

	return middleware.GetGatewayRequestID(
		middleware.LogRequest(
			middleware.ValidateJSONRequest(
				middleware.RequestTimeout(requestTimeout)(modelRouter),
			),
		),
	)
}

func newStreamingGatewayServer(t *testing.T, upstreamURL string, requestTimeout time.Duration) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(newStreamingGatewayHandler(t, upstreamURL, requestTimeout))
	t.Cleanup(server.Close)
	return server
}

func openStreamingGatewayResponse(t *testing.T, ctx context.Context, server *httptest.Server) *http.Response {
	t.Helper()

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
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("expected Gateway status %d, got %d", http.StatusOK, resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		resp.Body.Close()
		t.Fatalf("expected Gateway Content-Type %q, got %q", "text/event-stream", got)
	}

	return resp
}

func writeSSEData(w http.ResponseWriter, data string) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return errors.New("response writer does not support flushing")
	}
	if _, err := io.WriteString(w, "data: "+data+"\n\n"); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeSSEContent(w http.ResponseWriter, content string) error {
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
	return writeSSEData(w, string(jsonData))
}

func readSSEEvent(t *testing.T, scanner *bufio.Scanner) (content string, done bool) {
	t.Helper()

	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(field) != "data" {
			t.Fatalf("expected SSE line to use the data field, got %q", line)
		}

		data := strings.TrimSpace(value)
		if data == "[DONE]" {
			return "", true
		}

		var chunk protocol.ChatCompletionStreamResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("decode stream chunk: %v", err)
		}
		if len(chunk.Choices) != 1 {
			t.Fatalf("expected one choice in stream chunk, got %+v", chunk.Choices)
		}
		return chunk.Choices[0].Delta.Content, false
	}

	if err := scanner.Err(); err != nil {
		t.Fatalf("read SSE event from Gateway: %v", err)
	}
	t.Fatal("Gateway stream ended before the next SSE event")
	return "", false
}

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
		if err := writeSSEContent(w, "test1"); err != nil {
			t.Errorf("write and flush first upstream chunk: %v", err)
			return
		}
		select {
		case <-sendSecond:
		case <-r.Context().Done():
			return
		}
		if err := writeSSEData(w, "[DONE]"); err != nil {
			t.Errorf("write upstream stream terminator: %v", err)
		}
	}))
	defer upstream.Close()

	server := newStreamingGatewayServer(t, upstream.URL, 5*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp := openStreamingGatewayResponse(t, ctx, server)
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	content, done := readSSEEvent(t, scanner)
	if done || content != "test1" {
		t.Fatalf("expected first stream chunk content %q, got content %q, done %t", "test1", content, done)
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
		if err := writeSSEContent(w, "test1"); err != nil {
			t.Errorf("write and flush first upstream chunk: %v", err)
			return
		}

		select {
		case <-sendSecond:
		case <-r.Context().Done():
			return
		}

		if err := writeSSEContent(w, "test2"); err != nil {
			t.Errorf("send test2 failed: %v", err)
			return
		}

		if err := writeSSEData(w, "[DONE]"); err != nil {
			t.Errorf("write upstream stream terminator: %v", err)
		}
	}))
	defer upstream.Close()

	server := httptest.NewUnstartedServer(newStreamingGatewayHandler(t, upstream.URL, writeTimeout))
	server.Config.WriteTimeout = writeTimeout
	server.Start()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp := openStreamingGatewayResponse(t, ctx, server)
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	firstContent, done := readSSEEvent(t, scanner)
	if done || firstContent != "test1" {
		t.Fatalf("expected first stream chunk content %q, got content %q, done %t", "test1", firstContent, done)
	}

	time.Sleep(2 * writeTimeout)
	close(sendSecond)

	secondContent, done := readSSEEvent(t, scanner)
	if done || secondContent != "test2" {
		t.Fatalf("expected second stream chunk content %q, got content %q, done %t", "test2", secondContent, done)
	}
	if content, done := readSSEEvent(t, scanner); !done {
		t.Fatalf("expected Gateway stream to include data: [DONE], got content %q", content)
	}
}

func TestCancelAfterFirstStreamChunk(t *testing.T) {
	gotFirstChunk := make(chan struct{})
	errChan := make(chan error, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if err := writeSSEContent(w, "test1"); err != nil {
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

	server := newStreamingGatewayServer(t, upstream.URL, 5*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resp := openStreamingGatewayResponse(t, ctx, server)
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	content, done := readSSEEvent(t, scanner)
	if done || content != "test1" {
		t.Fatalf("expected first stream chunk content %q, got content %q, done %t", "test1", content, done)
	}
	cancel()
	close(gotFirstChunk)

	for scanner.Scan() {
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

func TestSlowClientBackPressure(t *testing.T) {
	const (
		writeCount       = 1_000_000
		stallWindow      = 200 * time.Millisecond
		operationTimeout = 3 * time.Second
	)

	upstreamStarted := make(chan struct{})
	upstreamDone := make(chan error, 1)
	gatewayDone := make(chan struct{})
	var completedWrites atomic.Int64
	var clientCanceled atomic.Bool

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "upstream response writer does not support flushing", http.StatusInternalServerError)
			return
		}

		if err := writeSSEContent(w, "test1"); err != nil {
			upstreamDone <- fmt.Errorf("write and flush first Upstream chunk: %w", err)
			return
		}

		floodEvent := []byte("data: " + strings.Repeat("x", 32*1024) + "\n\n")
		for i := 0; i < writeCount; i++ {
			select {
			case <-r.Context().Done():
				upstreamDone <- r.Context().Err()
				return
			default:
			}

			if _, err := w.Write(floodEvent); err != nil {
				if !clientCanceled.Load() {
					upstreamDone <- fmt.Errorf("upstream write failed before client cancellation: %w", err)
					return
				}
				<-r.Context().Done()
				upstreamDone <- r.Context().Err()
				return
			}
			flusher.Flush()
			completedWrites.Add(1)
			if i == 0 {
				close(upstreamStarted)
			}
		}

		upstreamDone <- errors.New("upstream completed all writes while the client was not reading")
	}))
	defer upstream.Close()

	baseHandler := newStreamingGatewayHandler(t, upstream.URL, 5*time.Second)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(gatewayDone)
		baseHandler.ServeHTTP(w, r)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resp := openStreamingGatewayResponse(t, ctx, server)
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	content, done := readSSEEvent(t, scanner)
	if done || content != "test1" {
		t.Fatalf("expected first stream chunk content %q, got content %q, done %t", "test1", content, done)
	}

	select {
	case <-upstreamStarted:
	case err := <-upstreamDone:
		t.Fatalf("expected Upstream to keep writing after the first chunk, got %v", err)
	case <-time.After(operationTimeout):
		t.Fatal("timed out waiting for Upstream streaming writes to start")
	}

	lastCompleted := completedWrites.Load()
	lastProgress := time.Now()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	watchdog := time.NewTimer(operationTimeout)
	defer watchdog.Stop()
	backPressureObserved := false
	for !backPressureObserved {
		select {
		case err := <-upstreamDone:
			t.Fatalf("expected Upstream writes to be backpressured before completion, got %v", err)
		case now := <-ticker.C:
			current := completedWrites.Load()
			if current != lastCompleted {
				lastCompleted = current
				lastProgress = now
				continue
			}
			backPressureObserved = now.Sub(lastProgress) >= stallWindow
		case <-watchdog.C:
			t.Fatalf("Upstream writes did not become backpressured within %v", operationTimeout)
		}
	}
	select {
	case err := <-upstreamDone:
		t.Fatalf("expected Upstream writes to remain blocked before cancellation, got %v", err)
	default:
	}
	if got := completedWrites.Load(); got >= writeCount {
		t.Fatalf("expected backpressure before all %d Upstream writes completed, got %d", writeCount, got)
	}

	clientCanceled.Store(true)
	cancel()

	select {
	case err := <-upstreamDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected Upstream request context error %v after cancellation, got %v", context.Canceled, err)
		}
	case <-time.After(operationTimeout):
		t.Fatalf("Upstream did not stop within %v after client cancellation", operationTimeout)
	}

	select {
	case <-gatewayDone:
	case <-time.After(operationTimeout):
		t.Fatalf("Gateway request handler did not stop within %v after client cancellation", operationTimeout)
	}
}

func TestUpstreamCrash(t *testing.T) {
	const operationTimeout = 3 * time.Second

	gotFirstChunk := make(chan struct{})
	upstreamDone := make(chan error, 1)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if err := writeSSEContent(w, "test1"); err != nil {
			upstreamDone <- fmt.Errorf("write and flush first Upstream chunk: %w", err)
			return
		}

		select {
		case <-gotFirstChunk:
		case <-r.Context().Done():
			upstreamDone <- r.Context().Err()
			return
		case <-time.After(operationTimeout):
			upstreamDone <- errors.New("client did not receive the first chunk before timeout")
			return
		}

		hijacker, ok := w.(http.Hijacker)
		if !ok {
			upstreamDone <- errors.New("Upstream response writer does not support hijacking")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			upstreamDone <- fmt.Errorf("hijack Upstream connection: %w", err)
			return
		}
		if err := conn.Close(); err != nil {
			upstreamDone <- fmt.Errorf("close hijacked Upstream connection: %w", err)
			return
		}
		upstreamDone <- nil
	}))
	defer upstream.Close()

	server := newStreamingGatewayServer(t, upstream.URL, 5*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()

	resp := openStreamingGatewayResponse(t, ctx, server)
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	content, done := readSSEEvent(t, scanner)
	if done || content != "test1" {
		t.Fatalf("expected first stream chunk content %q, got content %q, done %t", "test1", content, done)
	}
	close(gotFirstChunk)

	for scanner.Scan() {
	}
	if err := scanner.Err(); err == nil {
		t.Fatal("expected Gateway stream read to fail after Upstream connection was interrupted, got nil")
	}

	select {
	case err := <-upstreamDone:
		if err != nil {
			t.Fatalf("interrupt Upstream stream: %v", err)
		}
	case <-time.After(operationTimeout):
		t.Fatalf("timed out waiting %v for Upstream connection interruption", operationTimeout)
	}
}
