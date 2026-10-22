// Package learn is Auris' ranking model: a logistic regression over the
// signals the engine computes for each candidate, trained online from user
// feedback (like / skip).
//
// It starts from hand-tuned priors, so it ranks sensibly with zero data, and
// each feedback event nudges the weights with one SGD step. Regularisation
// pulls weights back toward the priors (not toward zero), so a handful of
// clicks personalises the ranking without being able to wreck it.
package learn

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Signal names. The engine fills these; anything else is ignored.
const (
	SigLastFM       = "lastfm"
	SigListenBrainz = "listenbrainz"
	SigReccoBeats   = "reccobeats"
	SigDeezer       = "deezer"
	SigFusion       = "fusion"          // reciprocal-rank fusion across sources, 0..1
	SigConsensus    = "consensus"       // share of active sources that agree, 0..1
	SigAudioMatch   = "audio_match"     // closeness to the (steered) target profile, 0..1
	SigSteerAlign   = "steer_align"     // movement along the requested direction, -1..1
	SigPopularity   = "popularity"      // 0..1
	SigSameArtist   = "same_artist"     // 1 if same lead artist as the seed
	SigAudioKnown   = "audio_known"     // 1 if we had real features for the track
	SigArtistAffin  = "artist_affinity" // how similar the candidate's artist is to the seed's, 0..1
)

// Priors encode what we believe before any feedback: listening-behaviour
// sources beat content similarity, agreement between independent sources is
// strong evidence, and an explicit steer request should dominate.
var Priors = map[string]float64{
	SigLastFM:       1.6,
	SigListenBrainz: 1.4,
	SigReccoBeats:   1.0,
	SigDeezer:       0.4, // related-artist top tracks skew to generic hits
	SigFusion:       1.5,
	SigConsensus:    1.2,
	SigAudioMatch:   1.5,
	SigSteerAlign:   2.0,
	SigPopularity:   0.1, // a light tiebreak; heavier just recommends the charts
	SigSameArtist:   -0.4,
	SigAudioKnown:   0.1,
	SigArtistAffin:  1.3,
}

const priorBias = -3.0

type state struct {
	Weights   map[string]float64 `json:"weights"`
	Bias      float64            `json:"bias"`
	Updates   int                `json:"updates"`
	Likes     int                `json:"likes"`
	Skips     int                `json:"skips"`
	UpdatedAt time.Time          `json:"updatedAt"`
}

type Ranker struct {
	mu   sync.RWMutex
	s    state
	lr   float64
	l2   float64
	path string
}

// New loads weights from path if present, otherwise starts from priors.
func New(path string, learningRate float64) *Ranker {
	r := &Ranker{lr: learningRate, l2: 0.02, path: path, s: state{Weights: map[string]float64{}, Bias: priorBias}}
	for k, v := range Priors {
		r.s.Weights[k] = v
	}
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			var loaded state
			if json.Unmarshal(b, &loaded) == nil && loaded.Weights != nil {
				for k := range Priors { // only accept known signals
					if v, ok := loaded.Weights[k]; ok && !math.IsNaN(v) && !math.IsInf(v, 0) {
						r.s.Weights[k] = v
					}
				}
				r.s.Bias, r.s.Updates, r.s.Likes, r.s.Skips, r.s.UpdatedAt =
					loaded.Bias, loaded.Updates, loaded.Likes, loaded.Skips, loaded.UpdatedAt
			}
		}
	}
	return r
}

// Score returns the logit and the probability-like score in (0,1).
func (r *Ranker) Score(signals map[string]float64) (logit, p float64) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	logit = r.s.Bias
	for k, x := range signals {
		logit += r.s.Weights[k] * x
	}
	return logit, sigmoid(logit)
}

// Update applies one SGD step on the log-loss for a labelled example
// (liked=true -> 1, skipped -> 0) and persists the new weights.
func (r *Ranker) Update(signals map[string]float64, liked bool) error {
	y := 0.0
	if liked {
		y = 1
	}
	r.mu.Lock()
	logit := r.s.Bias
	for k, x := range signals {
		logit += r.s.Weights[k] * x
	}
	g := y - sigmoid(logit) // gradient of log-likelihood w.r.t. logit
	for k, prior := range Priors {
		x := signals[k]
		w := r.s.Weights[k]
		r.s.Weights[k] = w + r.lr*(g*x-r.l2*(w-prior))
	}
	r.s.Bias += r.lr * (g - r.l2*(r.s.Bias-priorBias))
	r.s.Updates++
	if liked {
		r.s.Likes++
	} else {
		r.s.Skips++
	}
	r.s.UpdatedAt = time.Now()
	snapshot := r.copyLocked()
	r.mu.Unlock()
	return r.save(snapshot)
}

// Reset restores the priors.
func (r *Ranker) Reset() error {
	r.mu.Lock()
	r.s = state{Weights: map[string]float64{}, Bias: priorBias, UpdatedAt: time.Now()}
	for k, v := range Priors {
		r.s.Weights[k] = v
	}
	snapshot := r.copyLocked()
	r.mu.Unlock()
	return r.save(snapshot)
}

// Weight returns the current weight for a signal.
func (r *Ranker) Weight(signal string) float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.s.Weights[signal]
}

// Snapshot exposes the model for the telemetry panel.
func (r *Ranker) Snapshot() map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	type wv struct {
		Signal string  `json:"signal"`
		Weight float64 `json:"weight"`
		Prior  float64 `json:"prior"`
	}
	ws := make([]wv, 0, len(r.s.Weights))
	for k, v := range r.s.Weights {
		ws = append(ws, wv{k, round(v), Priors[k]})
	}
	sort.Slice(ws, func(i, j int) bool { return ws[i].Signal < ws[j].Signal })
	return map[string]any{
		"weights": ws, "bias": round(r.s.Bias), "updates": r.s.Updates,
		"likes": r.s.Likes, "skips": r.s.Skips, "updatedAt": r.s.UpdatedAt,
	}
}

func (r *Ranker) copyLocked() state {
	c := r.s
	c.Weights = make(map[string]float64, len(r.s.Weights))
	for k, v := range r.s.Weights {
		c.Weights[k] = v
	}
	return c
}

func (r *Ranker) save(s state) error {
	if r.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

func sigmoid(x float64) float64 { return 1 / (1 + math.Exp(-x)) }

func round(v float64) float64 { return math.Round(v*1000) / 1000 }
