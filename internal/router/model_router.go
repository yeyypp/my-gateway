package router

import (
	"net/http"

	"shuai/gateway/internal/handlers"
	"shuai/gateway/internal/log"
	"shuai/gateway/internal/middleware"
	"shuai/gateway/internal/protocol"

	"go.uber.org/zap"
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
	if !ok || chatReq.Model == "" {
		log.Error("model is required", middleware.GetReqIDFromCTX(r.Context())...)
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
		fields := append(middleware.GetReqIDFromCTX(r.Context()), zap.String("logical_model", chatReq.Model))
		log.Error("model is not supported", fields...)
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
