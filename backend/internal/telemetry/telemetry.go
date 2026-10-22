// Package telemetry keeps process-wide counters for every upstream source so
// the dashboard can show live health: request volume, error rate, cache hit
// rate and latency percentiles.
package telemetry

import (
	"sort"
	"sync"
	"time"
)

const window = 256 // latency samples kept per source

type sourceStats struct {
	Requests   uint64
	Errors     uint64
	CacheHits  uint64
	RateLimits uint64
	LastError  string
	LastErrAt  time.Time
	LastOKAt   time.Time
	lat        []float64 // ring buffer, ms
	next       int
}

// SourceSnapshot is the JSON view of one source's counters.
type SourceSnapshot struct {
	Source     string    `json:"source"`
	Enabled    bool      `json:"enabled"`
	Requests   uint64    `json:"requests"`
	Errors     uint64    `json:"errors"`
	CacheHits  uint64    `json:"cacheHits"`
	RateLimits uint64    `json:"rateLimits"`
	P50Ms      float64   `json:"p50Ms"`
	P95Ms      float64   `json:"p95Ms"`
	LastError  string    `json:"lastError,omitempty"`
	LastErrAt  time.Time `json:"lastErrorAt,omitzero"`
	LastOKAt   time.Time `json:"lastOkAt,omitzero"`
}

// RequestSample summarises one /api/similar call for the activity sparkline.
type RequestSample struct {
	At        time.Time `json:"at"`
	TookMs    int64     `json:"tookMs"`
	Seed      string    `json:"seed"`
	Results   int       `json:"results"`
	Pool      int       `json:"pool"`
	Steered   bool      `json:"steered"`
	SourcesOK int       `json:"sourcesOk"`
}

// Snapshot is the payload of GET /api/telemetry.
type Snapshot struct {
	StartedAt time.Time        `json:"startedAt"`
	UptimeSec int64            `json:"uptimeSec"`
	Sources   []SourceSnapshot `json:"sources"`
	Recent    []RequestSample  `json:"recent"`
	Cache     CacheStats       `json:"cache"`
	Learner   map[string]any   `json:"learner,omitempty"`
}

type CacheStats struct {
	Hits   uint64 `json:"hits"`
	Misses uint64 `json:"misses"`
	Size   int    `json:"size"`
}

type Recorder struct {
	mu      sync.Mutex
	started time.Time
	sources map[string]*sourceStats
	enabled map[string]bool
	recent  []RequestSample
}

func New() *Recorder {
	return &Recorder{started: time.Now(), sources: map[string]*sourceStats{}, enabled: map[string]bool{}}
}

func (r *Recorder) stats(src string) *sourceStats {
	s, ok := r.sources[src]
	if !ok {
		s = &sourceStats{lat: make([]float64, 0, window)}
		r.sources[src] = s
	}
	return s
}

// Register declares a source so it appears in snapshots even before traffic.
func (r *Recorder) Register(src string, enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stats(src)
	r.enabled[src] = enabled
}

// Observe records one upstream call.
func (r *Recorder) Observe(src string, d time.Duration, cached bool, err error, rateLimited bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stats(src)
	s.Requests++
	if cached {
		s.CacheHits++
		return // cache hits would drag latency percentiles toward zero
	}
	if rateLimited {
		s.RateLimits++
	}
	if err != nil {
		s.Errors++
		s.LastError, s.LastErrAt = err.Error(), time.Now()
	} else {
		s.LastOKAt = time.Now()
	}
	ms := float64(d.Microseconds()) / 1000
	if len(s.lat) < window {
		s.lat = append(s.lat, ms)
	} else {
		s.lat[s.next] = ms
		s.next = (s.next + 1) % window
	}
}

// Request records a completed recommendation request.
func (r *Recorder) Request(s RequestSample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recent = append(r.recent, s)
	if len(r.recent) > 120 {
		r.recent = r.recent[len(r.recent)-120:]
	}
}

func (r *Recorder) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	snap := Snapshot{StartedAt: r.started, UptimeSec: int64(time.Since(r.started).Seconds())}
	for name, s := range r.sources {
		p50, p95 := percentiles(s.lat)
		snap.Sources = append(snap.Sources, SourceSnapshot{
			Source: name, Enabled: r.enabled[name],
			Requests: s.Requests, Errors: s.Errors, CacheHits: s.CacheHits, RateLimits: s.RateLimits,
			P50Ms: p50, P95Ms: p95, LastError: s.LastError, LastErrAt: s.LastErrAt, LastOKAt: s.LastOKAt,
		})
	}
	sort.Slice(snap.Sources, func(i, j int) bool { return snap.Sources[i].Source < snap.Sources[j].Source })
	snap.Recent = append([]RequestSample(nil), r.recent...)
	return snap
}

func percentiles(xs []float64) (p50, p95 float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	at := func(q float64) float64 { return s[int(q*float64(len(s)-1))] }
	return at(0.5), at(0.95)
}
