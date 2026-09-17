package ratelimit

import (
	"sync"
	"time"
)

type Limiter interface {
	Allow(tokens int) bool
}

type TokenBucket struct {
	mu            sync.Mutex
	lastRefill    time.Time
	tokenPerSec   int
	capacity      int
	currentTokens int
}

func NewTokenBucket(tokenPerSec, capacity int) *TokenBucket {
	return &TokenBucket{
		lastRefill:    time.Now(),
		tokenPerSec:   tokenPerSec,
		capacity:      capacity,
		currentTokens: capacity,
	}
}

func (rl *TokenBucket) Allow(tokens int) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	duration := time.Since(rl.lastRefill).Seconds()
	rl.currentTokens = min(rl.capacity, rl.currentTokens+int(duration)*rl.tokenPerSec)
	rl.lastRefill = time.Now()
	if rl.currentTokens >= tokens {
		rl.currentTokens -= tokens
		return true
	}
	return false
}
