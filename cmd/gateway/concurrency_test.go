package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"shuai/gateway/internal/config"
	"shuai/gateway/internal/middleware"
)

// Each request has its own upstream gate and outer-handler completion signal.
// Completion is observed after deferred admission release, including proxy panics.
type admissionResult struct {
	resp *http.Response
	err  error
}

type admissionRequest struct {
	result   chan admissionResult
	response *http.Response
	cancel   context.CancelFunc
	finish   chan struct{}
	done     chan struct{}
}

func TestConcurrencyRequestLifecycle(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, ending := range []string{"success", "cancel", "upstream failure"} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, ending), func(t *testing.T) {
				t.Setenv("LOCAL_CLIENT_KEY_A", "key-a")
				t.Setenv("LOCAL_CLIENT_KEY_B", "key-b")
				ctx, cancelAll := context.WithTimeout(context.Background(), 10*time.Second)
				gates := make(chan chan struct{}, 20)
				entered := make(chan struct{}, 20)
				completed := make(chan chan struct{}, 20)
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					// Consume the request so net/http can observe a disconnect while
					// the ordinary response is still waiting at its gate.
					if _, err := io.Copy(io.Discard, r.Body); err != nil {
						return
					}
					calls.Add(1)
					var gate chan struct{}
					select {
					case gate = <-gates:
					case <-r.Context().Done():
						return
					}
					entered <- struct{}{}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						if err := writeSSEContent(w, "first"); err != nil {
							return
						}
					}
					select {
					case <-gate:
					case <-r.Context().Done():
						return
					}
					if ending == "upstream failure" {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Errorf("hijack upstream: %v", err)
							return
						}
						conn.Close()
						return
					}
					if stream {
						_ = writeSSEData(w, "[DONE]")
					} else {
						_, _ = io.WriteString(w, `{"ok":true}`)
					}
				}))
				t.Cleanup(upstream.Close)
				router, err := buildRouter(config.Config{
					Models:    []config.Model{{LogicModel: "logic-model", Upstream: "test", RealModel: "real-model"}},
					Upstreams: []config.Upstream{{Name: "test", BaseURL: upstream.URL}},
				})
				if err != nil {
					t.Fatal(err)
				}
				chain := middleware.Chain(middleware.SetGatewayRequestID, middleware.LogRequest,
					middleware.Authorization, middleware.ConcurrencyLimitWithLimits(2, 1),
					middleware.RateLimit, middleware.ValidateJSONRequest, middleware.RequestTimeout(8*time.Second))(router)
				gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					done := <-completed
					defer close(done)
					chain.ServeHTTP(w, r)
				}))
				t.Cleanup(gateway.Close)
				t.Cleanup(cancelAll)
				wait := func(ch <-chan struct{}) {
					t.Helper()
					select {
					case <-ch:
					case <-ctx.Done():
						t.Fatal("timed out waiting for request lifecycle signal")
					}
				}
				request := func(key string) (*http.Request, context.CancelFunc, chan struct{}) {
					requestCtx, cancel := context.WithCancel(ctx)
					t.Cleanup(cancel)
					req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, gateway.URL+"/v1/chat/completions",
						strings.NewReader(fmt.Sprintf(`{"model":"logic-model","messages":[{"role":"user","content":"test"}],"stream":%t}`, stream)))
					if err != nil {
						t.Fatal(err)
					}
					req.Header.Set("Authorization", "Bearer "+key)
					req.Header.Set("Content-Type", "application/json")
					done := make(chan struct{})
					completed <- done
					return req, cancel, done
				}
				start := func(key string) admissionRequest {
					t.Helper()
					gate := make(chan struct{})
					gates <- gate
					req, cancel, done := request(key)
					resultCh := make(chan admissionResult, 1)
					go func() { resp, err := gateway.Client().Do(req); resultCh <- admissionResult{resp, err} }()
					t.Cleanup(func() {
						cancel()
						select {
						case result := <-resultCh:
							if result.resp != nil {
								result.resp.Body.Close()
							}
						default:
						}
					})
					wait(entered)
					var resp *http.Response
					if stream {
						select {
						case result := <-resultCh:
							if result.err != nil {
								t.Fatal(result.err)
							}
							resp = result.resp
						case <-ctx.Done():
							t.Fatal("timed out receiving SSE headers")
						}
						t.Cleanup(func() { resp.Body.Close() })
						if resp.StatusCode != http.StatusOK {
							t.Fatalf("SSE status = %d", resp.StatusCode)
						}
						if content, done := readSSEEvent(t, bufio.NewScanner(resp.Body)); content != "first" || done {
							t.Fatalf("unexpected first event: %q, %t", content, done)
						}
					}
					return admissionRequest{resultCh, resp, cancel, gate, done}
				}
				reject := func(key string) {
					t.Helper()
					before := calls.Load()
					req, cancel, done := request(key)
					defer cancel()
					resp, err := gateway.Client().Do(req)
					if err != nil {
						t.Fatal(err)
					}
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					if resp.StatusCode != http.StatusTooManyRequests {
						t.Fatalf("expected 429, got %d", resp.StatusCode)
					}
					wait(done)
					if calls.Load() != before {
						t.Fatal("rejected request reached upstream")
					}
				}
				finish := func(req admissionRequest) {
					t.Helper()
					if ending == "cancel" {
						req.cancel()
					} else {
						close(req.finish)
					}
					if req.response != nil {
						_, err := io.Copy(io.Discard, req.response.Body)
						if ending == "success" && err != nil {
							t.Fatalf("successful SSE: %v", err)
						}
						if ending != "success" && err == nil {
							t.Fatal("expected interrupted SSE read")
						}
						req.response.Body.Close()
					}
					if !stream {
						select {
						case result := <-req.result:
							if ending == "cancel" {
								if !errors.Is(result.err, context.Canceled) {
									t.Fatalf("expected client cancellation, got %v", result.err)
								}
							} else {
								if result.err != nil {
									t.Fatal(result.err)
								}
								want := http.StatusOK
								if ending == "upstream failure" {
									want = http.StatusBadGateway
								}
								if result.resp.StatusCode != want {
									t.Errorf("expected status %d, got %d", want, result.resp.StatusCode)
								}
								if _, err := io.Copy(io.Discard, result.resp.Body); err != nil {
									t.Errorf("read ordinary response: %v", err)
								}
								result.resp.Body.Close()
							}
						case <-ctx.Done():
							t.Fatal("timed out waiting for ordinary response")
						}
					}
					wait(req.done)
				}
				a := start("key-a")
				reject("key-a") // Per-client rejection must not consume the remaining global slot.
				b := start("key-b")
				reject("key-a")
				reject("key-b")
				finish(a)
				a = start("key-a") // Exactly one slot is restored while B remains active.
				reject("key-a")
				reject("key-b") // Also detects an erroneous extra release affecting B.
				finish(a)
				finish(b)
				a = start("key-a")
				b = start("key-b") // Both slots are reusable after all previous requests end.
				finish(a)
				finish(b)
			})
		}
	}
}
