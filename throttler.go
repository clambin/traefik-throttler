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
	"time"
)

const (
	expireClientThrottlersInterval = 5 * time.Minute
	defaultCapacity                = 50
	defaultRate                    = 10
	defaultLogLevel                = "info"
	defaultLogFormat               = "json"
)

// Config holds the plugin configuration.
type Config struct {
	Rate     float64   `json:"rate"`
	Capacity int       `json:"capacity"`
	Log      LogConfig `json:"log"`
}

type LogConfig struct {
	Level  string `json:"level"`
	Format string `json:"format"`
}

func (c Config) interval() time.Duration {
	if c.Rate == 0 {
		c.Rate = 1.0
	}
	return time.Duration(float64(time.Second) / c.Rate)
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

// Throttler is a Traefik middleware that throttles requests for clients that generate too many 404 errors.
type Throttler struct {
	logger   *slog.Logger
	hosts    map[string]*clientThrottler
	lock     sync.Mutex
	interval time.Duration
	capacity int
}

type clientThrottler struct {
	rateLimiter *rateLimiter
	cancel      context.CancelFunc
	lastWritten time.Time
}

// New creates a new instance of the middleware.
func New(ctx context.Context, next http.Handler, config *Config, _ string) (http.Handler, error) {
	logger, err := config.logger(os.Stdout)
	if err != nil {
		return nil, fmt.Errorf("slog: %w", err)
	}
	t := Throttler{
		logger:   logger,
		hosts:    make(map[string]*clientThrottler),
		interval: config.interval(),
		capacity: config.Capacity,
	}
	go t.purgeExpiredClientThrottlers(ctx)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l := t.logger.With(
			slog.String("remoteAddr", r.RemoteAddr),
			slog.String("path", r.URL.Path),
		)
		l.Debug("throttler: new request")
		b := t.getHostThrottler(ctx, r.RemoteAddr)
		if !b.rateLimiter.acquire() {
			l.Warn("throttler: request throttled")
			http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
			return
		}
		lw := logWriterRecorder{ResponseWriter: w}
		next.ServeHTTP(&lw, r)
		if lw.status != http.StatusNotFound {
			b.rateLimiter.release()
		}
		l.Debug("throttler: request completed",
			slog.Int("statusCode", lw.status),
			slog.Int("tokens", b.rateLimiter.tokenCount()),
		)
	}), nil
}

func (t *Throttler) getHostThrottler(ctx context.Context, host string) *clientThrottler {
	t.lock.Lock()
	defer t.lock.Unlock()
	b, ok := t.hosts[host]
	if !ok {
		t.logger.Debug("throttler: new client", slog.String("host", host))
		subCtx, cancel := context.WithCancel(ctx)
		b = &clientThrottler{
			rateLimiter: newRateLimiter(subCtx, t.capacity, t.interval),
			cancel:      cancel,
		}
		t.hosts[host] = b
	}
	b.lastWritten = time.Now()
	return b
}

func (t *Throttler) purgeExpiredClientThrottlers(ctx context.Context) {
	ticker := time.NewTicker(expireClientThrottlersInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.lock.Lock()
			for host, b := range t.hosts {
				if time.Since(b.lastWritten) > expireClientThrottlersInterval {
					t.logger.Debug("throttler: expired client", slog.String("host", host))
					b.cancel()
					delete(t.hosts, host)
				}
			}
			t.lock.Unlock()
		}
	}
}

var _ http.ResponseWriter = &logWriterRecorder{}

// logWriterRecorder is an http.ResponseWriter that records the status code.
type logWriterRecorder struct {
	http.ResponseWriter
	status int
}

func (r *logWriterRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// rateLimiter is a token bucket rate limiter.
// Unlike a normal token bucket, it does not block when the bucket is empty, as we only use it to
// decide whether to throttle requests.
type rateLimiter struct {
	lock     sync.Mutex
	tokens   int
	capacity int
}

func newRateLimiter(ctx context.Context, capacity int, interval time.Duration) *rateLimiter {
	rl := rateLimiter{tokens: capacity, capacity: capacity}
	go rl.refill(ctx, interval)
	return &rl
}

func (r *rateLimiter) refill(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.release()
		}
	}
}

func (r *rateLimiter) acquire() bool {
	r.lock.Lock()
	defer r.lock.Unlock()
	if r.tokens == 0 {
		return false
	}
	r.tokens--
	return true
}

func (r *rateLimiter) release() {
	r.lock.Lock()
	defer r.lock.Unlock()
	if r.tokens < r.capacity {
		r.tokens++
	}
}

func (r *rateLimiter) tokenCount() int {
	r.lock.Lock()
	defer r.lock.Unlock()
	return r.tokens
}
