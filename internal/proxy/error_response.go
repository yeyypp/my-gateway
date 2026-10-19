package proxy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"shuai/gateway/internal/protocol"
)

const maxErrorBodyBytes = 64 << 10

// normalizeErrorResponse runs before downstream headers are committed. A known
// HTTP response remains a response even when its body cannot be read/decoded;
// it must never enter the transport error/fallback path.
func normalizeErrorResponse(resp *http.Response) {
	apiErr := protocol.UpstreamHTTPError(resp.StatusCode)
	if resp.Body != nil {
		body, err := readErrorBody(resp.Body)
		_ = resp.Body.Close()
		if err == nil && len(body) <= maxErrorBodyBytes {
			switch strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))) {
			case "", "identity":
			case "gzip":
				var reader *gzip.Reader
				reader, err = gzip.NewReader(bytes.NewReader(body))
				if err == nil {
					body, err = readErrorBody(reader)
					_ = reader.Close()
				}
			default:
				body = nil
			}
			var envelope struct {
				Error *protocol.APIError `json:"error"`
			}
			if err == nil && len(body) <= maxErrorBodyBytes && json.Unmarshal(body, &envelope) == nil &&
				envelope.Error != nil && strings.TrimSpace(envelope.Error.Message) != "" &&
				strings.TrimSpace(string(envelope.Error.Type)) != "" {
				apiErr = *envelope.Error
			}
		}
	}
	body, _ := json.Marshal(protocol.ErrorResponse{Error: apiErr})
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.TransferEncoding = nil
	resp.Trailer = nil
	if resp.Header == nil {
		resp.Header = make(http.Header)
	}
	for _, name := range []string{"Content-Encoding", "Transfer-Encoding", "Trailer", "ETag", "Content-MD5", "Digest", "Content-Digest", "Repr-Digest", "Content-Range"} {
		resp.Header.Del(name)
	}
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
}

func readErrorBody(reader io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(reader, maxErrorBodyBytes+1))
}
