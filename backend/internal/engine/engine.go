// Package engine is the aggregator. One call to Similar runs a five-stage
// pipeline, each stage fanning out to sources concurrently under a shared
// deadline, and records per-source telemetry for the dashboard:
//
//  1. resolve  - turn the query into a seed track and collect every ID the
//     sources need (Deezer -> ISRC; MusicBrainz -> MBID; ReccoBeats ->
//     audio features + its own ID)
//  2. steer    - apply "more upbeat"-style requests to the seed's audio
//     profile to get a target profile
//  3. candidates - ask every candidate source for neighbours in parallel
//  4. enrich   - merge/dedupe, then attach audio features and artwork to the
//     most promising candidates
//  5. rank     - score with the learned model and diversify (MMR)
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ayanjhunjhunwala/auris/backend/internal/cache"
	"github.com/ayanjhunjhunwala/auris/backend/internal/httpx"
	"github.com/ayanjhunjhunwala/auris/backend/internal/learn"
	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
	"github.com/ayanjhunjhunwala/auris/backend/internal/sources"
	"github.com/ayanjhunjhunwala/auris/backend/internal/telemetry"
)

// LanguageDetector sets Track.Language in place.
type LanguageDetector interface {
	Detect(ctx context.Context, tracks []*model.Track)
}

// ErrSeedNotFound means no source could identify the requested song.
var ErrSeedNotFound = errors.New("could not identify that song")

type Engine struct {
	Searchers    []sources.Searcher        // tried in order
	SeedResolver sources.SeedResolver      // primary seed lookup
	IDResolvers  []sources.IDResolver      // fill extra IDs on the seed
	Candidates   []sources.CandidateSource // run in parallel
	Features     sources.FeatureSource
	Enricher     sources.Enricher
	// ArtistNeighbors feed the artist-affinity ranking signal.
	ArtistNeighbors []sources.ArtistNeighbors
	// Languages tags tracks with the language they're sung in (optional;
	// without it the language filter is ignored).
	Languages LanguageDetector

	Ranker    *learn.Ranker
	Telemetry *telemetry.Recorder

	SourceTimeout time.Duration
	RequestBudget time.Duration
	// CandidateSoftDeadline: once it passes and at least one source has
	// answered, ranking starts. Slower sources keep running in the
	// background and warm the cache for the next request.
	CandidateSoftDeadline time.Duration
	// EnrichSoftDeadline bounds how long the response waits for artwork and
	// previews; stragglers finish in the background (see EnrichTracks).
	EnrichSoftDeadline time.Duration
	EnrichConcurrency  int

	// served remembers the signals behind each served recommendation so
	// feedback can train on exactly what the user saw.
	served *cache.LRU[map[string]map[string]float64]
}

func (e *Engine) init() {
	if e.served == nil {
		e.served = cache.New[map[string]map[string]float64](512, time.Hour)
	}
	if e.CandidateSoftDeadline == 0 {
		e.CandidateSoftDeadline = 2500 * time.Millisecond
	}
	if e.EnrichSoftDeadline == 0 {
		e.EnrichSoftDeadline = 1500 * time.Millisecond
	}
	if e.EnrichConcurrency == 0 {
		e.EnrichConcurrency = 8
	}
	if e.SourceTimeout == 0 {
		e.SourceTimeout = 6 * time.Second
	}
	if e.RequestBudget == 0 {
		e.RequestBudget = 12 * time.Second
	}
}

// Response extends the model response with a request ID for feedback.
type Response struct {
	model.SimilarResponse
	RequestID string `json:"requestId"`
}

// errLate marks a source that missed the soft deadline. Its call is left
// running so the response cache is warm next time.
var errLate = errors.New("missed the soft deadline; finishing in the background")

// reports collects per-stage telemetry safely from goroutines.
type reports struct {
	mu   sync.Mutex
	list []model.SourceReport
}

func (r *reports) add(src model.SourceName, stage string, start time.Time, count int, err error) {
	rep := model.SourceReport{Source: src, Stage: stage, LatencyMs: time.Since(start).Milliseconds(), Count: count, Status: model.StatusOK}
	switch {
	case errors.Is(err, errLate):
		rep.Status, rep.Error = model.StatusTimeout, err.Error()
	case errors.Is(err, sources.ErrMissingID):
		rep.Status = model.StatusSkipped
		rep.Error = err.Error()
	case httpx.IsTimeout(err):
		rep.Status, rep.Error = model.StatusTimeout, "timed out"
	case err != nil:
		rep.Status, rep.Error = model.StatusError, err.Error()
	case count == 0:
		rep.Status = model.StatusEmpty
	}
	r.mu.Lock()
	r.list = append(r.list, rep)
	r.mu.Unlock()
}

func (r *reports) disabled(src model.SourceName, stage string) {
	r.mu.Lock()
	r.list = append(r.list, model.SourceReport{Source: src, Stage: stage, Status: model.StatusDisabled})
	r.mu.Unlock()
}

// Search powers the search bar, trying searchers in order.
func (e *Engine) Search(ctx context.Context, q string, limit int) ([]model.Track, model.SourceName, error) {
	var lastErr error
	for _, s := range e.Searchers {
		if !s.Enabled() {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, e.timeout())
		res, err := s.Search(cctx, q, limit)
		cancel()
		if err == nil && len(res) > 0 {
			return res, s.Name(), nil
		}
		lastErr = err
	}
	return nil, "", lastErr
}

const (
	candidateDepth = 100
	// The head is the slice of the pool that gets audio features and
	// language tags before ranking: the best by fusion score.
	headFactor = 5
	minHead    = 120
)

func (e *Engine) timeout() time.Duration {
	if e.SourceTimeout == 0 {
		return 6 * time.Second
	}
	return e.SourceTimeout
}

// Similar runs the full pipeline.
func (e *Engine) Similar(ctx context.Context, req model.SimilarRequest) (*Response, error) {
	e.init()
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, e.RequestBudget)
	defer cancel()
	limit := req.Limit
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	allowed := func(n model.SourceName) bool {
		if len(req.Sources) == 0 {
			return true
		}
		for _, s := range req.Sources {
			if s == n {
				return true
			}
		}
		return false
	}
	rep := &reports{}

	// 1. Resolve ------------------------------------------------------------
	seed, err := e.resolveSeed(ctx, req.Seed, rep)
	if err != nil {
		return nil, err
	}
	// Each resolver works on a private copy of the seed; results are merged
	// back under seedMu so no goroutine reads a half-written seed.
	var wg sync.WaitGroup
	var seedMu sync.Mutex
	base := seed
	for _, r := range e.IDResolvers {
		if !r.Enabled() {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := base
			start := time.Now()
			cctx, cancel := context.WithTimeout(ctx, e.SourceTimeout)
			defer cancel()
			err := r.ResolveIDs(cctx, &local)
			rep.add(r.Name(), "resolve", start, boolCount(err == nil), err)
			seedMu.Lock()
			mergeIDs(&seed, local)
			seedMu.Unlock()
		}()
	}
	if e.Features != nil && e.Features.Enabled() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := base
			local.Features = nil
			start := time.Now()
			cctx, cancel := context.WithTimeout(ctx, e.SourceTimeout)
			defer cancel()
			n, err := e.Features.AttachFeatures(cctx, []*model.Track{&local})
			rep.add(e.Features.Name(), "resolve", start, n, err)
			seedMu.Lock()
			mergeIDs(&seed, local)
			if !local.Features.IsEmpty() {
				seed.Features = mergeFeatures(local.Features, seed.Features)
			}
			seedMu.Unlock()
		}()
	}
	var ref *model.Track
	if req.Reference != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ref = e.resolveReference(ctx, *req.Reference, rep)
		}()
	}
	wg.Wait()

	// 2. Steer --------------------------------------------------------------
	// Order matters: named directions, then the reference pull, then any
	// explicit absolute targets (most specific wins).
	plan := PlanSteer(seed.Features, req.Steer)
	if ref != nil && !ref.Features.IsEmpty() {
		w := req.ReferenceWeight
		if w <= 0 {
			w = 0.6
		}
		plan.Override(Blend(seed.Features, ref.Features, w))
	}
	plan.Override(req.Targets)
	// Ask each source for its deepest list (ReccoBeats caps at 100); a
	// bigger pool gives consensus and the ranker more to work with.
	opts := sources.Options{Limit: candidateDepth}
	if plan.Active {
		opts.Target = plan.Compare
	}
	exclude := []model.Track{seed}
	if ref != nil {
		opts.ExtraSeeds = []model.Track{*ref}
		exclude = append(exclude, *ref)
	}

	// 3. Candidates ---------------------------------------------------------
	p := newPool(exclude...)
	affinityCh := e.artistAffinity(ctx, seed, rep)
	active := e.gatherCandidates(ctx, seed, opts, allowed, p, rep)
	var affinity map[string]float64
	select { // usually long done; never hold the response for it
	case affinity = <-affinityCh:
	case <-time.After(300 * time.Millisecond):
	}

	// 4. Features -----------------------------------------------------------
	// Audio features feed the ranker, so they're attached before ranking,
	// to the most promising candidates by fusion score.
	sort.SliceStable(p.items, func(i, j int) bool { return fusion(p.items[i]) > fusion(p.items[j]) })
	head := p.items[:min(len(p.items), max(limit*headFactor, minHead))]
	if e.Features != nil && e.Features.Enabled() && len(head) > 0 {
		copies := make([]model.Track, len(head))
		cptrs := make([]*model.Track, len(head))
		for i, c := range head {
			copies[i] = model.Track{IDs: c.Track.IDs, Features: c.Track.Features}
			cptrs[i] = &copies[i]
		}
		start := time.Now()
		cctx, cancel := context.WithTimeout(ctx, e.SourceTimeout)
		n, err := e.Features.AttachFeatures(cctx, cptrs)
		cancel()
		rep.add(e.Features.Name(), "features", start, n, err)
		for i, c := range head {
			t := &c.Track
			t.Features = mergeFeatures(t.Features, copies[i].Features)
			t.IDs.ReccoBeatsID = firstNonEmpty(t.IDs.ReccoBeatsID, copies[i].IDs.ReccoBeatsID)
			t.IDs.SpotifyID = firstNonEmpty(t.IDs.SpotifyID, copies[i].IDs.SpotifyID)
		}
	}

	// 4b. Language filter -------------------------------------------------
	// Only the head was considered for features; the filter keeps the
	// tagged head tracks that match and drops everything else.
	if len(req.Languages) > 0 && e.Languages != nil {
		want := map[string]bool{}
		for _, l := range req.Languages {
			want[strings.ToLower(l)] = true
		}
		ptrs := make([]*model.Track, len(head))
		for i, c := range head {
			ptrs[i] = &c.Track
		}
		start := time.Now()
		cctx, cancel := context.WithTimeout(ctx, e.SourceTimeout)
		e.Languages.Detect(cctx, ptrs)
		cancel()
		kept := make([]*model.Candidate, 0, len(head))
		for _, c := range head {
			if want[c.Track.Language] {
				kept = append(kept, c)
			}
		}
		p.items = kept
		rep.add("language", "language", start, len(kept), nil)
	}

	// 5. Rank ---------------------------------------------------------------
	recs := rank(p.items, seed, plan, e.Ranker, max(active, 1), limit, affinity)

	// 6. Enrich -------------------------------------------------------------
	// Artwork and previews only for what's actually returned. Tracks that
	// miss the soft deadline come back bare; the client fills them in via
	// /api/enrich once the background lookups have warmed the cache.
	if e.Enricher != nil && e.Enricher.Enabled() && len(recs) > 0 {
		tracks := make([]model.Track, len(recs))
		for i, r := range recs {
			tracks[i] = r.Track
		}
		start := time.Now()
		done, err := e.EnrichTracks(ctx, tracks, e.EnrichSoftDeadline)
		rep.add(e.Enricher.Name(), "enrich", start, done, err)
		for i := range recs {
			fillPresentation(&recs[i].Track, tracks[i])
		}
	}

	reqID := newID()
	servedSignals := make(map[string]map[string]float64, len(recs))
	for _, r := range recs {
		servedSignals[r.Track.Key] = r.Breakdown.Signals
	}
	e.served.Set(reqID, servedSignals)

	resp := &Response{RequestID: reqID, SimilarResponse: model.SimilarResponse{
		Seed: seed, Recommendations: recs, Sources: rep.list, CandidatePool: len(p.items),
		TookMs: time.Since(started).Milliseconds(), GeneratedAt: time.Now().UTC(),
	}}
	if plan.Target != nil {
		resp.Target = plan.Target
	}
	resp.Reference = ref
	if e.Telemetry != nil {
		ok := 0
		for _, r := range rep.list {
			if r.Stage == "candidates" && r.Status == model.StatusOK {
				ok++
			}
		}
		e.Telemetry.Request(telemetry.RequestSample{
			At: time.Now(), TookMs: resp.TookMs, Seed: seed.Artist + " - " + seed.Title,
			Results: len(recs), Pool: len(p.items), Steered: plan.Active, SourcesOK: ok,
		})
	}
	return resp, nil
}

func (e *Engine) resolveSeed(ctx context.Context, q model.SeedQuery, rep *reports) (model.Track, error) {
	if e.SeedResolver != nil && e.SeedResolver.Enabled() {
		start := time.Now()
		cctx, cancel := context.WithTimeout(ctx, e.SourceTimeout)
		seed, err := e.SeedResolver.ResolveSeed(cctx, q)
		cancel()
		rep.add(e.SeedResolver.Name(), "resolve", start, boolCount(err == nil), err)
		if err == nil {
			return seed, nil
		}
	}
	// Fallbacks: explicit artist/title, else the first search hit elsewhere.
	if q.Artist != "" && q.Title != "" {
		return model.Track{Key: sources.TrackKey(q.Artist, q.Title), Artist: q.Artist, Title: q.Title, IDs: model.IDs{ISRC: q.ISRC}}, nil
	}
	if q.Query != "" {
		if hits, _, err := e.Search(ctx, q.Query, 1); err == nil && len(hits) > 0 {
			return hits[0], nil
		}
	}
	return model.Track{}, ErrSeedNotFound
}

// resolveReference identifies a "sound like" track and fetches its audio
// profile. Failure is non-fatal: the request proceeds without the pull.
func (e *Engine) resolveReference(ctx context.Context, q model.SeedQuery, rep *reports) *model.Track {
	if e.SeedResolver == nil || !e.SeedResolver.Enabled() {
		return nil
	}
	start := time.Now()
	cctx, cancel := context.WithTimeout(ctx, e.SourceTimeout)
	defer cancel()
	t, err := e.SeedResolver.ResolveSeed(cctx, q)
	rep.add(e.SeedResolver.Name(), "reference", start, boolCount(err == nil), err)
	if err != nil {
		return nil
	}
	if e.Features != nil && e.Features.Enabled() {
		start = time.Now()
		n, err := e.Features.AttachFeatures(cctx, []*model.Track{&t})
		rep.add(e.Features.Name(), "reference", start, n, err)
	}
	return &t
}

// artistAffinity asks every ArtistNeighbors source in parallel and merges
// their maps (max per artist). The result arrives on the channel.
func (e *Engine) artistAffinity(ctx context.Context, seed model.Track, rep *reports) <-chan map[string]float64 {
	out := make(chan map[string]float64, 1)
	go func() {
		merged := map[string]float64{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, src := range e.ArtistNeighbors {
			if !src.Enabled() {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				start := time.Now()
				cctx, cancel := context.WithTimeout(ctx, e.SourceTimeout)
				defer cancel()
				m, err := src.SimilarArtists(cctx, seed)
				rep.add(src.Name(), "artists", start, len(m), err)
				mu.Lock()
				for k, v := range m {
					merged[k] = math.Max(merged[k], v)
				}
				mu.Unlock()
			}()
		}
		wg.Wait()
		out <- merged
	}()
	return out
}

// gatherCandidates fans out to every candidate source and merges results
// into the pool. It returns once all sources answer, or once the soft
// deadline has passed with at least one useful answer. Calls run on a
// context detached from the request (bounded by SourceTimeout), so a late
// source still completes and caches its response.
func (e *Engine) gatherCandidates(ctx context.Context, seed model.Track, opts sources.Options,
	allowed func(model.SourceName) bool, p *pool, rep *reports) (active int) {
	type result struct {
		src   model.SourceName
		cands []model.Candidate
		err   error
		start time.Time
	}
	results := make(chan result, len(e.Candidates))
	pending := map[model.SourceName]time.Time{}
	for _, src := range e.Candidates {
		if !src.Enabled() || !allowed(src.Name()) {
			rep.disabled(src.Name(), "candidates")
			continue
		}
		start := time.Now()
		pending[src.Name()] = start
		go func() {
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.SourceTimeout)
			defer cancel()
			cands, err := src.Similar(cctx, seed, opts)
			results <- result{src.Name(), cands, err, start}
		}()
	}

	soft := time.NewTimer(e.CandidateSoftDeadline)
	defer soft.Stop()
	softPassed := false
	for len(pending) > 0 {
		select {
		case r := <-results:
			delete(pending, r.src)
			rep.add(r.src, "candidates", r.start, len(r.cands), r.err)
			if len(r.cands) > 0 {
				active++
				for _, c := range r.cands {
					p.add(c)
				}
			}
			if softPassed && active > 0 {
				goto late
			}
		case <-soft.C:
			softPassed = true
			if active > 0 {
				goto late
			}
		case <-ctx.Done():
			goto late
		}
	}
late:
	for name, start := range pending {
		rep.add(name, "candidates", start, 0, errLate)
	}
	return active
}

// EnrichTracks fills artwork, previews and links in place, several lookups
// at a time. It waits at most `wait`; lookups still running keep going on a
// detached context and land in the response cache. Returns how many tracks
// were enriched in time.
func (e *Engine) EnrichTracks(ctx context.Context, tracks []model.Track, wait time.Duration) (int, error) {
	e.init()
	if e.Enricher == nil || !e.Enricher.Enabled() {
		return 0, nil
	}
	type result struct {
		i   int
		t   model.Track
		err error
	}
	results := make(chan result, len(tracks))
	sem := make(chan struct{}, e.EnrichConcurrency)
	bg := context.WithoutCancel(ctx)
	n := 0
	for i, t := range tracks {
		if t.ArtworkURL != "" && t.PreviewURL != "" {
			continue
		}
		n++
		go func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(bg, e.SourceTimeout)
			defer cancel()
			local := model.Track{Title: t.Title, Artist: t.Artist, ArtworkURL: t.ArtworkURL, PreviewURL: t.PreviewURL,
				IDs: model.IDs{ISRC: t.IDs.ISRC}}
			err := e.Enricher.Enrich(cctx, &local)
			results <- result{i, local, err}
		}()
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	ok := 0
	var firstErr error
	for ; n > 0; n-- {
		select {
		case r := <-results:
			if r.err == nil {
				ok++
				fillPresentation(&tracks[r.i], r.t)
			} else if firstErr == nil && !errors.Is(r.err, sources.ErrNotFound) {
				firstErr = r.err
			}
		case <-timer.C:
			return ok, firstErr
		case <-ctx.Done():
			return ok, firstErr
		}
	}
	return ok, firstErr
}

// fillPresentation copies display fields the destination lacks.
func fillPresentation(dst *model.Track, src model.Track) {
	dst.ArtworkURL = firstNonEmpty(dst.ArtworkURL, src.ArtworkURL)
	dst.PreviewURL = firstNonEmpty(dst.PreviewURL, src.PreviewURL)
	dst.Album = firstNonEmpty(dst.Album, src.Album)
	dst.URL = firstNonEmpty(dst.URL, src.URL)
	if dst.IDs.DeezerID == 0 {
		dst.IDs.DeezerID, dst.IDs.DeezerArtistID = src.IDs.DeezerID, src.IDs.DeezerArtistID
	}
	if dst.Popularity == nil {
		dst.Popularity = src.Popularity
	}
}

// Feedback trains the ranker on a served recommendation.
func (e *Engine) Feedback(requestID, trackKey string, liked bool) error {
	e.init()
	served, ok := e.served.Get(requestID)
	if !ok {
		return fmt.Errorf("unknown or expired request %q", requestID)
	}
	sig, ok := served[trackKey]
	if !ok {
		return fmt.Errorf("track %q was not served in request %q", trackKey, requestID)
	}
	return e.Ranker.Update(sig, liked)
}

func mergeIDs(dst *model.Track, src model.Track) {
	dst.IDs.ISRC = firstNonEmpty(dst.IDs.ISRC, src.IDs.ISRC)
	dst.IDs.MBID = firstNonEmpty(dst.IDs.MBID, src.IDs.MBID)
	dst.IDs.SpotifyID = firstNonEmpty(dst.IDs.SpotifyID, src.IDs.SpotifyID)
	dst.IDs.ReccoBeatsID = firstNonEmpty(dst.IDs.ReccoBeatsID, src.IDs.ReccoBeatsID)
	dst.IDs.ReleaseMBID = firstNonEmpty(dst.IDs.ReleaseMBID, src.IDs.ReleaseMBID)
	dst.ArtistID = firstNonEmpty(dst.ArtistID, src.ArtistID)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func boolCount(ok bool) int {
	if ok {
		return 1
	}
	return 0
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
