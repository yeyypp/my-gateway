package internal

import (
	"errors"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
)

type RoutePolicy struct {
	UpstreamURL string
	//ResponseHeaderWait time.Duration // Duration to wait for upstream response headers
	//RequestDeadline    time.Duration // Duration to wait for the entire request to complete
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
			// TODO clean up response headers if needed
			return nil
		},

		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			log.Printf("Error proxying request to upstream %s: %v", routePolicy.UpstreamURL, err)
			rw.WriteHeader(http.StatusBadGateway)
		},
	}

	return proxy, nil
}
