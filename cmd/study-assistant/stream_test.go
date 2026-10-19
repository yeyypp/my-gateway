package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseStreamWithOfficialTypes(t *testing.T) {
	stream := ": keepalive\r\n\r\ndata: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\r\n\r\n" +
		"data: {\"choices\":[\n" +
		"data: {\"index\":0,\"delta\":{\"content\":\"hello 世界\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3},\"extension\":true}\n\n" +
		"data: [DONE]\n\n"
	var out bytes.Buffer
	if err := parseStream(strings.NewReader(stream), &out, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "TTFT:") || !strings.HasSuffix(out.String(), "ms\nhello 世界") || strings.Count(out.String(), "TTFT:") != 1 {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestParseStreamReportsErrorsAndTruncation(t *testing.T) {
	for _, tc := range []struct{ name, stream, want string }{
		{"API error", "event: error\ndata: {\"error\":{\"message\":\"Provider failed.\",\"type\":\"server_error\",\"code\":null,\"param\":null}}\n\ndata: [DONE]\n\n", "gateway stream failed: type=server_error"},
		{"truncated", "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n", "stream ended before [DONE]"},
		{"malformed JSON", "data: {\n\n", "decode stream chunk"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := parseStream(strings.NewReader(tc.stream), &out, time.Now())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestAnalyzeUsesGatewayAPIKeyAndParsesStream(t *testing.T) {
	t.Setenv("GATEWAY_API_KEY", "caller-key")
	file := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(file, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer caller-key" {
			t.Error("missing caller credential")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	var out bytes.Buffer
	if err := runAnalyze(context.Background(), server.Client(), server.URL, file, "logical", &out, true); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "Model:logical\nTTFT:") || !strings.HasSuffix(out.String(), "answer") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}
