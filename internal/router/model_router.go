package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"shuai/gateway/internal/handlers"
)

type ModelRouter struct {
	routes map[string]http.Handler
}

func NewModelRouter(modela, modelb http.Handler) *ModelRouter {
	m := make(map[string]http.Handler)
	m["model-a"] = modela
	m["model-b"] = modelb
	return &ModelRouter{
		routes: m,
	}
}

func (m *ModelRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlers.SendError(w, handlers.APIError{
			Message: fmt.Sprintf("read body failed, err:%s", err.Error()),
		}, http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))

	var selector struct {
		Model string `json:"model"`
	}
	err = json.Unmarshal(body, &selector)
	if err != nil {
		handlers.SendError(w, handlers.APIError{
			Message: fmt.Sprintf("invalid json, err:%s", err.Error()),
		}, http.StatusBadRequest)
		return
	}

	target, ok := m.routes[selector.Model]
	if !ok {
		handlers.SendError(w, handlers.APIError{
			Message: "invalid_model",
		}, http.StatusBadRequest)
		return
	}
	target.ServeHTTP(w, r)
}
