package traefik_throttler

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestRateLimiter_Acquire(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := NewRateLimiter(t.Context(), time.Hour, 1)

		// acquire one token. This should succeed.
		ctx, cancel := context.WithTimeout(t.Context(), 1*time.Second)
		defer cancel()
		expectSuccess(t, b.Acquire(ctx))
		expectEqual(t, 0, b.TokenCount())

		// acquire one token. This should fail.
		ctx, cancel = context.WithTimeout(t.Context(), 1*time.Second)
		defer cancel()
		expectError(t, b.Acquire(ctx))

		// release a token. the next call to Acquire should succeed.
		b.Release()
		expectEqual(t, 1, b.TokenCount())

		// acquire one token. This should succeed.
		ctx, cancel = context.WithTimeout(t.Context(), 1*time.Second)
		defer cancel()
		expectSuccess(t, b.Acquire(ctx))
		expectEqual(t, 0, b.TokenCount())

		// Release never blocks
		b.Release()
		b.Release()
		b.Release()
	})
}

func TestRateLimiter_TryAcquire(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const tokens = 10
		b := NewRateLimiter(t.Context(), time.Second, tokens)
		// acquire all available tokens
		for range tokens {
			expectEqual(t, true, b.TryAcquire())
		}
		// acquire one more token, should fail
		expectEqual(t, false, b.TryAcquire())
		// wait for tokens to be refilled
		for b.TokenCount() == 0 {
			time.Sleep(time.Second)
		}
		// acquire one more token, should succeed
		expectEqual(t, true, b.TryAcquire())
	})
}

func expectSuccess(tb testing.TB, err error) {
	tb.Helper()
	if err != nil {
		tb.Fatalf("expected no error, got %v", err)
	}
}

func expectError(tb testing.TB, err error) {
	tb.Helper()
	if err == nil {
		tb.Fatal("expected error")
	}
}

func expectEqual(tb testing.TB, expected, actual any) {
	tb.Helper()
	if expected != actual {
		tb.Fatalf("expected %v, got %v", expected, actual)
	}
}
