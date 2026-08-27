package router

import (
	"net/http"
	"shuai/gateway/internal/handlers"
	"shuai/gateway/internal/middleware"
	"shuai/gateway/internal/protocol"
)

type ModelRouter struct {
	routes map[string]http.Handler
}

func NewModelRouter(routes map[string]http.Handler) *ModelRouter {
	return &ModelRouter{
		routes: routes,
	}
}

func (m *ModelRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	chatReq, ok := r.Context().Value(middleware.ChatReqKey).(protocol.ChatCompletionRequest)
	if !ok {
		handlers.SendError(w, protocol.APIError{
			Message: "model is required",
			Type:    protocol.ErrorTypeInvalidRequest,
			Param:   "model",
			Code:    protocol.ErrorCodeMissingRequiredParameter,
		}, http.StatusBadRequest)
		return
	}

	target, ok := m.routes[chatReq.Model]
	if !ok {
		handlers.SendError(w, protocol.APIError{
			Message: "model is not supported",
			Type:    protocol.ErrorTypeInvalidRequest,
			Param:   "model",
			Code:    protocol.ErrorCodeModelNotFound,
		}, http.StatusBadRequest)
		return
	}
	target.ServeHTTP(w, r)
}
