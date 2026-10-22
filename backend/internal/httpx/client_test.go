package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ayanjhunjhunwala/auris/backend/internal/cache"
	"github.com/ayanjhunjhunwala/auris/backend/internal/telemetry"
)

func TestRetriesOn429ThenCaches(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "ua/1" {
			t.Errorf("missing user agent")
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tel := telemetry.New()
	c := New("ua/1", cache.New[[]byte](10, time.Minute), tel)
	var out struct{ OK bool }
	for range 2 {
		if err := c.GetJSON(context.Background(), "src", srv.URL, "", &out); err != nil || !out.OK {
			t.Fatalf("err=%v out=%v", err, out)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("want 1 retry + 1 success then cache hit, got %d upstream calls", calls.Load())
	}
	s := tel.Snapshot().Sources[0]
	if s.CacheHits != 1 || s.RateLimits != 1 {
		t.Fatalf("telemetry: %+v", s)
	}
}

func TestDoesNotRetry4xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()
	c := New("ua", nil, nil)
	var out any
	err := c.GetJSON(context.Background(), "src", srv.URL, "", &out)
	if se, ok := err.(*StatusError); !ok || se.Code != 404 || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestLimiterPaces(t *testing.T) {
	l := NewLimiter(20, 1)
	start := time.Now()
	for range 3 {
		if err := l.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if el := time.Since(start); el < 80*time.Millisecond {
		t.Fatalf("3 tokens at 20/s with burst 1 should take ~100ms, took %v", el)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	slow := NewLimiter(0.001, 1)
	_ = slow.Wait(context.Background())
	if err := slow.Wait(ctx); err == nil {
		t.Fatal("cancelled context should abort wait")
	}
}

func TestConcurrentIdenticalRequestsShareOneFetch(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(50 * time.Millisecond)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := New("ua", cache.New[[]byte](10, time.Minute), nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out struct{ OK bool }
			if err := c.GetJSON(context.Background(), "src", srv.URL, "", &out); err != nil || !out.OK {
				t.Errorf("err=%v out=%v", err, out)
			}
		}()
	}
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("8 concurrent identical requests made %d upstream calls", n)
	}
}
