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
		},

		Transport: transport,

		ModifyResponse: func(resp *http.Response) error {
			var path string
			if resp.Request != nil {
				path = resp.Request.URL.Path
			}

			log.Info("upstream response received",
				zap.String("upstream", target.Host),
				zap.String("model", routePolicy.UpstreamModel),
				zap.String("path", path),
			)
			return nil
		},

		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			var netErr net.Error
			switch {
			case errors.Is(req.Context().Err(), context.Canceled):
				log.Error("client canceled request",
					zap.String("method", req.Method),
					zap.String("path", req.URL.Path),
					zap.Error(err),
				)
				return
			case errors.Is(req.Context().Err(), context.DeadlineExceeded),
				errors.Is(err, context.DeadlineExceeded),
				errors.As(err, &netErr) && netErr.Timeout():
				log.Error("upstream request timed out",
					zap.String("upstream", target.Host),
					zap.Error(err),
				)
				handlers.SendError(w, protocol.APIError{
					Message: "upstream timeout",
					Type:    protocol.ErrorTypeGateway,
					Code:    protocol.ErrorCodeUpstreamTimeout,
				}, http.StatusGatewayTimeout)
				return
			default:
				log.Error("upstream unavailable",
					zap.String("upstream", target.Host),
					zap.Error(err),
				)
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
			log.Error("validated request missing from context")
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
			log.Error("encode upstream request failed",
				zap.Error(err),
			)
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
