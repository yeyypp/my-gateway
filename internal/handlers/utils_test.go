package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"shuai/gateway/internal/protocol"
)

type failedJSONMarshaler struct{}

func (failedJSONMarshaler) MarshalJSON() ([]byte, error) {
	return nil, errors.New("private encoder failure")
}

type failingResponseWriter struct {
	header           http.Header
	statuses, writes int
}

func (w *failingResponseWriter) Header() http.Header { return w.header }
func (w *failingResponseWriter) WriteHeader(int)     { w.statuses++ }
func (w *failingResponseWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, errors.New("client disconnected")
}

func TestJSONEncodeFailureIsOneStandardError(t *testing.T) {
	w := httptest.NewRecorder()
	SendJSON(w, failedJSONMarshaler{})
	var envelope protocol.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || w.Code != 500 ||
		envelope.Error.Type != protocol.ErrorTypeServer || envelope.Error.Code != protocol.ErrorCodeInternal {
		t.Fatalf("invalid fallback: status=%d body=%s", w.Code, w.Body)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("private")) {
		t.Fatal("encoder diagnostics leaked")
	}
}

func TestJSONWriteFailureDoesNotAppendAnotherResponse(t *testing.T) {
	w := &failingResponseWriter{header: make(http.Header)}
	SendError(w, protocol.ErrorForStatus(401), 401)
	if w.statuses != 1 || w.writes != 1 {
		t.Fatalf("status writes=%d body writes=%d", w.statuses, w.writes)
	}
}

func TestErrorOptionalFieldsAreNull(t *testing.T) {
	w := httptest.NewRecorder()
	SendError(w, protocol.NewError(protocol.ErrorTypeServer, "", "Request failed.", ""), 500)
	var raw struct {
		Error map[string]json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw.Error["param"]) != "null" || string(raw.Error["code"]) != "null" {
		t.Fatalf("expected null fields: %s", w.Body)
	}
}
