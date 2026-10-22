package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ayanjhunjhunwala/auris/backend/internal/learn"
	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
	"github.com/ayanjhunjhunwala/auris/backend/internal/sources"
)

// --- fakes -------------------------------------------------------------------

type fakeSource struct {
	name  model.SourceName
	cands []model.Candidate
	err   error
	delay time.Duration
	// lastTarget records the steering target the engine passed in.
	lastTarget *model.Features
}

func (f *fakeSource) Name() model.SourceName { return f.name }
func (f *fakeSource) Enabled() bool          { return true }
func (f *fakeSource) Similar(ctx context.Context, _ model.Track, o sources.Options) ([]model.Candidate, error) {
	f.lastTarget = o.Target
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return f.cands, f.err
}

type fakeResolver struct{ seed model.Track }

func (f fakeResolver) Name() model.SourceName { return model.SourceDeezer }
func (f fakeResolver) Enabled() bool          { return true }
func (f fakeResolver) ResolveSeed(_ context.Context, q model.SeedQuery) (model.Track, error) {
	if q.Query == "upbeat reference" {
		return model.Track{Key: sources.TrackKey("Party Band", "Fast One"), Artist: "Party Band", Title: "Fast One",
			IDs: model.IDs{ISRC: "UPBEAT", SpotifyID: "sp-ref"}}, nil
	}
	return f.seed, nil
}

type fakeFeatures struct{ byKey map[string]*model.Features }

func (f fakeFeatures) Name() model.SourceName { return model.SourceReccoBeats }
func (f fakeFeatures) Enabled() bool          { return true }
func (f fakeFeatures) AttachFeatures(_ context.Context, ts []*model.Track) (int, error) {
	n := 0
	for _, t := range ts {
		if feat, ok := f.byKey[t.IDs.ISRC]; ok {
			t.Features = feat
			n++
		}
	}
	return n, nil
}

func cand(src model.SourceName, rank int, score float64, artist, title, isrc string) model.Candidate {
	return model.Candidate{
		Track:    model.Track{Key: sources.TrackKey(artist, title), Artist: artist, Title: title, IDs: model.IDs{ISRC: isrc}},
		Evidence: []model.Evidence{{Source: src, Rank: rank, Score: score}},
	}
}

func feat(energy, valence, tempo float64) *model.Features {
	return &model.Features{Energy: &energy, Valence: &valence, Tempo: &tempo,
		Danceability: model.Ptr(0.6), Acousticness: model.Ptr(0.2)}
}

func newEngine(t *testing.T, cands ...*fakeSource) *Engine {
	seed := model.Track{Key: sources.TrackKey("Seed Artist", "Seed Song"), Artist: "Seed Artist", Title: "Seed Song", IDs: model.IDs{ISRC: "SEED"}}
	e := &Engine{
		SeedResolver: fakeResolver{seed},
		Features: fakeFeatures{byKey: map[string]*model.Features{
			"SEED":   feat(0.5, 0.4, 110),
			"CALM":   feat(0.3, 0.3, 95),
			"UPBEAT": feat(0.9, 0.85, 128),
			"MID":    feat(0.55, 0.45, 112),
		}},
		Ranker:        learn.New(filepath.Join(t.TempDir(), "w.json"), 0.1),
		SourceTimeout: 200 * time.Millisecond,
		RequestBudget: time.Second,
	}
	for _, c := range cands {
		e.Candidates = append(e.Candidates, c)
	}
	return e
}

// --- pipeline ----------------------------------------------------------------

func TestSimilarMergesAcrossSourcesAndRewardsConsensus(t *testing.T) {
	lfm := &fakeSource{name: model.SourceLastFM, cands: []model.Candidate{
		cand(model.SourceLastFM, 0, 0.9, "Solo Artist", "Only Lastfm", ""),
		cand(model.SourceLastFM, 1, 0.8, "Shared Artist", "Everyone Likes (feat. X)", "MID"),
		cand(model.SourceLastFM, 2, 0.7, "Seed Artist", "Seed Song", "SEED"), // the seed itself
	}}
	lb := &fakeSource{name: model.SourceListenBrainz, cands: []model.Candidate{
		cand(model.SourceListenBrainz, 0, 1.0, "Shared Artist", "Everyone Likes", ""),
	}}
	rb := &fakeSource{name: model.SourceReccoBeats, cands: []model.Candidate{
		cand(model.SourceReccoBeats, 0, 1.0, "Shared Artist feat. X", "Everyone Likes", "MID"),
	}}
	e := newEngine(t, lfm, lb, rb)

	resp, err := e.Similar(context.Background(), model.SimilarRequest{Seed: model.SeedQuery{Query: "seed"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if resp.CandidatePool != 2 {
		t.Fatalf("pool=%d, want 2 (seed excluded, variants merged)", resp.CandidatePool)
	}
	top := resp.Recommendations[0]
	if top.Track.Title == "Only Lastfm" || len(top.Evidence) != 3 {
		t.Fatalf("3-source consensus track should win: %+v", top)
	}
	if top.Breakdown.Signals[learn.SigConsensus] != 1 {
		t.Errorf("consensus=%v", top.Breakdown.Signals[learn.SigConsensus])
	}
	if top.Track.Features == nil || top.Breakdown.Signals[learn.SigAudioKnown] != 1 {
		t.Errorf("features not attached to merged track")
	}
	if resp.Seed.Features == nil || *resp.Seed.Features.Energy != 0.5 {
		t.Errorf("seed features not resolved: %+v", resp.Seed.Features)
	}
}

func TestSteeringReordersAndConditionsSources(t *testing.T) {
	// Identical source evidence; only audio differs.
	mk := func() *fakeSource {
		return &fakeSource{name: model.SourceReccoBeats, cands: []model.Candidate{
			cand(model.SourceReccoBeats, 0, 1.0, "Calm Band", "Slow One", "CALM"),
			cand(model.SourceReccoBeats, 1, 0.95, "Party Band", "Fast One", "UPBEAT"),
		}}
	}
	plain := mk()
	resp, err := newEngine(t, plain).Similar(context.Background(), model.SimilarRequest{Seed: model.SeedQuery{Query: "s"}})
	if err != nil {
		t.Fatal(err)
	}
	if plain.lastTarget != nil {
		t.Error("no steering requested, but a target was sent upstream")
	}
	if resp.Recommendations[0].Track.Title != "Slow One" {
		t.Fatalf("unsteered: closer-to-seed track should lead, got %s", resp.Recommendations[0].Track.Title)
	}

	steered := mk()
	resp, err = newEngine(t, steered).Similar(context.Background(), model.SimilarRequest{
		Seed: model.SeedQuery{Query: "s"}, Steer: []model.Steer{{Direction: "upbeat", Intensity: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Recommendations[0].Track.Title != "Fast One" {
		t.Fatalf("upbeat steer should promote the energetic track, got %s", resp.Recommendations[0].Track.Title)
	}
	if steered.lastTarget == nil || *steered.lastTarget.Energy <= 0.5 {
		t.Fatalf("steer target not passed upstream: %+v", steered.lastTarget)
	}
	if *resp.Target.Energy != 0.75 || *resp.Target.Valence != 0.7 {
		t.Errorf("target = seed + delta: energy %v valence %v", *resp.Target.Energy, *resp.Target.Valence)
	}
}

func TestFailingAndSlowSourcesDegradeGracefully(t *testing.T) {
	ok := &fakeSource{name: model.SourceLastFM, cands: []model.Candidate{cand(model.SourceLastFM, 0, 1, "A", "B", "")}}
	bad := &fakeSource{name: model.SourceListenBrainz, err: errors.New("boom")}
	slow := &fakeSource{name: model.SourceDeezer, delay: time.Second}
	skip := &fakeSource{name: model.SourceReccoBeats, err: sources.ErrMissingID}
	resp, err := newEngine(t, ok, bad, slow, skip).Similar(context.Background(), model.SimilarRequest{Seed: model.SeedQuery{Query: "s"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Recommendations) != 1 {
		t.Fatalf("want 1 rec from the healthy source, got %d", len(resp.Recommendations))
	}
	status := map[model.SourceName]model.SourceStatus{}
	for _, r := range resp.Sources {
		if r.Stage == "candidates" {
			status[r.Source] = r.Status
		}
	}
	want := map[model.SourceName]model.SourceStatus{
		model.SourceLastFM: model.StatusOK, model.SourceListenBrainz: model.StatusError,
		model.SourceDeezer: model.StatusTimeout, model.SourceReccoBeats: model.StatusSkipped,
	}
	for k, v := range want {
		if status[k] != v {
			t.Errorf("%s: status %s want %s", k, status[k], v)
		}
	}
}

func TestDiversifyLimitsOneArtist(t *testing.T) {
	var cands []model.Candidate
	for i, title := range []string{"Hit 1", "Hit 2", "Hit 3", "Hit 4"} {
		cands = append(cands, cand(model.SourceLastFM, i, 1-0.01*float64(i), "Mega Star", title, ""))
	}
	cands = append(cands, cand(model.SourceLastFM, 4, 0.9, "Other One", "Song", ""))
	resp, err := newEngine(t, &fakeSource{name: model.SourceLastFM, cands: cands}).
		Similar(context.Background(), model.SimilarRequest{Seed: model.SeedQuery{Query: "s"}, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	artists := map[string]bool{}
	for _, r := range resp.Recommendations {
		artists[r.Track.Artist] = true
	}
	if !artists["Other One"] {
		t.Fatalf("MMR should surface a second artist in the top 3: %+v", resp.Recommendations)
	}
}

func TestFeedbackTrainsOnServedSignals(t *testing.T) {
	src := &fakeSource{name: model.SourceLastFM, cands: []model.Candidate{cand(model.SourceLastFM, 0, 1, "A", "B", "")}}
	e := newEngine(t, src)
	resp, err := e.Similar(context.Background(), model.SimilarRequest{Seed: model.SeedQuery{Query: "s"}})
	if err != nil {
		t.Fatal(err)
	}
	before := e.Ranker.Weight(learn.SigLastFM)
	if err := e.Feedback(resp.RequestID, resp.Recommendations[0].Track.Key, true); err != nil {
		t.Fatal(err)
	}
	if after := e.Ranker.Weight(learn.SigLastFM); after <= before {
		t.Fatalf("like should raise the lastfm weight: %v -> %v", before, after)
	}
	if err := e.Feedback("nope", "x", true); err == nil {
		t.Fatal("unknown request should error")
	}
}

// --- steering math -------------------------------------------------------------

func TestPlanSteerOnlyComparesRealDimensions(t *testing.T) {
	plan := PlanSteer(&model.Features{Tempo: model.Ptr(100.0)}, nil)
	if plan.Active || plan.Compare.Energy != nil || *plan.Compare.Tempo != 100 {
		t.Fatalf("unknown dims must not appear in Compare: %+v", plan.Compare)
	}
	if plan.Target.Energy == nil {
		t.Fatal("Target should be fully imputed for display")
	}
	plan = PlanSteer(nil, []model.Steer{{Direction: "chill", Intensity: 0.5}})
	if !plan.Active || plan.Compare.Energy == nil || *plan.Compare.Energy >= 0.62 {
		t.Fatalf("steered dims must be compared: %+v", plan.Compare)
	}
}

func TestSteerClamps(t *testing.T) {
	plan := PlanSteer(feat(0.95, 0.95, 210), []model.Steer{{Direction: "upbeat", Intensity: 5}})
	if *plan.Target.Energy > 1 || *plan.Target.Tempo > 220 {
		t.Fatalf("not clamped: %+v", plan.Target)
	}
}

func TestAudioSimilarityAndAlignment(t *testing.T) {
	a := feat(0.5, 0.5, 120)
	if s, _ := AudioSimilarity(a, a); s != 1 {
		t.Fatalf("self-similarity %v", s)
	}
	near, _ := AudioSimilarity(a, feat(0.55, 0.5, 124))
	far, _ := AudioSimilarity(a, feat(0.95, 0.1, 170))
	if near <= far {
		t.Fatalf("near %v should beat far %v", near, far)
	}
	if _, ok := AudioSimilarity(a, nil); ok {
		t.Fatal("nil features are not comparable")
	}
	vec := PlanSteer(a, []model.Steer{{Direction: "upbeat", Intensity: 1}}).Vector
	up, _ := SteerAlignment(a, feat(0.8, 0.8, 132), vec)
	down, _ := SteerAlignment(a, feat(0.2, 0.2, 100), vec)
	if up <= 0.5 || down >= 0 {
		t.Fatalf("alignment up=%v down=%v", up, down)
	}
}

func TestReferencePullsTargetAndJoinsSeeds(t *testing.T) {
	src := &fakeSource{name: model.SourceReccoBeats, cands: []model.Candidate{
		cand(model.SourceReccoBeats, 0, 1.0, "Calm Band", "Slow One", "CALM"),
		cand(model.SourceReccoBeats, 1, 0.9, "Mid Band", "Middle", "MID"),
		cand(model.SourceReccoBeats, 2, 0.8, "Party Band", "Fast One", "UPBEAT"), // the reference itself
	}}
	var gotExtra []model.Track
	e := newEngine(t, src)
	e.Candidates = []sources.CandidateSource{recordExtra{src, &gotExtra}}

	resp, err := e.Similar(context.Background(), model.SimilarRequest{
		Seed:      model.SeedQuery{Query: "s"},
		Reference: &model.SeedQuery{Query: "upbeat reference"}, ReferenceWeight: 1,
		Targets: &model.Features{Tempo: model.Ptr(140.0)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reference == nil || resp.Reference.Title != "Fast One" || resp.Reference.Features == nil {
		t.Fatalf("reference not resolved with features: %+v", resp.Reference)
	}
	if *resp.Target.Energy != 0.9 {
		t.Errorf("weight 1 should move energy all the way to the reference's 0.9, got %v", *resp.Target.Energy)
	}
	if *resp.Target.Tempo != 140 {
		t.Errorf("explicit target must win over the reference: tempo %v", *resp.Target.Tempo)
	}
	for _, r := range resp.Recommendations {
		if r.Track.Title == "Fast One" {
			t.Error("the reference track itself should not be recommended")
		}
	}
	if len(gotExtra) != 1 || gotExtra[0].IDs.SpotifyID != "sp-ref" {
		t.Errorf("reference not passed as extra seed: %+v", gotExtra)
	}
}

func TestOverrideAndBlend(t *testing.T) {
	seed := feat(0.4, 0.4, 100)
	plan := PlanSteer(seed, nil)
	plan.Override(&model.Features{Energy: model.Ptr(0.8)})
	if !plan.Active || *plan.Target.Energy != 0.8 || plan.Vector["energy"] <= 0 {
		t.Fatalf("override not applied: %+v %+v", plan.Target, plan.Vector)
	}
	b := Blend(seed, feat(0.8, 0.8, 140), 0.5)
	if math.Abs(*b.Energy-0.6) > 1e-9 || *b.Tempo != 120 {
		t.Fatalf("blend midpoint wrong: %v %v", *b.Energy, *b.Tempo)
	}
}

// recordExtra wraps a source to capture Options.ExtraSeeds.
type recordExtra struct {
	*fakeSource
	got *[]model.Track
}

func (r recordExtra) Similar(ctx context.Context, s model.Track, o sources.Options) ([]model.Candidate, error) {
	*r.got = o.ExtraSeeds
	return r.fakeSource.Similar(ctx, s, o)
}

type fakeEnricher struct{ slow string }

func (fakeEnricher) Name() model.SourceName { return model.SourceDeezer }
func (fakeEnricher) Enabled() bool          { return true }
func (f fakeEnricher) Enrich(ctx context.Context, t *model.Track) error {
	if t.Title == f.slow {
		select {
		case <-time.After(300 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	t.ArtworkURL, t.PreviewURL = "art:"+t.Title, "mp3:"+t.Title
	return nil
}

// notifySource reports when its (possibly late) call actually completes.
type notifySource struct {
	*fakeSource
	done chan struct{}
}

func (n notifySource) Similar(ctx context.Context, s model.Track, o sources.Options) ([]model.Candidate, error) {
	defer close(n.done)
	return n.fakeSource.Similar(ctx, s, o)
}

func TestSoftDeadlineDoesNotWaitForSlowSources(t *testing.T) {
	fast := &fakeSource{name: model.SourceLastFM, delay: 10 * time.Millisecond,
		cands: []model.Candidate{cand(model.SourceLastFM, 0, 1, "A", "B", "")}}
	slow := notifySource{&fakeSource{name: model.SourceListenBrainz, delay: 600 * time.Millisecond,
		cands: []model.Candidate{cand(model.SourceListenBrainz, 0, 1, "C", "D", "")}}, make(chan struct{})}
	e := newEngine(t)
	e.Candidates = []sources.CandidateSource{fast, slow}
	e.SourceTimeout, e.RequestBudget = 2*time.Second, 3*time.Second
	e.CandidateSoftDeadline = 100 * time.Millisecond

	start := time.Now()
	resp, err := e.Similar(context.Background(), model.SimilarRequest{Seed: model.SeedQuery{Query: "s"}})
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 400*time.Millisecond {
		t.Fatalf("waited for the slow source: %v", took)
	}
	if len(resp.Recommendations) != 1 {
		t.Fatalf("want the fast source's result only: %+v", resp.Recommendations)
	}
	var late *model.SourceReport
	for i, r := range resp.Sources {
		if r.Source == model.SourceListenBrainz && r.Stage == "candidates" {
			late = &resp.Sources[i]
		}
	}
	if late == nil || late.Status != model.StatusTimeout || !strings.Contains(late.Error, "background") {
		t.Fatalf("slow source should be reported late: %+v", late)
	}
	select {
	case <-slow.done: // finished in the background, so its response got cached
	case <-time.After(2 * time.Second):
		t.Fatal("late source was cancelled instead of finishing in the background")
	}
}

func TestEnrichmentRunsOnReturnedTracksWithinBudget(t *testing.T) {
	src := &fakeSource{name: model.SourceLastFM, cands: []model.Candidate{
		cand(model.SourceLastFM, 0, 1, "A", "Quick", ""),
		cand(model.SourceLastFM, 1, 0.9, "B", "Slow", ""),
	}}
	e := newEngine(t, src)
	e.Enricher = fakeEnricher{slow: "Slow"}
	e.EnrichSoftDeadline = 100 * time.Millisecond
	e.SourceTimeout = time.Second
	resp, err := e.Similar(context.Background(), model.SimilarRequest{Seed: model.SeedQuery{Query: "s"}})
	if err != nil {
		t.Fatal(err)
	}
	art := map[string]string{}
	for _, r := range resp.Recommendations {
		art[r.Track.Title] = r.Track.ArtworkURL
	}
	if art["Quick"] != "art:Quick" || art["Slow"] != "" {
		t.Fatalf("quick should be enriched, slow left for later: %v", art)
	}
	// The fill-in path (/api/enrich) waits longer and gets it.
	tracks := []model.Track{{Title: "Slow", Artist: "B"}}
	if n, _ := e.EnrichTracks(context.Background(), tracks, time.Second); n != 1 || tracks[0].PreviewURL != "mp3:Slow" {
		t.Fatalf("fill-in failed: n=%d %+v", n, tracks[0])
	}
}

type fakeLang struct{}

func (fakeLang) Detect(_ context.Context, ts []*model.Track) {
	for _, t := range ts {
		if strings.HasPrefix(t.Title, "Canción") {
			t.Language = "es"
		} else {
			t.Language = "en"
		}
	}
}

func TestLanguageFilter(t *testing.T) {
	src := &fakeSource{name: model.SourceLastFM, cands: []model.Candidate{
		cand(model.SourceLastFM, 0, 1, "A", "Song One", ""),
		cand(model.SourceLastFM, 1, 0.9, "B", "Canción Dos", ""),
		cand(model.SourceLastFM, 2, 0.8, "C", "Canción Tres", ""),
	}}
	e := newEngine(t, src)
	e.Languages = fakeLang{}
	resp, err := e.Similar(context.Background(), model.SimilarRequest{Seed: model.SeedQuery{Query: "s"}, Languages: []string{"ES"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Recommendations) != 2 {
		t.Fatalf("want 2 Spanish songs, got %+v", resp.Recommendations)
	}
	for _, r := range resp.Recommendations {
		if r.Track.Language != "es" {
			t.Errorf("non-matching language leaked: %+v", r.Track)
		}
	}
	all, _ := e.Similar(context.Background(), model.SimilarRequest{Seed: model.SeedQuery{Query: "s"}})
	if len(all.Recommendations) != 3 {
		t.Fatalf("no filter should keep all: %d", len(all.Recommendations))
	}
}

func TestPoolDropsJunkAndOtherVersions(t *testing.T) {
	src := &fakeSource{name: model.SourceLastFM, cands: []model.Candidate{
		cand(model.SourceLastFM, 0, 1, "A", "Good Song", ""),
		cand(model.SourceLastFM, 1, 1, "Sing King", "Good Song (Karaoke Version)", ""),
		cand(model.SourceLastFM, 2, 1, "B", "Other (Live at Wembley)", ""),
		cand(model.SourceLastFM, 3, 1, "C", "Third - Remastered 2011", ""),
	}}
	resp, err := newEngine(t, src).Similar(context.Background(), model.SimilarRequest{Seed: model.SeedQuery{Query: "s"}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range resp.Recommendations {
		got[r.Track.Title] = true
	}
	if len(got) != 2 || !got["Good Song"] || !got["Third - Remastered 2011"] {
		t.Fatalf("want studio originals only (remasters are fine): %v", got)
	}
}

type fakeNeighbors map[string]float64

func (fakeNeighbors) Name() model.SourceName { return model.SourceLastFM }
func (fakeNeighbors) Enabled() bool          { return true }
func (f fakeNeighbors) SimilarArtists(context.Context, model.Track) (map[string]float64, error) {
	return f, nil
}

func TestArtistAffinityLiftsKindredArtists(t *testing.T) {
	// Same source evidence; only the artist relationship differs.
	src := &fakeSource{name: model.SourceReccoBeats, cands: []model.Candidate{
		cand(model.SourceReccoBeats, 0, 1.0, "Random Popstar", "Hit", ""),
		cand(model.SourceReccoBeats, 1, 0.95, "Portishead", "Roads", ""),
	}}
	e := newEngine(t, src)
	e.ArtistNeighbors = []sources.ArtistNeighbors{fakeNeighbors{"portishead": 0.9}}
	resp, err := e.Similar(context.Background(), model.SimilarRequest{Seed: model.SeedQuery{Query: "s"}})
	if err != nil {
		t.Fatal(err)
	}
	top := resp.Recommendations[0]
	if top.Track.Artist != "Portishead" || top.Breakdown.Signals["artist_affinity"] != 0.9 {
		t.Fatalf("kindred artist should lead: %+v", top)
	}
}

func TestHalfTimeTempoCountsAsClose(t *testing.T) {
	a := feat(0.5, 0.5, 140)
	half, _ := AudioSimilarity(a, feat(0.5, 0.5, 70))
	off, _ := AudioSimilarity(a, feat(0.5, 0.5, 100))
	if half <= off {
		t.Fatalf("70 vs 140 bpm (%v) should beat 100 vs 140 (%v)", half, off)
	}
}

func TestAtMostTwoPerArtist(t *testing.T) {
	var cands []model.Candidate
	for i := range 6 {
		cands = append(cands, cand(model.SourceLastFM, i, 1-0.01*float64(i), "Mega Star", fmt.Sprintf("Hit %d", i), ""))
	}
	resp, err := newEngine(t, &fakeSource{name: model.SourceLastFM, cands: cands}).
		Similar(context.Background(), model.SimilarRequest{Seed: model.SeedQuery{Query: "s"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Recommendations) != 2 {
		t.Fatalf("one artist should fill at most 2 slots, got %d", len(resp.Recommendations))
	}
}
