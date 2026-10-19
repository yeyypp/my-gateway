package router

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"shuai/gateway/internal/handlers"
	"shuai/gateway/internal/log"
	"shuai/gateway/internal/middleware"
	"shuai/gateway/internal/protocol"
	"shuai/gateway/internal/proxy"

	"go.uber.org/zap"
)

type ModelRouter struct {
	routes map[string][]*proxy.Proxy
}

func NewModelRouter(routes map[string][]*proxy.Proxy) *ModelRouter {
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

	upstreams, ok := m.routes[chatReq.Model]
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

	for i, up := range upstreams {
		rc := proxy.NewReqContext()
		newR := proxy.AttachReqContext(rc, r)
		up.ServeHTTP(w, newR)

		if r.Context().Err() == nil {
			deadline, ok := r.Context().Deadline()
			if ok && time.Until(deadline) > 0 || !ok {
				if rc.NeedRetry() && i != len(upstreams)-1 {
					log.Info("upstream failed, send to another one",
						zap.String("failed upstream", up.GetUpstreamName()))
					continue
				}
			}
		}

		if !rc.IsSuccess() {
			var netErr net.Error
			switch {
			case errors.Is(r.Context().Err(), context.Canceled):
				log.Error("client canceled request")
				return
			case errors.Is(r.Context().Err(), context.DeadlineExceeded),
				errors.Is(rc.Err(), context.DeadlineExceeded),
				errors.As(rc.Err(), &netErr) && netErr.Timeout():
				log.Error("upstream request timed out")

				handlers.SendError(w, protocol.APIError{
					Message: "upstream timeout",
					Type:    protocol.ErrorTypeServer,
					Code:    protocol.ErrorCodeUpstreamTimeout,
				}, http.StatusGatewayTimeout)
				return
			default:
				log.Error("upstream unavailable")
				handlers.SendError(w, protocol.APIError{
					Message: "upstream unavailable",
					Type:    protocol.ErrorTypeServer,
					Code:    protocol.ErrorCodeUpstreamUnavailable,
				}, http.StatusServiceUnavailable)
				return
			}
		}
		return
	}
}
