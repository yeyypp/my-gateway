package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shuai/gateway/internal/ratelimit"
)

func TestConcurrencyGlobalLimitAndRecovery(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	handler := ConcurrencyLimitWithLimits(1, 2)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request := func(client string) int {
		req := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(context.WithValue(ctx, ClientID, client))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Code
	}
	done := make(chan int, 1)
	go func() { done <- request("a") }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first request did not enter")
	}
	// Both callers have unused per-client capacity, so only the global limit can reject.
	for _, client := range []string{"a", "b"} {
		if got := request(client); got != http.StatusTooManyRequests {
			t.Fatalf("client %s: expected global rejection, got %d", client, got)
		}
	}
	close(release)
	select {
	case got := <-done:
		if got != http.StatusNoContent {
			t.Fatalf("first status = %d", got)
		}
	case <-ctx.Done():
		t.Fatal("first request did not finish")
	}
	if got := request("b"); got != http.StatusNoContent {
		t.Fatalf("global capacity did not recover: %d", got)
	}
}

// Changing the deterministic rate budget removes wall-clock refill from release assertions.
func TestConcurrencyReleaseAfterDownstreamRejection(t *testing.T) {
	for _, reason := range []string{"rate limit", "invalid JSON"} {
		t.Run(reason, func(t *testing.T) {
			t.Setenv("LOCAL_CLIENT_KEY_A", "key-a")
			budget := &fixedBudgetLimiter{remaining: 100}
			if reason == "rate limit" {
				budget.remaining = 0
			}
			calls := 0
			handler := Chain(SetGatewayRequestID, Authorization, ConcurrencyLimitWithLimits(1, 1),
				rateLimitWithFactory(func() ratelimit.Limiter { return budget }), ValidateJSONRequest)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(http.StatusNoContent)
			}))
			request := func(body string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer key-a")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, req)
				return w
			}
			valid := `{"model":"test","messages":[{"role":"user","content":"test"}]}`
			for range 3 {
				body, want := valid, http.StatusTooManyRequests
				if reason == "invalid JSON" {
					body, want = `{`, http.StatusBadRequest
				}
				w := request(body)
				if w.Code != want {
					t.Fatalf("expected %d, got %d", want, w.Code)
				}
				// A rate rejection must really reach RateLimit, not be masked by a leaked concurrency slot.
				if reason == "rate limit" && !strings.Contains(w.Body.String(), `"code":"rate_limit_exceeded"`) {
					t.Fatalf("unexpected rejection: %s", w.Body.String())
				}
			}
			if calls != 0 {
				t.Fatalf("rejected requests reached downstream %d times", calls)
			}
			budget.mu.Lock()
			budget.remaining = 1
			budget.mu.Unlock()
			if w := request(valid); w.Code != http.StatusNoContent {
				t.Fatalf("capacity did not recover: %d %s", w.Code, w.Body.String())
			}
			if calls != 1 {
				t.Fatalf("expected exactly one downstream call, got %d", calls)
			}
		})
	}
}

func TestConcurrencyReleaseDoesNotReleaseAnotherRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entered := make(chan struct{}, 3)
	gates := make(chan chan struct{}, 3)
	handler := ConcurrencyLimitWithLimits(2, 2)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		var gate chan struct{}
		select {
		case gate = <-gates:
		case <-ctx.Done():
			return
		}
		select {
		case <-gate:
		case <-ctx.Done():
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	request := func() int {
		req := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(context.WithValue(ctx, ClientID, "a"))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Code
	}
	start := func() (chan struct{}, chan int) {
		t.Helper()
		gate, done := make(chan struct{}), make(chan int, 1)
		gates <- gate
		go func() { done <- request() }()
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("request did not enter")
		}
		return gate, done
	}
	finish := func(gate chan struct{}, done chan int) {
		t.Helper()
		close(gate)
		select {
		case status := <-done:
			if status != http.StatusNoContent {
				t.Fatalf("status = %d", status)
			}
		case <-ctx.Done():
			t.Fatal("request did not finish")
		}
	}
	first, firstDone := start()
	second, secondDone := start()
	finish(first, firstDone)
	third, thirdDone := start()
	// A duplicate release of the first request would incorrectly free the second's slot.
	if got := request(); got != http.StatusTooManyRequests {
		t.Fatalf("expected second and third requests to fill capacity, got %d", got)
	}
	finish(second, secondDone)
	finish(third, thirdDone)
}
