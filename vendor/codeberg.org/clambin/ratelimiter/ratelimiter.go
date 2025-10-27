package ratelimiter

import (
	"context"
	"time"
)

// RateLimiter is a token bucket rate limiter.
type RateLimiter struct {
	tokens chan struct{}
}

// New returns a new RateLimiter.
// Tokens are refilled at the given interval, up to the given capacity until the context is canceled.
func New(ctx context.Context, interval time.Duration, capacity int) *RateLimiter {
	rl := &RateLimiter{
		tokens: make(chan struct{}, capacity),
	}

	// Fill the bucket initially
	for i := 0; i < capacity; i++ {
		rl.tokens <- struct{}{}
	}

	// Refill tokens at interval rate
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				rl.Release()
			}
		}
	}()

	return rl
}

// Acquire blocks until a token is available, or the context is canceled.
func (rl *RateLimiter) Acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-rl.tokens:
		return nil
	}
}

// TryAcquire returns true if a token is acquired, false otherwise.
func (rl *RateLimiter) TryAcquire() bool {
	select {
	case <-rl.tokens:
		return true
	default:
		return false
	}
}

// Release returns a token to the bucket.
func (rl *RateLimiter) Release() {
	select {
	case rl.tokens <- struct{}{}:
	default:
	}
}

// TokenCount returns the number of tokens in the bucket.
func (rl *RateLimiter) TokenCount() int {
	return len(rl.tokens)
}
