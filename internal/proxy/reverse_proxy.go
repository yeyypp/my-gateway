package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"

	"shuai/gateway/internal/handlers"
	"shuai/gateway/internal/log"
	"shuai/gateway/internal/middleware"
	"shuai/gateway/internal/protocol"
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

			log.Infof("upstream:%s,model:%s,path:%s", routePolicy.UpstreamURL, routePolicy.UpstreamModel, path)
			return nil
		},

		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			switch {
			case errors.Is(req.Context().Err(), context.Canceled):
				log.Errorf("client canceled,method:%s,path:%s,err:%v", req.Method, req.URL.Path, err)
				return
			case errors.Is(req.Context().Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded):
				log.Errorf("upstream timeout, upstream:%s, err:%v", routePolicy.UpstreamURL, err)
				handlers.SendError(w, protocol.APIError{
					Message: "upstream timeout",
					Type:    protocol.ErrorTypeGateway,
					Code:    protocol.ErrorCodeUpstreamTimeout,
				}, http.StatusGatewayTimeout)
				return
			default:
				log.Errorf("upstream available, upstream:%s, err:%v", routePolicy.UpstreamURL, err)
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
			log.Errorf("missing chatReq")
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
			log.Errorf("json marshal chatReq failed. err:%v", err)
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

func upstreamResponseCategory(statusCode int) string {
	switch {
	case statusCode >= http.StatusInternalServerError:
		return "upstream_server_error"
	case statusCode >= http.StatusBadRequest:
		return "upstream_client_error"
	default:
		return "success"
	}
}
