package ratelimit

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenBucketExhaustsWithoutRefill(t *testing.T) {
	limiter := NewTokenBucket(0, 2)
	want := []bool{true, true, false}

	for i, wantAllowed := range want {
		if gotAllowed := limiter.Allow(1); gotAllowed != wantAllowed {
			t.Fatalf("request %d: expected allowed=%t, got %t", i+1, wantAllowed, gotAllowed)
		}
	}
}

func TestTokenBucketRefillsWithoutExceedingCapacity(t *testing.T) {
	limiter := NewTokenBucket(1, 2)
	limiter.currentTokens = 0
	limiter.lastRefill = time.Now().Add(-10 * time.Second)

	if limiter.Allow(3) {
		t.Fatal("expected a refill not to exceed the bucket capacity")
	}
	if !limiter.Allow(2) {
		t.Fatal("expected elapsed time to refill the bucket to capacity")
	}
}

func TestTokenBucketConcurrentAllowDoesNotExceedCapacity(t *testing.T) {
	const (
		capacity = 16
		attempts = 128
	)
	limiter := NewTokenBucket(0, capacity)

	var allowed atomic.Int64
	var wg sync.WaitGroup
	wg.Add(attempts)
	for range attempts {
		go func() {
			defer wg.Done()
			if limiter.Allow(1) {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := allowed.Load(); got != capacity {
		t.Fatalf("expected exactly %d allowed requests, got %d", capacity, got)
	}
}
