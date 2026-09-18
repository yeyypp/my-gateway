package ratelimit

import (
	"hash/maphash"
	"sync"
	"sync/atomic"
)

type concurrencyShard struct {
	mu                sync.Mutex
	clientConcurrency map[string]int32
}

type GatewayConcurrencyLimiter struct {
	seed                    maphash.Seed
	curConcurrency          atomic.Int32
	maxConcurrency          int32
	maxConcurrencyPerClient int32
	shard                   [256]*concurrencyShard
}

func NewGatewayConcurrencyLimiter(maxConcurrency, maxConcurrencyPerClient int32) *GatewayConcurrencyLimiter {
	shard := [256]*concurrencyShard{}
	for i := 0; i < len(shard); i++ {
		shard[i] = new(concurrencyShard)
	}
	return &GatewayConcurrencyLimiter{
		seed:                    maphash.MakeSeed(),
		maxConcurrency:          maxConcurrency,
		maxConcurrencyPerClient: maxConcurrencyPerClient,
		shard:                   shard,
	}
}

func (gcl *GatewayConcurrencyLimiter) TryAcquire(clientID string) bool {
	cur := gcl.curConcurrency.Load()
	if cur+1 > gcl.maxConcurrency {
		return false
	}

	for !gcl.curConcurrency.CompareAndSwap(cur, cur+1) {
		cur = gcl.curConcurrency.Load()
		if cur+1 > gcl.maxConcurrency {
			return false
		}
	}

	idx := maphash.String(gcl.seed, clientID) % uint64(len(gcl.shard))
	shard := gcl.shard[idx]

	shard.mu.Lock()
	defer shard.mu.Unlock()
	if shard.clientConcurrency == nil {
		shard.clientConcurrency = make(map[string]int32)
	}

	clientCur := shard.clientConcurrency[clientID]
	if clientCur+1 > gcl.maxConcurrencyPerClient {
		gcl.curConcurrency.Add(-1)
		return false
	}
	shard.clientConcurrency[clientID] = clientCur + 1
	return true
}

func (gcl *GatewayConcurrencyLimiter) Release(clientID string) {
	idx := maphash.String(gcl.seed, clientID) % uint64(len(gcl.shard))
	shard := gcl.shard[idx]

	shard.mu.Lock()
	defer shard.mu.Unlock()
	if shard.clientConcurrency == nil {
		return
	}

	clientCur := shard.clientConcurrency[clientID]
	if clientCur == 0 {
		return
	}
	shard.clientConcurrency[clientID] = clientCur - 1
	if clientCur-1 == 0 {
		delete(shard.clientConcurrency, clientID)
	}

	gcl.curConcurrency.Add(-1)
}
