package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"shuai/gateway/internal/config"
	"shuai/gateway/internal/handlers"
	"shuai/gateway/internal/protocol"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func newCompatibleGateway(t *testing.T, upstreamHandler http.Handler) *httptest.Server {
	t.Helper()
	t.Setenv("LOCAL_CLIENT_KEY_A", "test-client-key")
	upstream := httptest.NewServer(upstreamHandler)
	t.Cleanup(upstream.Close)
	cfg := config.Config{
		Models:    []config.Model{{LogicModel: "logical", Upstreams: []string{"mock"}}},
		Upstreams: []config.Upstream{{Name: "mock", BaseURL: upstream.URL, DefaultModel: "real-model"}},
	}
	router, err := buildRouter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newGatewayHandler(cfg, router))
	t.Cleanup(server.Close)
	return server
}

func compatibleClient(server *httptest.Server) openai.Client {
	return openai.NewClient(option.WithBaseURL(server.URL+"/v1"), option.WithAPIKey("test-client-key"),
		option.WithHTTPClient(server.Client()), option.WithMaxRetries(0))
}

func completionParams() openai.ChatCompletionNewParams {
	return openai.ChatCompletionNewParams{Model: "logical", Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hello")}}
}

func TestOfficialSDKNormalAndStreamingResponses(t *testing.T) {
	var calls atomic.Int32
	mock := handlers.NewMockAPIHandler("mock", "real-model")
	server := newCompatibleGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		mock.ServeHTTP(w, r)
	}))
	client := compatibleClient(server)
	var previousID string
	for range 2 {
		var response *http.Response
		completion, err := client.Chat.Completions.New(context.Background(), completionParams(), option.WithResponseInto(&response))
		if err != nil {
			t.Fatal(err)
		}
		if completion.Model != "real-model" || completion.Object != "chat.completion" || completion.Created <= 0 ||
			!strings.HasPrefix(completion.ID, "chatcmpl-") || completion.ID == previousID || len(completion.Choices) != 1 {
			t.Fatalf("invalid completion: %s", completion.RawJSON())
		}
		if completion.Choices[0].Message.Content != "response from mock" || completion.Choices[0].FinishReason != "stop" {
			t.Fatalf("invalid choice: %s", completion.RawJSON())
		}
		if response.Header.Get(protocol.HeaderGatewayRequestID) == "" {
			t.Fatal("missing gateway request ID")
		}
		if strings.Contains(completion.RawJSON(), `"usage"`) {
			t.Fatal("mock must not invent token usage")
		}
		previousID = completion.ID
	}
	stream := client.Chat.Completions.NewStreaming(context.Background(), completionParams())
	defer stream.Close()
	var id string
	var created int64
	var text string
	chunks := 0
	for stream.Next() {
		chunk := stream.Current()
		if chunks == 0 {
			id, created = chunk.ID, chunk.Created
		}
		if chunk.ID == "" || chunk.ID != id || chunk.Created != created || created <= 0 ||
			chunk.Model != "real-model" || chunk.Object != "chat.completion.chunk" || len(chunk.Choices) != 1 {
			t.Fatalf("invalid chunk: %s", chunk.RawJSON())
		}
		choice := chunk.Choices[0]
		if chunks < 2 && !strings.Contains(chunk.RawJSON(), `"finish_reason":null`) {
			t.Fatalf("intermediate chunk needs null finish_reason: %s", chunk.RawJSON())
		}
		if chunks == 0 && choice.Delta.Role != "assistant" || chunks == 2 && choice.FinishReason != "stop" {
			t.Fatalf("invalid stream order: %s", chunk.RawJSON())
		}
		if strings.Contains(chunk.RawJSON(), `"usage"`) {
			t.Fatal("mock must not invent stream usage")
		}
		text += choice.Delta.Content
		chunks++
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if chunks != 3 || text != "response from mock" || calls.Load() != 3 {
		t.Fatalf("chunks=%d text=%q upstream calls=%d", chunks, text, calls.Load())
	}
}

func TestOfficialSDKAPIErrorFields(t *testing.T) {
	for _, tc := range []struct {
		name              string
		status            int
		kind, code, param string
		opts              []option.RequestOption
		upstream          bool
	}{
		{name: "invalid key", status: 401, kind: "authentication_error", code: "invalid_api_key", opts: []option.RequestOption{option.WithAPIKey("invalid")}},
		{name: "missing key", status: 401, kind: "authentication_error", code: "invalid_api_key", opts: []option.RequestOption{option.WithHeaderDel("Authorization")}},
		{name: "invalid JSON", status: 400, kind: "invalid_request_error", code: "invalid_json", opts: []option.RequestOption{option.WithRequestBody("application/json", []byte(`{`))}},
		{name: "missing messages", status: 400, kind: "invalid_request_error", code: "invalid_value", param: "messages", opts: []option.RequestOption{option.WithJSONSet("messages", []any{})}},
		{name: "unknown model", status: 400, kind: "invalid_request_error", code: "model_not_found", param: "model", opts: []option.RequestOption{option.WithJSONSet("model", "missing")}},
		{name: "upstream text error", status: 503, kind: "server_error", code: "upstream_error", upstream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := newCompatibleGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "3")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, "private provider diagnostic")
			}))
			client := compatibleClient(server)
			_, err := client.Chat.Completions.New(context.Background(), completionParams(), tc.opts...)
			var apiErr *openai.Error
			if !errors.As(err, &apiErr) {
				t.Fatalf("expected *openai.Error, got %T: %v", err, err)
			}
			if apiErr.StatusCode != tc.status || apiErr.Type != tc.kind || apiErr.Code != tc.code || apiErr.Param != tc.param || apiErr.Message == "" {
				t.Fatalf("unexpected API error: status=%d body=%s", apiErr.StatusCode, apiErr.RawJSON())
			}
			if apiErr.Response.Header.Get(protocol.HeaderGatewayRequestID) == "" {
				t.Fatal("missing error request ID")
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal([]byte(apiErr.RawJSON()), &raw); err != nil {
				t.Fatal(err)
			}
			if tc.param == "" && string(raw["param"]) != "null" {
				t.Fatalf("expected param null: %s", apiErr.RawJSON())
			}
			wantCalls := int32(0)
			if tc.upstream {
				wantCalls = 1
				if apiErr.Response.Header.Get("Retry-After") != "3" || strings.Contains(apiErr.RawJSON(), "private") {
					t.Fatal("normalized error leaked diagnostics or lost Retry-After")
				}
			}
			if calls.Load() != wantCalls {
				t.Fatalf("upstream calls=%d, want %d", calls.Load(), wantCalls)
			}
		})
	}
}

func TestAPIRouteErrorsAndHealth(t *testing.T) {
	handler := newGatewayHandler(config.Config{}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected chat call") }))
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/v1", 404}, {"POST", "/v1/missing", 404}, {"GET", "/v1/chat/completions", 405},
		{"HEAD", "/v1/chat/completions", 405}, {"GET", "/healthz", 200}, {"GET", "/readyz", 200},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			if tc.status == 200 {
				return
			}
			if tc.status == 405 && w.Header().Get("Allow") != "POST" {
				t.Fatal("missing Allow header")
			}
			var response protocol.ErrorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Error.Type != protocol.ErrorTypeInvalidRequest || response.Error.Message == "" {
				t.Fatalf("invalid API route error: %s", w.Body)
			}
			if w.Header().Get(protocol.HeaderGatewayRequestID) == "" {
				t.Fatal("missing route error request ID")
			}
		})
	}
}
