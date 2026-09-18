package ratelimit

import (
	"fmt"
	"hash/maphash"
	"sync"
	"sync/atomic"
	"testing"
)

func TestLimiter(t *testing.T) {
	t.Run("normal initial map", func(t *testing.T) {
		gcl := NewGatewayConcurrencyLimiter(4, 2)
		if !gcl.TryAcquire("a") {
			t.Fatal("expected the first acquisition to succeed")
		}
	})

	t.Run("client reach limit", func(t *testing.T) {
		gcl := NewGatewayConcurrencyLimiter(4, 2)
		for i := 0; i < 2; i++ {
			if !gcl.TryAcquire("a") {
				t.Fatalf("expected client a acquisition %d to succeed", i+1)
			}
		}
		if gcl.TryAcquire("a") {
			t.Fatal("expected client a to be rejected after reaching its limit")
		}
	})

	t.Run("different client", func(t *testing.T) {
		gcl := NewGatewayConcurrencyLimiter(4, 2)
		for i := 0; i < 2; i++ {
			if !gcl.TryAcquire("a") {
				t.Fatalf("expected client a acquisition %d to succeed", i+1)
			}
		}
		if !gcl.TryAcquire("b") {
			t.Fatal("expected client b not to share client a's limit")
		}
	})

	t.Run("different client reach global limit", func(t *testing.T) {
		gcl := NewGatewayConcurrencyLimiter(4, 2)
		for i := 0; i < 2; i++ {
			if !gcl.TryAcquire("a") || !gcl.TryAcquire("b") {
				t.Fatalf("expected acquisition round %d to succeed", i+1)
			}
		}
		if gcl.TryAcquire("c") {
			t.Fatal("expected a new client to be rejected after reaching the global limit")
		}
	})

	t.Run("client rejection rolls back global limit", func(t *testing.T) {
		gcl := NewGatewayConcurrencyLimiter(3, 1)
		if !gcl.TryAcquire("a") {
			t.Fatal("expected client a's first acquisition to succeed")
		}
		if gcl.TryAcquire("a") {
			t.Fatal("expected client a's second acquisition to reach the per-client limit")
		}
		if !gcl.TryAcquire("b") || !gcl.TryAcquire("c") {
			t.Fatal("expected the rejected client acquisition not to consume global capacity")
		}
		if gcl.TryAcquire("d") {
			t.Fatal("expected the global limit to reject client d")
		}
	})

	t.Run("release work normal", func(t *testing.T) {
		gcl := NewGatewayConcurrencyLimiter(4, 2)
		for i := 0; i < 2; i++ {
			if !gcl.TryAcquire("a") || !gcl.TryAcquire("b") {
				t.Fatalf("expected acquisition round %d to succeed", i+1)
			}
		}
		if gcl.TryAcquire("a") {
			t.Fatal("expected acquisition to fail while all capacity is occupied")
		}
		gcl.Release("a")
		if !gcl.TryAcquire("a") {
			t.Fatal("expected released capacity to be available again")
		}
	})

	t.Run("release deletes idle client", func(t *testing.T) {
		gcl := NewGatewayConcurrencyLimiter(4, 2)
		if !gcl.TryAcquire("a") {
			t.Fatal("expected acquisition to succeed")
		}
		gcl.Release("a")

		idx := maphash.String(gcl.seed, "a") % uint64(len(gcl.shard))
		shard := gcl.shard[idx]
		shard.mu.Lock()
		_, exists := shard.clientConcurrency["a"]
		shard.mu.Unlock()
		if exists {
			t.Fatal("expected an idle client to be deleted from its shard")
		}
	})

	t.Run("concurrency limit doesn't pass the limit", func(t *testing.T) {
		var successAcquire atomic.Uint32
		gcl := NewGatewayConcurrencyLimiter(4, 2)
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(num int) {
				defer wg.Done()
				clientID := fmt.Sprintf("%d-key", num)
				if gcl.TryAcquire(clientID) {
					successAcquire.Add(1)
				}
			}(i)
		}
		wg.Wait()
		if got := int32(successAcquire.Load()); got != gcl.maxConcurrency {
			t.Fatalf("expected exactly %d successful acquisitions, got %d", gcl.maxConcurrency, got)
		}
	})

	t.Run("same client concurrency doesn't pass client limit", func(t *testing.T) {
		var successAcquire atomic.Uint32
		gcl := NewGatewayConcurrencyLimiter(100, 2)
		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if gcl.TryAcquire("a") {
					successAcquire.Add(1)
				}
			}()
		}
		wg.Wait()
		if got := successAcquire.Load(); got != uint32(gcl.maxConcurrencyPerClient) {
			t.Fatalf("expected exactly %d successful acquisitions for client a, got %d", gcl.maxConcurrencyPerClient, got)
		}
	})

	t.Run("concurrency limit back to zero after test", func(t *testing.T) {
		gcl := NewGatewayConcurrencyLimiter(4, 2)
		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func(num int) {
				defer wg.Done()
				clientID := fmt.Sprintf("%d-key", num)
				if gcl.TryAcquire(clientID) {
					gcl.Release(clientID)
				}
			}(i)
		}
		wg.Wait()
		if got := gcl.curConcurrency.Load(); got != 0 {
			t.Fatalf("expected global concurrency to return to zero, got %d", got)
		}
		for i, shard := range gcl.shard {
			shard.mu.Lock()
			clientCount := len(shard.clientConcurrency)
			shard.mu.Unlock()
			if clientCount != 0 {
				t.Fatalf("expected shard %d to contain no idle clients, got %d", i, clientCount)
			}
		}
	})
}
