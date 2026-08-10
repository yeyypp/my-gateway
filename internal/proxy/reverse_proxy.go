package proxy

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"shuai/gateway/internal/api"
)

type RoutePolicy struct {
	UpstreamURL string
}

func BuildReverseProxy(routePolicy RoutePolicy, transport *http.Transport) (http.Handler, error) {
	if routePolicy.UpstreamURL == "" || transport == nil {
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
			var method, path string
			if resp.Request != nil {
				method = resp.Request.Method
				path = resp.Request.URL.Path
			}

			log.Printf(
				"proxy_response category=%s status=%d upstream=%s method=%s path=%s",
				upstreamResponseCategory(resp.StatusCode),
				resp.StatusCode,
				routePolicy.UpstreamURL,
				method,
				path,
			)
			return nil
		},

		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			switch {
			case errors.Is(req.Context().Err(), context.Canceled):
				log.Printf(
					"proxy_error category=client_canceled method=%s path=%s err=%v",
					req.Method,
					req.URL.Path,
					err,
				)
				return
			case errors.Is(req.Context().Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded):
				log.Printf(
					"proxy_error category=upstream_timeout status=%d method=%s path=%s err=%v",
					http.StatusGatewayTimeout,
					req.Method,
					req.URL.Path,
					err,
				)
				api.ResponseJSON(
					w,
					http.StatusGatewayTimeout,
					"upstream_timeout",
					nil,
				)
				return
			default:
				log.Printf(
					"proxy_error category=upstream_unavailable status=%d upstream=%s method=%s path=%s err=%v",
					http.StatusBadGateway,
					routePolicy.UpstreamURL,
					req.Method,
					req.URL.Path,
					err,
				)
				api.ResponseJSON(
					w,
					http.StatusBadGateway,
					"upstream unavailable",
					nil,
				)
			}
		},
	}

	return proxy, nil
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
