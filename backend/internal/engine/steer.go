package engine

import (
	"math"
	"strings"

	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
)

// Direction is one spoke on the dashboard: a named nudge in audio-feature
// space. Deltas are the shift applied at full intensity (1.0).
type Direction struct {
	ID          string             `json:"id"`
	Label       string             `json:"label"`
	Description string             `json:"description"`
	Deltas      map[string]float64 `json:"deltas"`
}

// Directions is the steering vocabulary. Tempo deltas are in BPM; all
// other dimensions are 0..1.
var Directions = []Direction{
	{"upbeat", "More upbeat", "Brighter, more energetic, a little faster",
		map[string]float64{"energy": 0.25, "valence": 0.3, "tempo": 12}},
	{"chill", "More chill", "Calmer, softer, slower",
		map[string]float64{"energy": -0.3, "tempo": -15, "acousticness": 0.15}},
	{"danceable", "More danceable", "Steadier groove, stronger beat",
		map[string]float64{"danceability": 0.3, "energy": 0.1}},
	{"acoustic", "More acoustic", "Organic instruments over synths",
		map[string]float64{"acousticness": 0.35, "energy": -0.1}},
	{"darker", "Darker", "Moodier and more melancholic",
		map[string]float64{"valence": -0.35, "energy": -0.05}},
	{"faster", "Faster", "Higher tempo",
		map[string]float64{"tempo": 25, "energy": 0.1}},
	{"instrumental", "More instrumental", "Fewer vocals",
		map[string]float64{"instrumentalness": 0.45, "speechiness": -0.1}},
	{"heavier", "Heavier", "Louder, denser, more intense",
		map[string]float64{"energy": 0.3, "loudness": 4, "acousticness": -0.2}},
}

func DirectionByID(id string) (Direction, bool) {
	for _, d := range Directions {
		if strings.EqualFold(d.ID, id) {
			return d, true
		}
	}
	return Direction{}, false
}

// dims is the ordered feature space used for distances. scale maps each
// dimension onto roughly 0..1 so BPM doesn't dominate; weight reflects how
// much each dimension matters perceptually for "sounds similar".
var dims = []struct {
	name   string
	scale  float64
	weight float64
	mean   float64 // population prior used when a value is unknown
}{
	{"energy", 1, 1.2, 0.62},
	{"valence", 1, 1.0, 0.48},
	{"danceability", 1, 0.9, 0.58},
	{"acousticness", 1, 0.8, 0.28},
	{"instrumentalness", 1, 0.6, 0.12},
	{"speechiness", 1, 0.5, 0.08},
	{"liveness", 1, 0.2, 0.18},
	{"tempo", 1.0 / 120, 0.7, 120},
	{"loudness", 1.0 / 30, 0.4, -8},
}

func get(f *model.Features, name string) *float64 {
	if f == nil {
		return nil
	}
	switch name {
	case "energy":
		return f.Energy
	case "valence":
		return f.Valence
	case "danceability":
		return f.Danceability
	case "acousticness":
		return f.Acousticness
	case "instrumentalness":
		return f.Instrumentalness
	case "speechiness":
		return f.Speechiness
	case "liveness":
		return f.Liveness
	case "tempo":
		return f.Tempo
	case "loudness":
		return f.Loudness
	}
	return nil
}

func set(f *model.Features, name string, v float64) {
	p := &v
	switch name {
	case "energy":
		f.Energy = p
	case "valence":
		f.Valence = p
	case "danceability":
		f.Danceability = p
	case "acousticness":
		f.Acousticness = p
	case "instrumentalness":
		f.Instrumentalness = p
	case "speechiness":
		f.Speechiness = p
	case "liveness":
		f.Liveness = p
	case "tempo":
		f.Tempo = p
	case "loudness":
		f.Loudness = p
	}
}

func clampDim(name string, v float64) float64 {
	switch name {
	case "tempo":
		return math.Max(40, math.Min(220, v))
	case "loudness":
		return math.Max(-40, math.Min(0, v))
	}
	return math.Max(0, math.Min(1, v))
}

// SteerPlan is the result of applying steer requests to a seed profile.
type SteerPlan struct {
	// Target is the full profile (unknown dims imputed with population
	// means) - used for display.
	Target *model.Features
	// Compare holds only dimensions that are real: known on the seed or
	// explicitly steered. Similarity and upstream conditioning use this so
	// imputed averages never masquerade as the user's taste.
	Compare *model.Features
	// Vector is the combined requested shift in scaled feature space; the
	// ranker rewards candidates that move along it relative to the seed.
	Vector map[string]float64
	Active bool
}

// PlanSteer computes the target profile: seed features (imputed where
// unknown) shifted by each requested direction scaled by its intensity.
func PlanSteer(seed *model.Features, steers []model.Steer) SteerPlan {
	target, compare := &model.Features{}, &model.Features{}
	for _, d := range dims {
		v := d.mean
		if p := get(seed, d.name); p != nil {
			v = *p
			set(compare, d.name, v)
		}
		set(target, d.name, v)
	}
	vec := map[string]float64{}
	for _, s := range steers {
		dir, ok := DirectionByID(s.Direction)
		if !ok {
			continue
		}
		k := math.Max(0, math.Min(1, s.Intensity))
		for name, delta := range dir.Deltas {
			next := clampDim(name, *get(target, name)+delta*k)
			set(target, name, next)
			set(compare, name, next)
			vec[name] += delta * k * scaleOf(name)
		}
	}
	return SteerPlan{Target: target, Compare: compare, Vector: vec, Active: len(vec) > 0}
}

// Override pins absolute values onto the plan. Each pinned dimension moves
// the target and adds the shift to the steering vector, so the ranker
// rewards candidates heading toward the pinned value.
func (p *SteerPlan) Override(values *model.Features) {
	if values == nil {
		return
	}
	for _, d := range dims {
		v := get(values, d.name)
		if v == nil {
			continue
		}
		cur := *get(p.Target, d.name)
		next := clampDim(d.name, *v)
		set(p.Target, d.name, next)
		set(p.Compare, d.name, next)
		if shift := (next - cur) * d.scale; shift != 0 {
			p.Vector[d.name] += shift
		}
	}
	p.Active = len(p.Vector) > 0
}

// Blend returns the profile a fraction w of the way from `from` to `to`, for
// dimensions known on `to`. Unknown dimensions of `from` use the population
// mean. Used for "make it sound like <reference>".
func Blend(from, to *model.Features, w float64) *model.Features {
	w = math.Max(0, math.Min(1, w))
	out := &model.Features{}
	for _, d := range dims {
		pt := get(to, d.name)
		if pt == nil {
			continue
		}
		f := d.mean
		if pf := get(from, d.name); pf != nil {
			f = *pf
		}
		set(out, d.name, f+w*(*pt-f))
	}
	return out
}

func scaleOf(name string) float64 {
	for _, d := range dims {
		if d.name == name {
			return d.scale
		}
	}
	return 1
}

// AudioSimilarity is a Gaussian kernel over the weighted, scaled distance
// between two profiles, in 0..1. Dimensions unknown on either side are
// skipped; ok=false means nothing was comparable.
func AudioSimilarity(a, b *model.Features) (sim float64, ok bool) {
	var dist, wsum float64
	for _, d := range dims {
		pa, pb := get(a, d.name), get(b, d.name)
		if pa == nil || pb == nil {
			continue
		}
		diff := (*pa - *pb) * d.scale
		if d.name == "tempo" {
			diff = foldedTempoDiff(*pa, *pb) * d.scale
		}
		dist += d.weight * diff * diff
		wsum += d.weight
	}
	if wsum == 0 {
		return 0, false
	}
	// sigma tuned so a typical "same genre" distance (~0.15 RMS) scores ~0.75.
	const sigma = 0.2
	return math.Exp(-(dist / wsum) / (2 * sigma * sigma)), true
}

// foldedTempoDiff treats half/double time as near-identical: BPM detectors
// routinely report 70 for a song felt at 140, and they sound alike. A small
// penalty keeps true matches ahead.
func foldedTempoDiff(a, b float64) float64 {
	d := math.Abs(a - b)
	d = math.Min(d, math.Abs(a-2*b)*1.15+4)
	d = math.Min(d, math.Abs(2*a-b)*1.15+4)
	return d
}

// SteerAlignment measures how far a candidate moves from the seed in the
// requested direction, normalised to -1..1 (1 = moved the full requested
// amount or more, negative = moved the opposite way).
func SteerAlignment(seed, cand *model.Features, vec map[string]float64) (float64, bool) {
	var dot, norm2 float64
	used := false
	for name, want := range vec {
		ps, pc := get(seed, name), get(cand, name)
		s := dimMean(name)
		if ps != nil {
			s = *ps
		}
		if pc == nil {
			continue
		}
		used = true
		moved := (*pc - s) * scaleOf(name)
		dot += moved * want
		norm2 += want * want
	}
	if !used || norm2 == 0 {
		return 0, false
	}
	return math.Max(-1, math.Min(1, dot/norm2)), true
}

func dimMean(name string) float64 {
	for _, d := range dims {
		if d.name == name {
			return d.mean
		}
	}
	return 0
}
