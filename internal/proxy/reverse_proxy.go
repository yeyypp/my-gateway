package proxy

import (
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
			method, path := "", ""
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
