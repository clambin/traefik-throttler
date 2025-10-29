package traefik_throttler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"codeberg.org/clambin/ratelimiter"
)

const (
	clientThrottlersExpirationInterval = 30 * time.Second
	defaultCapacity                    = 50
	defaultRate                        = 10
	defaultLogLevel                    = "info"
	defaultLogFormat                   = "json"
)

// Config holds the plugin configuration.
type Config struct {
	Log      LogConfig `json:"log"`
	Rate     float64   `json:"rate"`
	Capacity int       `json:"capacity"`
}

type LogConfig struct {
	Level  string `json:"level"`
	Format string `json:"format"`
}

func (c Config) interval() time.Duration {
	rate := c.Rate
	if rate <= 0 {
		rate = 1.0
	}
	return time.Duration(float64(time.Second) / rate)
}

func (c Config) logger(w io.Writer) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(c.Log.Level)); err != nil {
		return nil, err
	}
	opts := slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	switch strings.ToLower(c.Log.Format) {
	case "json":
		h = slog.NewJSONHandler(w, &opts)
	default:
		h = slog.NewTextHandler(w, &opts)
	}
	return slog.New(h), nil
}

// CreateConfig creates the default plugin configuration.
func CreateConfig() *Config {
	return &Config{
		Rate:     defaultRate,
		Capacity: defaultCapacity,
		Log: LogConfig{
			Level:  defaultLogLevel,
			Format: defaultLogFormat,
		},
	}
}

// Throttler is a Traefik plugin that throttles requests for clients that generate too many 404 errors.
type Throttler struct {
	clientThrottlers map[string]*clientThrottler
	lock             sync.Mutex
	interval         time.Duration
	capacity         int
}

// New creates a new instance of the middleware.
func New(ctx context.Context, next http.Handler, config *Config, _ string) (http.Handler, error) {
	logger, err := config.logger(os.Stdout)
	if err != nil {
		return nil, fmt.Errorf("slog: %w", err)
	}
	t := Throttler{
		clientThrottlers: make(map[string]*clientThrottler),
		interval:         config.interval(),
		capacity:         config.Capacity,
	}
	go t.purgeExpiredClientThrottlers(ctx, logger)

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l := logger.With(
			slog.String("remoteAddr", r.RemoteAddr),
			slog.String("path", r.URL.Path),
		)
		l.Debug("throttler: new request")
		// start (or get) the clientThrottler for the current client.  We don't use r.Context() here to ensure that
		// the clientThrottler continues to fill the bucket even if the connection is closed.
		ct := t.getClientThrottler(ctx, r.RemoteAddr, l)
		// get a token from the clientThrottler.  If the token is not available, return a 429.
		if !ct.tryAcquire() {
			l.Warn("throttler: request throttled")
			http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
			return
		}
		// we're not throttled.  Perform the request.
		lw := loggingResponseWriter{ResponseWriter: w}
		next.ServeHTTP(&lw, r)
		// if the request was successful, restore the token so the rate limit will not apply.
		if lw.status != http.StatusNotFound {
			ct.restore()
		}
		l.Debug("throttler: request completed",
			slog.Int("statusCode", lw.status),
			slog.Int("tokens", ct.rateLimiter.TokenCount()),
		)
	})

	return h, nil
}

func (t *Throttler) getClientThrottler(ctx context.Context, remoteAddr string, logger *slog.Logger) *clientThrottler {
	t.lock.Lock()
	defer t.lock.Unlock()
	ct, ok := t.clientThrottlers[remoteAddr]
	if !ok || !ct.active() {
		logger.Debug("throttler: allocating new client throttler")
		ct = newClientThrottler(ctx, t.interval, t.capacity)
		t.clientThrottlers[remoteAddr] = ct
	}
	return ct
}

func (t *Throttler) purgeExpiredClientThrottlers(ctx context.Context, logger *slog.Logger) {
	ticker := time.NewTicker(clientThrottlersExpirationInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.lock.Lock()
			for host, ct := range t.clientThrottlers {
				if !ct.active() {
					logger.Debug("throttler: expired client", slog.String("host", host))
					delete(t.clientThrottlers, host)
					ct.cancel()
				}
			}
			t.lock.Unlock()
		}
	}
}

// clientThrottler is a rate limiter for a single client connection.
type clientThrottler struct {
	rateLimiter *ratelimiter.RateLimiter
	cancel      context.CancelFunc
	expiration  atomic.Value
}

func newClientThrottler(ctx context.Context, interval time.Duration, capacity int) *clientThrottler {
	subCtx, cancel := context.WithCancel(ctx)
	ct := clientThrottler{
		rateLimiter: ratelimiter.New(subCtx, interval, capacity),
		cancel:      cancel,
	}
	ct.markActive(true)
	go func() {
		// when the context is canceled (i.e., the client disconnects), mark the clientThrottler as inactive.
		<-subCtx.Done()
		ct.markActive(false)
	}()
	return &ct
}

func (ct *clientThrottler) tryAcquire() bool {
	if ct.rateLimiter.TryAcquire() {
		ct.markActive(true)
		return true
	}
	return false
}

func (ct *clientThrottler) restore() {
	ct.rateLimiter.Release()
}

func (ct *clientThrottler) active() bool {
	expiration, ok := ct.expiration.Load().(time.Time)
	return ok && time.Until(expiration) > 0
}

func (ct *clientThrottler) markActive(active bool) {
	var tm time.Time
	if active {
		tm = time.Now().Add(clientThrottlersExpirationInterval)
	}
	ct.expiration.Store(tm)
}

var _ http.ResponseWriter = &loggingResponseWriter{}

// loggingResponseWriter is an http.ResponseWriter that records the status code.
type loggingResponseWriter struct {
	http.ResponseWriter
	status int
}

func (r *loggingResponseWriter) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
