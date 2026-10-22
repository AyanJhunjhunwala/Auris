// Package httpx is the one place Auris talks HTTP to upstream providers. It
// layers, in order: response cache -> per-source rate limiter -> request with
// timeout -> bounded retry on 429/5xx -> telemetry. Source adapters only ever
// call GetJSON.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/ayanjhunjhunwala/auris/backend/internal/cache"
	"github.com/ayanjhunjhunwala/auris/backend/internal/telemetry"
)

// StatusError is returned for non-2xx responses after retries.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("http %d: %s", e.Code, e.Body)
	}
	return fmt.Sprintf("http %d", e.Code)
}

// Client is shared by all sources. Each source gets its own limiter.
type Client struct {
	http      *http.Client
	userAgent string
	cache     *cache.LRU[[]byte]
	tel       *telemetry.Recorder
	limiters  map[string]*Limiter
	retries   int

	mu       sync.Mutex
	inflight map[string]*flight
}

// flight is one upstream fetch that concurrent identical requests share.
type flight struct {
	done chan struct{}
	body []byte
	err  error
}

func New(userAgent string, c *cache.LRU[[]byte], tel *telemetry.Recorder) *Client {
	// The default transport keeps only 2 idle connections per host, so
	// parallel calls to one provider kept paying for fresh TLS handshakes.
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 256
	t.MaxIdleConnsPerHost = 32
	t.IdleConnTimeout = 90 * time.Second
	t.ForceAttemptHTTP2 = true
	return &Client{
		http:      &http.Client{Timeout: 15 * time.Second, Transport: t},
		inflight:  map[string]*flight{},
		userAgent: userAgent,
		cache:     c,
		tel:       tel,
		limiters:  map[string]*Limiter{},
		retries:   2,
	}
}

// SetLimit configures a token bucket for a source: rate requests/second with
// the given burst. Call during setup only (the map is not locked).
func (c *Client) SetLimit(source string, rate float64, burst int) {
	c.limiters[source] = NewLimiter(rate, burst)
}

// WithHTTPClient swaps the transport (tests point it at httptest servers).
func (c *Client) WithHTTPClient(h *http.Client) *Client { c.http = h; return c }

// GetJSON fetches url on behalf of source and decodes the body into out.
// cacheKey defaults to the URL; pass a custom key to keep secrets (API keys)
// out of cache keys and logs.
func (c *Client) GetJSON(ctx context.Context, source, url, cacheKey string, out any) error {
	if cacheKey == "" {
		cacheKey = url
	}
	start := time.Now()
	if c.cache != nil {
		if b, ok := c.cache.Get(cacheKey); ok {
			c.observe(source, start, true, nil, false)
			return json.Unmarshal(b, out)
		}
	}

	// Join an identical request already in flight instead of duplicating it.
	c.mu.Lock()
	if f, ok := c.inflight[cacheKey]; ok {
		c.mu.Unlock()
		select {
		case <-f.done:
		case <-ctx.Done():
			return ctx.Err()
		}
		c.observe(source, start, true, nil, false)
		if f.err != nil {
			return f.err
		}
		return json.Unmarshal(f.body, out)
	}
	f := &flight{done: make(chan struct{})}
	c.inflight[cacheKey] = f
	c.mu.Unlock()

	body, rateLimited, err := c.fetch(ctx, source, url)
	c.observe(source, start, false, err, rateLimited)
	if err == nil {
		var probe any
		if jerr := json.Unmarshal(body, &probe); jerr != nil {
			err = fmt.Errorf("decode %s response: %w", source, jerr)
		} else if c.cache != nil {
			c.cache.Set(cacheKey, body)
		}
	}
	f.body, f.err = body, err
	c.mu.Lock()
	delete(c.inflight, cacheKey)
	c.mu.Unlock()
	close(f.done)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

func (c *Client) observe(source string, start time.Time, cached bool, err error, rl bool) {
	if c.tel != nil {
		c.tel.Observe(source, time.Since(start), cached, err, rl)
	}
}

func (c *Client) fetch(ctx context.Context, source, url string) ([]byte, bool, error) {
	var lastErr error
	rateLimited := false
	for attempt := 0; attempt <= c.retries; attempt++ {
		if l := c.limiters[source]; l != nil {
			if err := l.Wait(ctx); err != nil {
				return nil, rateLimited, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, rateLimited, err
		}
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Accept", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, rateLimited, ctx.Err()
			}
			lastErr = err
			if !sleep(ctx, backoff(attempt, 0)) {
				return nil, rateLimited, ctx.Err()
			}
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return body, rateLimited, nil
		}
		lastErr = &StatusError{Code: resp.StatusCode, Body: truncate(string(body), 200)}
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		if resp.StatusCode == http.StatusTooManyRequests {
			rateLimited = true
		}
		if !retryable || attempt == c.retries {
			break
		}
		if !sleep(ctx, backoff(attempt, retryAfter(resp.Header.Get("Retry-After")))) {
			return nil, rateLimited, ctx.Err()
		}
	}
	return nil, rateLimited, lastErr
}

func backoff(attempt int, hint time.Duration) time.Duration {
	if hint > 0 {
		return min(hint, 5*time.Second)
	}
	return time.Duration(250*(1<<attempt)) * time.Millisecond
}

func retryAfter(h string) time.Duration {
	if s, err := strconv.Atoi(h); err == nil {
		return time.Duration(s) * time.Second
	}
	return 0
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// IsTimeout reports whether err came from a deadline.
func IsTimeout(err error) bool {
	return errors.Is(err, context.DeadlineExceeded)
}
