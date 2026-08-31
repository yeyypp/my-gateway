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
	requestID, ok := r.Context().Value(middleware.RequestIDKey).(string)
	if !ok {
		log.Error("get requestID from req context failed")
	}

	chatReq, ok := r.Context().Value(middleware.ChatReqKey).(protocol.ChatCompletionRequest)
	if !ok || chatReq.Model == "" {
		log.Error("model is required", zap.String("request-id", requestID))
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
		log.Error("model is not supported",
			zap.String("logical_model", chatReq.Model),
			zap.String("request-id", requestID),
		)
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
