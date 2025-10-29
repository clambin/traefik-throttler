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
		name     string
		cfg      LogConfig
		wantPass bool
	}{
		{"json - valid", LogConfig{Level: "error", Format: "json"}, true},
		{"text - valid", LogConfig{Level: "error", Format: "text"}, true},
		{"invalid level", LogConfig{Level: "invalid"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Config{Log: tt.cfg}.logger(io.Discard)
			if (err != nil) == tt.wantPass {
				t.Errorf("logger() error = %v, wantPass %v", err, tt.wantPass)
			}
		})
	}
}

func TestThrottler(t *testing.T) {
	ctx := t.Context()
	h := handler(http.StatusNotFound)

	const tokens = 5
	throttler, err := New(ctx, h, config(tokens, 0.1, "error"), "")
	if err != nil {
		t.Fatal(err)
	}

	// consume all tokens for one client
	for range tokens {
		expectStatus(t, http.StatusNotFound, send(ctx, throttler, "127.0.0.1:12345"))
	}
	// the next request should fail
	expectStatus(t, http.StatusTooManyRequests, send(ctx, throttler, "127.0.0.1:12345"))

	// different client should be able to make requests
	expectStatus(t, http.StatusNotFound, send(ctx, throttler, "127.0.0.1:12346"))
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
		expectStatus(t, http.StatusOK, send(ctx, throttler, "127.0.0.1:12345"))
	}
}

func TestThrottler_Refill(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		h := handler(http.StatusNotFound)
		const (
			tokens   = 5
			interval = time.Second
		)
		throttler, err := New(ctx, h, &Config{Capacity: 5, Rate: 1 / interval.Seconds(), Log: LogConfig{Level: "error"}}, "")
		if err != nil {
			t.Fatal(err)
		}
		// consume all tokens for one client
		for range tokens {
			expectStatus(t, http.StatusNotFound, send(ctx, throttler, "127.0.0.1:12345"))
		}
		// the next request should fail
		expectStatus(t, http.StatusTooManyRequests, send(ctx, throttler, "127.0.0.1:12345"))
		// wait for tokens to be refilled
		time.Sleep(2 * interval)
		// the next request should succeed
		expectStatus(t, http.StatusNotFound, send(ctx, throttler, "127.0.0.1:12345"))
	})
}

func TestThrottler_Expiration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		h := handler(http.StatusNotFound)
		const (
			tokens   = 5
			interval = time.Second
		)
		throttler, err := New(ctx, h, config(tokens, 1/interval.Seconds(), "error"), "")
		if err != nil {
			t.Fatal(err)
		}

		// consume all tokens for one client
		for range tokens {
			expectStatus(t, http.StatusNotFound, send(ctx, throttler, "127.0.0.1:12345"))
		}
		// the next request should fail
		expectStatus(t, http.StatusTooManyRequests, send(ctx, throttler, "127.0.0.1:12345"))
		// wait for hostThrottler to expire
		time.Sleep(2 * clientThrottlersExpirationInterval)
		// the next request should succeed
		expectStatus(t, http.StatusNotFound, send(ctx, throttler, "127.0.0.1:12345"))
	})
}

func TestThrottler_ReUse_ClientAddr(t *testing.T) {
	ctx := t.Context()
	h := handler(http.StatusNotFound)
	const tokens = 5
	throttler, err := New(ctx, h, config(tokens, 10, "error"), "")
	if err != nil {
		t.Fatal(err)
	}

	// subContext to simulate a new connection from the same clientAddr
	subCtx, cancel := context.WithCancel(ctx)
	// consume all tokens for one client
	for range tokens {
		expectStatus(t, http.StatusNotFound, send(subCtx, throttler, "127.0.0.1:12345"))
	}
	// the next request should fail
	expectStatus(t, http.StatusTooManyRequests, send(subCtx, throttler, "127.0.0.1:12345"))
	// connection closed.
	cancel()
	// new connection: should not be throttled
	// may take some time for the previous clientThrottler to process the cancellation, so try until we succeed.
	// it should take less than clientThrottlersExpirationInterval.
	subCtx, cancel = context.WithCancel(ctx)
	t.Cleanup(cancel)
	for {
		if code := send(subCtx, throttler, "127.0.0.1:12345"); code == http.StatusNotFound {
			break
		}
		t.Log("waiting for clientThrottler to expire")
		time.Sleep(100 * time.Millisecond)
	}
}

func TestClientThrottler_Active(t *testing.T) {
	ctx := context.Background()
	ct := newClientThrottler(ctx, time.Second, 1)

	if !ct.active() {
		t.Fatal("expected active immediately after creation")
	}

	ct.markActive(false)
	if ct.active() {
		t.Fatal("expected inactive after markActive(false)")
	}
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

func handler(code int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
	})
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

func expectStatus(tb testing.TB, want, got int) {
	tb.Helper()
	if got != want {
		tb.Fatalf("expected %d, got %d", want, got)
	}
}
