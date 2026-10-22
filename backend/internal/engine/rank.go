package engine

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/ayanjhunjhunwala/auris/backend/internal/learn"
	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
	"github.com/ayanjhunjhunwala/auris/backend/internal/sources"
)

// rrfK is the standard Reciprocal Rank Fusion constant: large enough that
// the exact rank inside a long list matters less than appearing at all.
const rrfK = 60

// fusion computes raw RRF for a candidate: sum over sources of 1/(k+rank).
func fusion(c *model.Candidate) float64 {
	s := 0.0
	for _, e := range c.Evidence {
		s += 1 / float64(rrfK+e.Rank+1)
	}
	return s
}

// signals builds the feature vector the ranker scores. Every value is
// bounded so learned weights stay comparable.
func signals(c *model.Candidate, seed model.Track, plan SteerPlan, fusionMax float64, activeSources int, affinity map[string]float64) map[string]float64 {
	sig := map[string]float64{}
	if a, ok := affinity[sources.PrimaryArtist(c.Track.Artist)]; ok {
		sig[learn.SigArtistAffin] = a
	}
	for _, e := range c.Evidence {
		sig[string(e.Source)] = e.Score
	}
	if fusionMax > 0 {
		sig[learn.SigFusion] = fusion(c) / fusionMax
	}
	if activeSources > 1 {
		sig[learn.SigConsensus] = float64(len(c.Evidence)-1) / float64(activeSources-1)
	}

	// Audio: full credit needs real energy/valence-type features; a lone
	// Deezer BPM is weak evidence.
	f := c.Track.Features
	switch {
	case f != nil && f.Energy != nil:
		sig[learn.SigAudioKnown] = 1
	case !f.IsEmpty():
		sig[learn.SigAudioKnown] = 0.3
	}
	if sim, ok := AudioSimilarity(plan.Compare, f); ok {
		sig[learn.SigAudioMatch] = sim
	} else {
		sig[learn.SigAudioMatch] = 0.5 // unknown: neither reward nor punish
	}
	if plan.Active {
		if a, ok := SteerAlignment(seed.Features, f, plan.Vector); ok {
			sig[learn.SigSteerAlign] = a
		}
	}

	if c.Track.Popularity != nil {
		sig[learn.SigPopularity] = *c.Track.Popularity
	} else {
		sig[learn.SigPopularity] = 0.3
	}
	if sources.PrimaryArtist(c.Track.Artist) == sources.PrimaryArtist(seed.Artist) {
		sig[learn.SigSameArtist] = 1
	}
	return sig
}

type scored struct {
	c      *model.Candidate
	sig    map[string]float64
	logit  float64
	score  float64
	artist string
}

// rank scores the pool with the learned model, then diversifies.
func rank(items []*model.Candidate, seed model.Track, plan SteerPlan, r *learn.Ranker, activeSources, limit int, affinity map[string]float64) []model.Recommendation {
	fusionMax := 0.0
	for _, c := range items {
		fusionMax = math.Max(fusionMax, fusion(c))
	}
	all := make([]scored, 0, len(items))
	for _, c := range items {
		sig := signals(c, seed, plan, fusionMax, activeSources, affinity)
		logit, p := r.Score(sig)
		all = append(all, scored{c: c, sig: sig, logit: logit, score: p, artist: sources.PrimaryArtist(c.Track.Artist)})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].score > all[j].score })

	picked := diversify(all, limit, 0.8)
	out := make([]model.Recommendation, 0, len(picked))
	for _, s := range picked {
		out = append(out, model.Recommendation{
			Track:     s.c.Track,
			Score:     math.Round(s.score*1000) / 1000,
			Evidence:  s.c.Evidence,
			Breakdown: model.Breakdown{Signals: roundAll(s.sig), Logit: math.Round(s.logit*1000) / 1000},
			Reasons:   reasons(s, plan),
		})
	}
	return out
}

// diversify is Maximal Marginal Relevance: repeatedly take the candidate
// that maximises lambda*relevance - (1-lambda)*redundancy, where redundancy
// is similarity to anything already picked (same artist counts as near-
// duplicate). Without it, one artist with five hits fills the top ten.
func diversify(ranked []scored, limit int, lambda float64) []scored {
	if limit <= 0 || limit > len(ranked) {
		limit = len(ranked)
	}
	// Only consider the head of the list; MMR is O(n*k).
	head := ranked[:min(len(ranked), limit*4)]
	used := make([]bool, len(head))
	perArtist := map[string]int{}
	out := make([]scored, 0, limit)
	for len(out) < limit {
		best, bestVal := -1, math.Inf(-1)
		for i, s := range head {
			if used[i] || perArtist[s.artist] >= maxPerArtist {
				continue
			}
			red := 0.0
			for _, p := range out {
				red = math.Max(red, redundancy(s, p))
			}
			if v := lambda*s.score - (1-lambda)*red; v > bestVal {
				best, bestVal = i, v
			}
		}
		if best < 0 {
			break
		}
		used[best] = true
		perArtist[head[best].artist]++
		out = append(out, head[best])
	}
	return out
}

// maxPerArtist hard-caps one artist's share of a result list; MMR alone
// still lets a dominant artist take three or four slots.
const maxPerArtist = 2

func redundancy(a, b scored) float64 {
	if a.artist == b.artist {
		return 1
	}
	if sim, ok := AudioSimilarity(a.c.Track.Features, b.c.Track.Features); ok {
		return 0.35 * sim
	}
	return 0
}

func reasons(s scored, plan SteerPlan) []string {
	var out []string
	ev := append([]model.Evidence(nil), s.c.Evidence...)
	sort.Slice(ev, func(i, j int) bool { return ev[i].Score > ev[j].Score })
	for _, e := range ev {
		if e.Reason != "" {
			out = append(out, e.Reason)
		}
	}
	if n := len(s.c.Evidence); n > 1 {
		out = append(out, fmt.Sprintf("%d independent sources agree", n))
	}
	if s.sig[learn.SigAudioKnown] == 1 {
		out = append(out, fmt.Sprintf("Audio profile %d%% match", int(math.Round(s.sig[learn.SigAudioMatch]*100))))
	}
	if plan.Active {
		if a := s.sig[learn.SigSteerAlign]; a > 0.25 {
			out = append(out, "Moves the way you steered ("+steerSummary(plan.Vector)+")")
		}
	}
	return out
}

func steerSummary(vec map[string]float64) string {
	keys := make([]string, 0, len(vec))
	for k := range vec {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return math.Abs(vec[keys[i]]) > math.Abs(vec[keys[j]]) })
	parts := []string{}
	for _, k := range keys[:min(2, len(keys))] {
		sign := "+"
		if vec[k] < 0 {
			sign = "-"
		}
		parts = append(parts, sign+k)
	}
	return strings.Join(parts, ", ")
}

func roundAll(m map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[k] = math.Round(v*1000) / 1000
	}
	return out
}
