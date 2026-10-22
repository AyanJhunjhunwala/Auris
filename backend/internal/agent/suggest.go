package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ayanjhunjhunwala/auris/backend/internal/cache"
	"github.com/ayanjhunjhunwala/auris/backend/internal/llm"
	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
)

// Suggestion is one branch radiating from the song in the UI: a short label
// and the ready-to-run plan behind it, so taking a branch needs no second
// LLM round-trip.
type Suggestion struct {
	ID    string `json:"id"`
	Label string `json:"label"` // 1-3 words: "Darker", "Slower", "Like Portishead"
	Kind  string `json:"kind"`  // "direction" | "reference"
	Plan  Plan   `json:"plan"`
}

type SuggestResult struct {
	Suggestions []Suggestion `json:"suggestions"`
	Source      string       `json:"source"` // "llm" | "heuristic"
}

const suggestCount = 5

var (
	suggestOnce  sync.Once
	suggestCache *cache.LRU[SuggestResult]
)

// Suggest proposes suggestCount directions for a song. With an LLM it asks
// for song-specific ideas (including "like <artist>" references); without
// one, or if the model's answer is unusable, it falls back to rules over
// the song's audio features. Results are cached per track.
func (a *Agent) Suggest(ctx context.Context, t model.Track) SuggestResult {
	suggestOnce.Do(func() { suggestCache = cache.New[SuggestResult](1024, 6*time.Hour) })
	if r, ok := suggestCache.Get(t.Key); ok && t.Key != "" {
		return r
	}
	res := SuggestResult{Source: "heuristic"}
	if a.Enabled() {
		if got, err := a.suggestLLM(ctx, t); err == nil && len(got) >= 3 {
			res = SuggestResult{Suggestions: got, Source: "llm"}
		}
	}
	res.Suggestions = fill(res.Suggestions, Heuristic(t.Features))
	if t.Key != "" {
		suggestCache.Set(t.Key, res)
	}
	return res
}

func (a *Agent) suggestLLM(ctx context.Context, t model.Track) ([]Suggestion, error) {
	var user strings.Builder
	fmt.Fprintf(&user, "Song: %q by %s.\n", t.Title, t.Artist)
	if f := t.Features; !f.IsEmpty() {
		b, _ := json.Marshal(f)
		fmt.Fprintf(&user, "Audio features: %s\n", b)
	}
	parts := []llm.Part{{Type: "text", Text: user.String()}}
	if strings.HasPrefix(t.ArtworkURL, "https://") {
		parts = append(parts, llm.Part{Type: "image_url", ImageURL: &llm.ImageURL{URL: t.ArtworkURL}})
	}
	msgs := []llm.Message{
		{Role: "system", Content: suggestPrompt()},
		{Role: "user", Content: parts},
	}
	noop := func(string) {}
	text, _, err := a.LLM.Stream(ctx, msgs, llm.Callbacks{OnContent: noop, OnReasoning: noop})
	if err != nil {
		return nil, err
	}
	return ParseSuggestions(text)
}

func suggestPrompt() string {
	return systemPrompt() + `

TASK OVERRIDE: do not answer with prose or a <plan> tag. Instead propose exactly
5 directions a listener might want to explore from this song, tailored to it:
3 or 4 sonic moves (e.g. darker, slower, faster, stripped back) and 1 or 2
references to real artists or songs it could move toward. Respond with ONLY:
{"suggestions":[{"label":"Darker","plan":{...}}, ...]}
Each label is 1-3 words, no punctuation, e.g. "Darker", "Slower", "Like Burial".
Each plan uses the plan fields described above.`
}

// ParseSuggestions extracts and validates the model's JSON.
func ParseSuggestions(text string) ([]Suggestion, error) {
	i, j := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if i < 0 || j <= i {
		return nil, fmt.Errorf("no JSON in suggestion response")
	}
	var raw struct {
		Suggestions []struct {
			Label string `json:"label"`
			Plan  Plan   `json:"plan"`
		} `json:"suggestions"`
	}
	if err := json.Unmarshal([]byte(text[i:j+1]), &raw); err != nil {
		return nil, err
	}
	var out []Suggestion
	for _, s := range raw.Suggestions {
		label := tidyLabel(s.Label)
		p := s.Plan.sanitize()
		if label == "" || (len(p.Steer) == 0 && p.Targets == nil && p.Reference == nil) {
			continue
		}
		p.Label = label
		kind := "direction"
		if p.Reference != nil {
			kind = "reference"
		}
		out = append(out, Suggestion{Label: label, Kind: kind, Plan: p})
	}
	return out, nil
}

// fill dedupes by label, tops up from fallbacks and assigns stable IDs.
func fill(primary, fallback []Suggestion) []Suggestion {
	seen := map[string]bool{}
	var out []Suggestion
	for _, s := range append(primary, fallback...) {
		k := strings.ToLower(s.Label)
		if seen[k] || len(out) == suggestCount {
			continue
		}
		seen[k] = true
		s.ID = slug(s.Label)
		out = append(out, s)
	}
	return out
}

// Heuristic proposes directions with the most room to move, given what the
// song already is. A bright song suggests "Darker"; a sparse one "Heavier".
func Heuristic(f *model.Features) []Suggestion {
	val := func(p *float64, def float64) float64 {
		if p == nil {
			return def
		}
		return *p
	}
	var valence, energy, tempo, acoustic float64 = 0.5, 0.6, 120, 0.3
	if f != nil {
		valence, energy, tempo, acoustic = val(f.Valence, 0.5), val(f.Energy, 0.6), val(f.Tempo, 120), val(f.Acousticness, 0.3)
	}
	dir := func(label, direction string, k float64) Suggestion {
		return Suggestion{Label: label, Kind: "direction", Plan: Plan{Label: label, Steer: []model.Steer{{Direction: direction, Intensity: k}}}}
	}
	tgt := func(label string, t *model.Features) Suggestion {
		return Suggestion{Label: label, Kind: "direction", Plan: Plan{Label: label, Targets: t}}
	}

	out := make([]Suggestion, 0, suggestCount)
	if valence > 0.45 {
		out = append(out, dir("Darker", "darker", 0.8))
	} else {
		out = append(out, dir("Brighter", "upbeat", 0.7))
	}
	out = append(out,
		tgt("Slower", &model.Features{Tempo: model.Ptr(clamp(tempo-25, 40, 220)), Energy: model.Ptr(clamp(energy-0.15, 0, 1))}),
		dir("Faster", "faster", 0.8),
	)
	if energy > 0.55 {
		out = append(out, dir("Softer", "chill", 0.7))
	} else {
		out = append(out, dir("Heavier", "heavier", 0.7))
	}
	if acoustic < 0.4 {
		out = append(out, dir("Acoustic", "acoustic", 0.8))
	} else {
		out = append(out, dir("Danceable", "danceable", 0.8))
	}
	return out
}

func tidyLabel(s string) string {
	s = strings.TrimSpace(strings.Trim(s, `."'!`))
	if words := strings.Fields(s); len(words) > 3 {
		s = strings.Join(words[:3], " ")
	}
	if len(s) > 28 {
		s = s[:28]
	}
	r := []rune(s)
	if len(r) > 0 {
		r[0] = unicode.ToUpper(r[0])
	}
	return string(r)
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
