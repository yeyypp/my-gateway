package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"

	"shuai/gateway/internal/handlers"
	"shuai/gateway/internal/log"
	"shuai/gateway/internal/middleware"
	"shuai/gateway/internal/protocol"

	"go.uber.org/zap"
)

type RoutePolicy struct {
	UpstreamURL   string
	UpstreamModel string
}

func BuildReverseProxy(routePolicy RoutePolicy, transport *http.Transport) (http.Handler, error) {
	if routePolicy.UpstreamURL == "" || routePolicy.UpstreamModel == "" || transport == nil {
		return nil, errors.New("invalid policy or transport")
	}
	target, err := url.Parse(routePolicy.UpstreamURL)
	if err != nil {
		return nil, err
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(preq *httputil.ProxyRequest) {
			preq.SetURL(target)
			preq.SetXForwarded()
			preq.Out.Header.Del(protocol.HeaderClientRequestID)
		},

		Transport: transport,

		ModifyResponse: func(resp *http.Response) error {
			// 此处删除的是从upstream收到响应中的id，不影响middleware在外层设定的id
			resp.Header.Del(protocol.HeaderGatewayRequestID)

			fields := make([]zap.Field, 0, 9)
			if resp.Request != nil {
				fields = append(fields, middleware.GetReqIDFromCTX(resp.Request.Context())...)
				fields = append(fields,
					zap.String("method", resp.Request.Method),
					zap.String("path", resp.Request.URL.Path),
				)
				if chatReq, ok := resp.Request.Context().Value(middleware.ChatReqKey).(protocol.ChatCompletionRequest); ok {
					fields = append(fields, zap.String("logical_model", chatReq.Model))
				}
			}
			fields = append(fields,
				zap.String("upstream", target.Host),
				zap.String("model", routePolicy.UpstreamModel),
				zap.Int("status", resp.StatusCode),
			)
			if upstreamRequestID := resp.Header.Get(protocol.HeaderUpstreamRequestID); upstreamRequestID != "" {
				fields = append(fields, zap.String("upstream_request_id", upstreamRequestID))
			}

			log.Info("upstream response received", fields...)
			return nil
		},

		// 处理下游响应开始前的 RoundTrip / ModifyResponse 错误，不处理响应体复制阶段的错误。
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			var netErr net.Error
			fields := append(middleware.GetReqIDFromCTX(req.Context()),
				zap.String("method", req.Method),
				zap.String("path", req.URL.Path),
				zap.String("upstream", target.Host),
				zap.String("model", routePolicy.UpstreamModel),
				zap.Error(err),
			)
			switch {
			case errors.Is(req.Context().Err(), context.Canceled):
				log.Error("client canceled request", fields...)
				return
			case errors.Is(req.Context().Err(), context.DeadlineExceeded),
				errors.Is(err, context.DeadlineExceeded),
				errors.As(err, &netErr) && netErr.Timeout():
				log.Error("upstream request timed out", fields...)
				handlers.SendError(w, protocol.APIError{
					Message: "upstream timeout",
					Type:    protocol.ErrorTypeGateway,
					Code:    protocol.ErrorCodeUpstreamTimeout,
				}, http.StatusGatewayTimeout)
				return
			default:
				log.Error("upstream unavailable", fields...)
				handlers.SendError(w, protocol.APIError{
					Message: "upstream unavailable",
					Type:    protocol.ErrorTypeGateway,
					Code:    protocol.ErrorCodeUpstreamUnavailable,
				}, http.StatusBadGateway)
			}
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatReq, ok := r.Context().Value(middleware.ChatReqKey).(protocol.ChatCompletionRequest)
		if !ok {
			log.Error("validated request missing from context", middleware.GetReqIDFromCTX(r.Context())...)
			handlers.SendError(w, protocol.APIError{
				Message: "missing chatReq",
				Type:    protocol.ErrorTypeGateway,
				Code:    protocol.ErrorCodeMissingRequiredParameter,
			}, http.StatusBadGateway)
			return
		}

		chatReq.Model = routePolicy.UpstreamModel
		content, err := json.Marshal(chatReq)
		if err != nil {
			fields := append(middleware.GetReqIDFromCTX(r.Context()), zap.Error(err))
			log.Error("encode upstream request failed", fields...)
			handlers.SendError(w, protocol.APIError{
				Message: "encode upstream request failed",
				Type:    protocol.ErrorTypeGateway,
				Code:    protocol.ErrorCodeRequestEncodingFailed,
			}, http.StatusInternalServerError)
			return
		}

		proxyReq := r.Clone(r.Context())
		proxyReq.Body = io.NopCloser(bytes.NewReader(content))
		proxyReq.ContentLength = int64(len(content))
		proxy.ServeHTTP(w, proxyReq)
	}), nil

}
