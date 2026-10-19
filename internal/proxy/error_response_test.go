package proxy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"shuai/gateway/internal/protocol"
)

type errorTestBody struct {
	io.Reader
	closed bool
}

func (b *errorTestBody) Close() error { b.closed = true; return nil }

type failingErrorReader struct{}

func (failingErrorReader) Read([]byte) (int, error) { return 0, errors.New("private read failure") }

func TestNormalizeUpstreamErrorResponse(t *testing.T) {
	standard := `{"error":{"message":"Provider rate limit.","type":"rate_limit_error","param":"model","code":"provider_limit"}}`
	gzipBody := func(body string) string {
		var buffer bytes.Buffer
		writer := gzip.NewWriter(&buffer)
		_, _ = writer.Write([]byte(body))
		_ = writer.Close()
		return buffer.String()
	}
	for _, tc := range []struct {
		name, body, encoding string
		reader               io.Reader
		standard             bool
	}{
		{name: "standard", body: standard, standard: true},
		{name: "null optional fields", body: `{"error":{"message":"Provider rate limit.","type":"rate_limit_error","param":null,"code":null}}`, standard: true},
		{name: "omitted optional fields", body: `{"error":{"message":"Provider rate limit.","type":"rate_limit_error"}}`, standard: true},
		{name: "text", body: "private diagnostic"},
		{name: "HTML", body: "<html>private diagnostic</html>"},
		{name: "empty"},
		{name: "other JSON", body: `{"message":"private diagnostic"}`},
		{name: "null error", body: `{"error":null}`},
		{name: "missing message", body: `{"error":{"type":"server_error"}}`},
		{name: "numeric code", body: `{"error":{"message":"private","type":"server_error","code":123}}`},
		{name: "read failure", reader: failingErrorReader{}},
		{name: "oversized valid error", body: `{"error":{"message":"` + strings.Repeat("x", maxErrorBodyBytes) + `","type":"server_error"}}`},
		{name: "gzip", body: gzipBody(standard), encoding: "gzip", standard: true},
		{name: "invalid gzip", body: "private diagnostic", encoding: "gzip"},
		{name: "gzip expanded too large", body: gzipBody(strings.Repeat("x", maxErrorBodyBytes+1)), encoding: "gzip"},
		{name: "unsupported encoding", body: standard, encoding: "br"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := tc.reader
			if reader == nil {
				reader = strings.NewReader(tc.body)
			}
			original := &errorTestBody{Reader: reader}
			response := &http.Response{
				StatusCode: 429, Body: original, ContentLength: int64(len(tc.body)),
				Header: http.Header{"Content-Type": {"text/html"}, "Content-Encoding": {tc.encoding}, "Content-Length": {"999"},
					"Retry-After": {"7"}, "Etag": {"old"}, "Content-Md5": {"old"}, "Trailer": {"X-Old"}, "Digest": {"old"},
					http.CanonicalHeaderKey(protocol.HeaderUpstreamRequestID): {"provider-123"}},
				TransferEncoding: []string{"chunked"}, Trailer: http.Header{"X-Old": {"old"}},
			}
			normalizeErrorResponse(response)
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			var envelope protocol.ErrorResponse
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Fatal(err)
			}
			if !original.closed || response.StatusCode != 429 || response.ContentLength != int64(len(body)) || response.Header.Get("Content-Length") != strconv.Itoa(len(body)) {
				t.Fatalf("invalid response metadata: %+v", response)
			}
			if response.Header.Get("Retry-After") != "7" || response.Header.Get(protocol.HeaderUpstreamRequestID) != "provider-123" || response.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("lost response headers: %+v", response.Header)
			}
			for _, name := range []string{"Content-Encoding", "ETag", "Content-MD5", "Trailer", "Digest"} {
				if response.Header.Get(name) != "" {
					t.Errorf("stale %s", name)
				}
			}
			if response.TransferEncoding != nil || response.Trailer != nil {
				t.Fatal("stale trailers/transfer encoding")
			}
			if tc.standard {
				if envelope.Error.Message != "Provider rate limit." || envelope.Error.Type != protocol.ErrorTypeRateLimit {
					t.Fatalf("lost provider fields: %s", body)
				}
				if tc.name == "standard" && (envelope.Error.Code != "provider_limit" || envelope.Error.Param != "model") {
					t.Fatalf("lost provider code/param: %s", body)
				}
			} else if envelope.Error.Code != protocol.ErrorCodeUpstreamHTTP || envelope.Error.Message != "Upstream returned HTTP 429." {
				t.Fatalf("invalid fallback: %s", body)
			}
			if (tc.name == "null optional fields" || tc.name == "omitted optional fields") && (!bytes.Contains(body, []byte(`"param":null`)) || !bytes.Contains(body, []byte(`"code":null`))) {
				t.Fatalf("expected null optional fields: %s", body)
			}
		})
	}
}

func TestSuccessfulResponsesRemainByteForByte(t *testing.T) {
	for _, tc := range []struct{ name, contentType, body string }{
		{"JSON", "application/json", `{"id":"real-id","model":"real-model","choices":[],"provider_extension":{"nested":[1,2]}}`},
		{"SSE", "text/event-stream", "data: {\"id\":\"real-id\",\"model\":\"real-model\",\"provider_extension\":true}\n\ndata: [DONE]\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer upstream.Close()
			w := httptest.NewRecorder()
			newTestProxy(t, upstream.URL).ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(testChatBody)))
			if w.Code != 200 || w.Body.String() != tc.body || w.Header().Get("Content-Type") != tc.contentType {
				t.Fatalf("success response changed: %d %s", w.Code, w.Body)
			}
		})
	}
}
