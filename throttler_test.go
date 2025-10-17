package traefik_throttler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"
)

func TestThrottler(t *testing.T) {
	ctx := t.Context()
	h := handler(http.StatusNotFound)

	const tokens = 5
	cfg := CreateConfig()
	cfg.Rate = time.Second
	cfg.Capacity = tokens

	throttler, err := New(ctx, h, cfg, "")
	if err != nil {
		t.Fatal(err)
	}

	// consume all tokens for one client
	for range tokens {
		if code := send(ctx, throttler, "127.0.0.1:12345"); code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", code)
		}
	}
	// the next request should fail
	if code := send(ctx, throttler, "127.0.0.1:12345"); code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", code)
	}
	// different client should be able to make requests
	if code := send(ctx, throttler, "127.0.0.1:12346"); code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", code)
	}
}

func TestThrottler_NoNotFound(t *testing.T) {
	ctx := t.Context()
	h := handler(http.StatusOK)

	const tokens = 5
	throttler, err := New(ctx, h, &Config{Capacity: 5, Rate: time.Second}, "")
	if err != nil {
		t.Fatal(err)
	}
	// status OK does not trigger 429
	for range 20 * tokens {
		if code := send(ctx, throttler, "127.0.0.1:12345"); code != http.StatusOK {
			t.Fatalf("expected 404, got %d", code)
		}
	}
}

func TestThrottler_Refill(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		h := handler(http.StatusNotFound)
		const (
			tokens             = 5
			expirationInterval = time.Second
		)

		throttler, err := New(ctx, h, &Config{Capacity: 5, Rate: expirationInterval}, "")
		if err != nil {
			t.Fatal(err)
		}

		// consume all tokens for one client
		for range tokens {
			if code := send(ctx, throttler, "127.0.0.1:12345"); code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d", code)
			}
		}
		// the next request should fail
		if code := send(ctx, throttler, "127.0.0.1:12345"); code != http.StatusTooManyRequests {
			t.Fatalf("expected 429, got %d", code)
		}
		// wait for tokens to be refilled
		time.Sleep(2 * expirationInterval)
		// the next request should succeed
		if code := send(ctx, throttler, "127.0.0.1:12345"); code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", code)
		}
	})
}

func TestThrottler_Expiration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		h := handler(http.StatusNotFound)

		const (
			tokens             = 5
			expirationInterval = time.Second
		)

		throttler, err := New(ctx, h, &Config{Capacity: 5, Rate: expirationInterval}, "")
		if err != nil {
			t.Fatal(err)
		}

		// consume all tokens for one client
		for range tokens {
			if code := send(ctx, throttler, "127.0.0.1:12345"); code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d", code)
			}
		}
		// the next request should fail
		if code := send(ctx, throttler, "127.0.0.1:12345"); code != http.StatusTooManyRequests {
			t.Fatalf("expected 429, got %d", code)
		}
		// wait for hostThrottler to expire
		time.Sleep(10 * time.Minute)
		// the next request should succeed
		if code := send(ctx, throttler, "127.0.0.1:12345"); code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", code)
		}
	})
}

func send(ctx context.Context, h http.Handler, clientAddr string) int {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost:8080", nil)
	req.RemoteAddr = clientAddr
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)
	return resp.Code
}

func handler(code int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
	})
}

func Test_rateLimiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const tokens = 10
		ctx := t.Context()
		b := newRateLimiter(ctx, tokens, time.Second)
		// acquire all available tokens
		for range tokens {
			if !b.acquire() {
				t.Fatal("expected no error")
			}
		}
		// acquire one more token, should fail
		if b.acquire() {
			t.Fatal("expected error")
		}
		// wait for tokens to be refilled
		time.Sleep(2 * time.Second)
		// acquire one more token, should succeed
		if !b.acquire() {
			t.Fatal("expected no error")
		}
	})
}

func BenchmarkThrottler(b *testing.B) {
	b.ReportAllocs()
	h := handler(http.StatusNotFound)
	ctx := b.Context()
	throttler, err := New(ctx, h, &Config{Capacity: 1000, Rate: time.Second}, "")
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		send(ctx, throttler, "127.0.0.1:12345")
	}
}
