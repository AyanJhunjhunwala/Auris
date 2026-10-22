package learn

import (
	"path/filepath"
	"testing"
)

func TestPriorsRankSensiblyWithoutData(t *testing.T) {
	r := New("", 0.05)
	_, strong := r.Score(map[string]float64{SigLastFM: 1, SigListenBrainz: 1, SigConsensus: 1, SigFusion: 1, SigAudioMatch: 0.9})
	_, weak := r.Score(map[string]float64{SigDeezer: 0.4, SigAudioMatch: 0.5})
	if strong <= weak {
		t.Fatalf("multi-source evidence should outrank a lone weak source: %v vs %v", strong, weak)
	}
}

func TestLikesAndSkipsMoveWeightsAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "weights.json")
	r := New(path, 0.2)
	ex := map[string]float64{SigReccoBeats: 1, SigAudioMatch: 1}
	for range 20 {
		if err := r.Update(ex, true); err != nil {
			t.Fatal(err)
		}
	}
	if r.Weight(SigReccoBeats) <= Priors[SigReccoBeats] {
		t.Fatal("likes should raise the weight of signals present in liked items")
	}
	if r.Weight(SigLastFM) != Priors[SigLastFM] {
		t.Fatal("absent signals should stay at their prior")
	}

	reloaded := New(path, 0.2)
	if reloaded.Weight(SigReccoBeats) != r.Weight(SigReccoBeats) {
		t.Fatal("weights did not persist")
	}

	for range 40 {
		_ = reloaded.Update(ex, false)
	}
	if reloaded.Weight(SigReccoBeats) >= r.Weight(SigReccoBeats) {
		t.Fatal("skips should lower the weight again")
	}
	if err := reloaded.Reset(); err != nil || reloaded.Weight(SigReccoBeats) != Priors[SigReccoBeats] {
		t.Fatal("reset should restore priors")
	}
}

func TestRegularisationBoundsDrift(t *testing.T) {
	r := New("", 0.5)
	for range 2000 {
		_ = r.Update(map[string]float64{SigDeezer: 1}, false)
	}
	// L2 toward the prior keeps a flood of skips from sending the weight to -inf.
	if w := r.Weight(SigDeezer); w < -30 {
		t.Fatalf("weight drifted unbounded: %v", w)
	}
}
