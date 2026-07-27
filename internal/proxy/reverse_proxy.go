package proxy

import (
	"errors"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
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

		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			log.Printf("Error proxying request to upstream %s: %v", routePolicy.UpstreamURL, err)
			rw.WriteHeader(http.StatusBadGateway)
		},
	}

	return proxy, nil
}
