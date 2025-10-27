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

	"codeberg.org/clambin/ratelimiter"
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
	Log      LogConfig `json:"log"`
	Rate     float64   `json:"rate"`
	Capacity int       `json:"capacity"`
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

// Throttler is a Traefik plugin that throttles requests for clients that generate too many 404 errors.
type Throttler struct {
	logger           *slog.Logger
	clientThrottlers map[string]*clientThrottler
	lock             sync.Mutex
	interval         time.Duration
	capacity         int
}

// clientThrottler is a rate limiter for a single client.
type clientThrottler struct {
	rateLimiter *ratelimiter.RateLimiter
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
		logger:           logger,
		clientThrottlers: make(map[string]*clientThrottler),
		interval:         config.interval(),
		capacity:         config.Capacity,
	}
	go t.purgeExpiredClientThrottlers(ctx)

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l := t.logger.With(
			slog.String("remoteAddr", r.RemoteAddr),
			slog.String("path", r.URL.Path),
		)
		l.Debug("throttler: new request")
		// start (or get) the clientThrottler for the current client.  We don't use r.Context() here to ensure that
		// the clientThrottler continues to fill the bucket even if the connection is closed.
		ct := t.getClientThrottler(ctx, r.RemoteAddr)
		// get a token from the clientThrottler.  If the token is not available, return a 429.
		if !ct.rateLimiter.TryAcquire() {
			l.Warn("throttler: request throttled")
			http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
			return
		}
		// we're not throttled.  Perform the request.
		lw := logWriterRecorder{ResponseWriter: w}
		next.ServeHTTP(&lw, r)
		// if the request was successful, release the token.
		if lw.status != http.StatusNotFound {
			ct.rateLimiter.Release()
		}
		l.Debug("throttler: request completed",
			slog.Int("statusCode", lw.status),
			slog.Int("tokens", ct.rateLimiter.TokenCount()),
		)
	})

	return h, nil
}

func (t *Throttler) getClientThrottler(ctx context.Context, remoteAddr string) *clientThrottler {
	t.lock.Lock()
	defer t.lock.Unlock()
	ct, ok := t.clientThrottlers[remoteAddr]
	if !ok {
		t.logger.Debug("throttler: new client", slog.String("remoteAddr", remoteAddr))
		subCtx, cancel := context.WithCancel(ctx)
		ct = &clientThrottler{
			rateLimiter: ratelimiter.New(subCtx, t.interval, t.capacity),
			cancel:      cancel,
		}
		t.clientThrottlers[remoteAddr] = ct
	}
	ct.lastWritten = time.Now()
	return ct
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
			for host, ct := range t.clientThrottlers {
				if time.Since(ct.lastWritten) > expireClientThrottlersInterval {
					t.logger.Debug("throttler: expired client", slog.String("host", host))
					ct.cancel()
					delete(t.clientThrottlers, host)
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
