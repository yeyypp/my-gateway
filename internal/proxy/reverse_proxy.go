package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/http/httputil"
	"net/url"
	"sync/atomic"

	"shuai/gateway/internal/handlers"
	"shuai/gateway/internal/log"
	"shuai/gateway/internal/middleware"
	"shuai/gateway/internal/protocol"

	"go.uber.org/zap"
)

type Proxy struct {
	routePolicy RoutePolicy
	proxy       *httputil.ReverseProxy
}

type RoutePolicy struct {
	UpstreamName string
	UpstreamURL  string
	DefaultModel string
}

func (p *Proxy) GetUpstreamName() string {
	return p.routePolicy.UpstreamName
}

func BuildReverseProxy(routePolicy RoutePolicy, transport *http.Transport, apiKey string) (*Proxy, error) {
	if routePolicy.UpstreamURL == "" || routePolicy.DefaultModel == "" || transport == nil {
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
			preq.Out.Header.Del("Authorization")
			if preq.Out.Header.Get("Content-Type") == "" {
				preq.Out.Header.Set("Content-Type", "application/json")
			}
			if len(apiKey) != 0 {
				preq.Out.Header.Set("Authorization", "Bearer "+apiKey)
			}
		},

		Transport: transport,

		ModifyResponse: func(resp *http.Response) error {
			// 此处删除的是从upstream收到响应中的id，不影响middleware在外层设定的id
			resp.Header.Del(protocol.HeaderGatewayRequestID)
			if resp.StatusCode >= http.StatusBadRequest && resp.StatusCode <= 599 {
				normalizeErrorResponse(resp)
			}

			fields := make([]zap.Field, 0, 9)
			if resp.Request != nil {
				fields = append(fields, middleware.GetReqIDFromCTX(resp.Request.Context())...)
				fields = append(fields,
					zap.String("method", resp.Request.Method),
					zap.String("path", resp.Request.URL.Path),
				)
				if chatReq, ok := resp.Request.Context().Value(middleware.ChatReqKey).(protocol.ChatCompletionRequest); ok {
					fields = append(fields, zap.String("logical_model", chatReq.Model))
					fields = append(fields, zap.Bool("stream", chatReq.Stream))
				}
			}
			fields = append(fields,
				zap.String("upstream", target.Host),
				zap.String("model", routePolicy.DefaultModel),
				zap.Int("status", resp.StatusCode),
			)
			if upstreamRequestID := resp.Header.Get(protocol.HeaderUpstreamRequestID); upstreamRequestID != "" {
				fields = append(fields, zap.String("upstream_request_id", upstreamRequestID))
			}

			if isSSEContentType(resp.Header.Get("Content-Type")) {
				resp.Body = newSSETrackBody(resp.Body, fields)
			}

			log.Info("upstream response received", fields...)
			return nil
		},

		// 处理下游响应开始前的 RoundTrip / ModifyResponse 错误，不处理响应体复制阶段的错误。
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			rc, _ := req.Context().Value("rc").(*ReqContext)
			if rc != nil {
				rc.isSuccess = false
				rc.err = err
			}

			var netErr net.Error
			fields := append(middleware.GetReqIDFromCTX(req.Context()),
				zap.String("method", req.Method),
				zap.String("path", req.URL.Path),
				zap.String("upstream", target.Host),
				zap.String("model", routePolicy.DefaultModel),
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
				if rc == nil {
					handlers.SendError(w, protocol.NewError(protocol.ErrorTypeServer, protocol.ErrorCodeUpstreamTimeout, "Upstream request timed out.", ""), http.StatusGatewayTimeout)
				}
				return
			default:
				log.Error("upstream unavailable", fields...)
				if rc == nil {
					handlers.SendError(w, protocol.NewError(protocol.ErrorTypeServer, protocol.ErrorCodeUpstreamUnavailable, "Upstream unavailable.", ""), http.StatusServiceUnavailable)
				}
			}
		},
	}

	return &Proxy{
		routePolicy: routePolicy,
		proxy:       proxy,
	}, nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rawBody, ok := middleware.GetRequestBodyFromCTX(r.Context())
	if !ok {
		log.Error("validated request body missing from context", middleware.GetReqIDFromCTX(r.Context())...)
		handlers.SendError(w, protocol.APIError{
			Message: "validated request body missing",
			Type:    protocol.ErrorTypeServer,
			Code:    protocol.ErrorCodeRequestEncodingFailed,
		}, http.StatusInternalServerError)
		return
	}

	upstreamBody, err := changeModel(rawBody, p.routePolicy.DefaultModel)
	if err != nil {
		fields := append(middleware.GetReqIDFromCTX(r.Context()), zap.Error(err))
		log.Error("rewrite upstream model failed", fields...)
		handlers.SendError(w, protocol.APIError{
			Message: "rewrite upstream model failed",
			Type:    protocol.ErrorTypeServer,
			Code:    protocol.ErrorCodeRequestEncodingFailed,
		}, http.StatusInternalServerError)
		return
	}

	proxyReq := r.Clone(r.Context())
	proxyReq.Body = io.NopCloser(bytes.NewReader(upstreamBody))
	proxyReq.ContentLength = int64(len(upstreamBody))

	p.proxy.ServeHTTP(w, proxyReq)
}

// changeModel replaces the top-level model field of an already validated Chat
// Completions body and returns the bytes to forward. Every other field keeps the
// bytes the client sent, so fields the Gateway does not model survive.
func changeModel(rawBody []byte, targetModel string) ([]byte, error) {
	var partialReq map[string]json.RawMessage
	if err := json.Unmarshal(rawBody, &partialReq); err != nil {
		return nil, err
	}

	encodedModel, err := json.Marshal(targetModel)
	if err != nil {
		return nil, err
	}
	partialReq["model"] = encodedModel

	return json.Marshal(partialReq)
}

func isSSEContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "text/event-stream"
}

func newSSETrackBody(src io.ReadCloser, fields []zap.Field) *sseTrackBody {
	return &sseTrackBody{
		src:    src,
		fields: fields,
	}
}

type sseTrackBody struct {
	src    io.ReadCloser
	fields []zap.Field
}

func (s *sseTrackBody) Read(p []byte) (int, error) {
	n, err := s.src.Read(p)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
		fields := append(s.fields, zap.Error(err))
		log.Error("read from upstream interrupted", fields...)
	}
	return n, err
}

func (s *sseTrackBody) Close() error {
	return s.src.Close()
}

type ReqContext struct {
	gotConn   atomic.Bool
	isSuccess bool
	err       error
}

func NewReqContext() *ReqContext {
	return &ReqContext{
		isSuccess: true,
	}
}

func (rc *ReqContext) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GotConn: func(gci httptrace.GotConnInfo) {
			rc.gotConn.Store(true)
		},
	}
}

func (rc *ReqContext) NeedRetry() bool {
	return !rc.isSuccess && !rc.gotConn.Load()
}

func (rc *ReqContext) IsSuccess() bool {
	return rc.isSuccess
}

func (rc *ReqContext) Err() error {
	return rc.err
}

func AttachReqContext(rc *ReqContext, req *http.Request) *http.Request {
	ctx := context.WithValue(req.Context(), "rc", rc)
	return req.WithContext(httptrace.WithClientTrace(ctx, rc.clientTrace()))
}
