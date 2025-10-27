package traefik_throttler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"
)

func TestConfig_interval(t *testing.T) {
	tests := []struct {
		rate float64
		want time.Duration
	}{
		{rate: 0, want: time.Second}, // default: 1 tps
		{rate: .1, want: 10 * time.Second},
		{rate: 1, want: time.Second},
		{rate: 1_000, want: 1 * time.Millisecond},
		{rate: 1_000_000, want: time.Microsecond},
		{rate: 1_000_000_000, want: time.Nanosecond},
	}
	for _, tt := range tests {
		cfg := &Config{Rate: tt.rate}
		if got := cfg.interval(); got != tt.want {
			t.Errorf("interval() = %v, want %v", got, tt.want)
		}
	}
}

func TestConfig_logger(t *testing.T) {
	tests := []struct {
		name    string
		cfg     LogConfig
		wantErr bool
	}{
		{"json - valid", LogConfig{Level: "error", Format: "json"}, true},
		{"text - valid", LogConfig{Level: "error", Format: "text"}, true},
		{"invalid level", LogConfig{Level: "invalid"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Config{Log: tt.cfg}.logger(io.Discard)
			if (err != nil) == tt.wantErr {
				t.Errorf("logger() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestThrottler(t *testing.T) {
	ctx := t.Context()
	h := handler(http.StatusNotFound)

	const tokens = 5
	throttler, err := New(ctx, h, config(tokens, 0.1, "debug"), "")
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
	throttler, err := New(ctx, h, &Config{Capacity: 5, Rate: 1, Log: LogConfig{Level: "error"}}, "")
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

		throttler, err := New(ctx, h, &Config{Capacity: 5, Rate: 1 / expirationInterval.Seconds(), Log: LogConfig{Level: "error"}}, "")
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

		throttler, err := New(ctx, h, config(tokens, 1/expirationInterval.Seconds(), ""), "")
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
	throttler, err := New(ctx, h, config(10_000, 1, "error"), "")
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		send(ctx, throttler, "127.0.0.1:12345")
	}
}

func config(capacity int, rate float64, level string) *Config {
	cfg := CreateConfig()
	cfg.Rate = rate
	cfg.Capacity = capacity
	if level != "" {
		cfg.Log.Level = level
	}
	return cfg
}

func send(ctx context.Context, h http.Handler, clientAddr string) int {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost:8080/", nil)
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
