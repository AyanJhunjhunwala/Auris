// Package agent turns a natural-language request about a song ("make this
// sound darker and slower", "like this but more Tame Impala") into a
// structured plan the engine understands, then runs it.
//
// The model answers in two parts: a short reply to the user, then a
// <plan>{json}</plan> block. The reply streams to the UI token by token;
// the plan is parsed, validated and clamped server-side, so a confused model
// can never send the engine out-of-range values.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/ayanjhunjhunwala/auris/backend/internal/engine"
	"github.com/ayanjhunjhunwala/auris/backend/internal/llm"
	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
)

// LLM is the slice of llm.Client the agent needs (faked in tests).
type LLM interface {
	Enabled() bool
	Stream(ctx context.Context, msgs []llm.Message, cb llm.Callbacks) (string, *llm.Usage, error)
}

// Recommender is the slice of engine.Engine the agent needs.
type Recommender interface {
	Similar(ctx context.Context, req model.SimilarRequest) (*engine.Response, error)
}

// Request is the body of POST /api/agent.
type Request struct {
	Prompt string        `json:"prompt"`
	Track  *model.Track  `json:"track,omitempty"` // the song that was right-clicked
	Steer  []model.Steer `json:"steer,omitempty"` // what's already applied, for context
	Limit  int           `json:"limit,omitempty"`
}

// Plan is what the model is asked to produce.
type Plan struct {
	Label           string           `json:"label,omitempty"`
	Seed            *model.SeedQuery `json:"seed,omitempty"`
	Steer           []model.Steer    `json:"steer,omitempty"`
	Targets         *model.Features  `json:"targets,omitempty"`
	Reference       *model.SeedQuery `json:"reference,omitempty"`
	ReferenceWeight float64          `json:"referenceWeight,omitempty"`
}

// Event is one item on the agent's output stream.
type Event struct {
	Type string `json:"type"` // status | thinking | delta | plan | result | error
	Data any    `json:"data"`
}

type Agent struct {
	LLM    LLM
	Engine Recommender
}

func (a *Agent) Enabled() bool { return a.LLM != nil && a.LLM.Enabled() }

// Run executes one agent turn, emitting events as it goes. It returns the
// final response (nil on failure); errors are also emitted as events.
func (a *Agent) Run(ctx context.Context, req Request, emit func(Event)) (*engine.Response, error) {
	if !a.Enabled() {
		return nil, llm.ErrDisabled
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, errors.New("prompt is empty")
	}
	emit(Event{"status", map[string]string{"stage": "thinking"}})

	filter := &proseFilter{}
	thinkingSent := 0
	text, _, err := a.LLM.Stream(ctx, buildMessages(req), llm.Callbacks{
		OnContent: func(s string) {
			if out := filter.Write(s); out != "" {
				emit(Event{"delta", map[string]string{"text": out}})
			}
		},
		OnReasoning: func(s string) {
			// Only a heartbeat: the UI pulses while the model thinks.
			thinkingSent += len(s)
			emit(Event{"thinking", map[string]int{"chars": thinkingSent}})
		},
	})
	if err != nil {
		return nil, err
	}
	if rest := filter.Flush(); rest != "" {
		emit(Event{"delta", map[string]string{"text": rest}})
	}

	plan, err := ParsePlan(text)
	if err != nil {
		return nil, err
	}
	sreq := plan.ToRequest(req)
	if sreq.Seed == (model.SeedQuery{}) {
		return nil, errors.New("the agent needs a song to work from: right-click a track or name one")
	}
	emit(Event{"plan", plan})
	emit(Event{"status", map[string]string{"stage": "searching"}})

	resp, err := a.Engine.Similar(ctx, sreq)
	if err != nil {
		return nil, err
	}
	emit(Event{"result", resp})
	return resp, nil
}

// ToRequest maps a validated plan onto an engine request, defaulting the
// seed to the right-clicked track.
func (p Plan) ToRequest(r Request) model.SimilarRequest {
	sr := model.SimilarRequest{
		Limit: r.Limit, Steer: p.Steer, Targets: p.Targets,
		Reference: p.Reference, ReferenceWeight: p.ReferenceWeight,
	}
	switch {
	case p.Seed != nil && (p.Seed.Query != "" || (p.Seed.Artist != "" && p.Seed.Title != "")):
		sr.Seed = *p.Seed
	case r.Track != nil && r.Track.IDs.DeezerID != 0:
		sr.Seed = model.SeedQuery{DeezerID: r.Track.IDs.DeezerID}
	case r.Track != nil && r.Track.Artist != "" && r.Track.Title != "":
		sr.Seed = model.SeedQuery{Artist: r.Track.Artist, Title: r.Track.Title, ISRC: r.Track.IDs.ISRC}
	}
	if sr.Limit <= 0 {
		sr.Limit = 24
	}
	return sr
}

// ---------------------------------------------------------------------------
// Prompting

func buildMessages(r Request) []llm.Message {
	var user strings.Builder
	if t := r.Track; t != nil {
		fmt.Fprintf(&user, "The user right-clicked: %q by %s", t.Title, t.Artist)
		if t.Album != "" {
			fmt.Fprintf(&user, " (album %q)", t.Album)
		}
		user.WriteString(".\n")
		if f := t.Features; !f.IsEmpty() {
			b, _ := json.Marshal(f)
			fmt.Fprintf(&user, "Its audio features: %s\n", b)
		}
	} else {
		user.WriteString("No track is selected.\n")
	}
	if len(r.Steer) > 0 {
		b, _ := json.Marshal(r.Steer)
		fmt.Fprintf(&user, "Steering already applied: %s\n", b)
	}
	fmt.Fprintf(&user, "\nUser request: %s", r.Prompt)

	parts := []llm.Part{{Type: "text", Text: user.String()}}
	// The model is multimodal; the cover often says a lot about the vibe.
	if r.Track != nil && strings.HasPrefix(r.Track.ArtworkURL, "https://") {
		parts = append(parts, llm.Part{Type: "image_url", ImageURL: &llm.ImageURL{URL: r.Track.ArtworkURL}})
	}
	return []llm.Message{
		{Role: "system", Content: systemPrompt()},
		{Role: "user", Content: parts},
	}
}

func systemPrompt() string {
	var dirs strings.Builder
	for _, d := range engine.Directions {
		b, _ := json.Marshal(d.Deltas)
		fmt.Fprintf(&dirs, "- %s: %s %s\n", d.ID, d.Description, b)
	}
	return `You are Auris, a music-discovery agent. The user right-clicks a song and
describes how they want it to sound. You translate that into a search plan.

Audio features (Spotify-style): energy, valence (happiness), danceability,
acousticness, instrumentalness, speechiness, liveness are 0..1; tempo is BPM
(40-220); loudness is dB (-40..0).

Named steering directions (intensity 0..1 scales these deltas):
` + dirs.String() + `
Tools in a plan, combine as needed:
- "steer": named directions with intensity, for relative changes ("a bit darker").
- "targets": absolute feature values, for specific asks ("around 90 bpm", "fully acoustic").
- "reference": {"query": "artist title"} when they name another song or artist to
  sound like; "referenceWeight" 0..1 is how strongly to pull toward it (default 0.6).
- "seed": only if they want to start from a different song than the one clicked.
- "label": a 2-4 word name for the resulting vibe.

Respond with ONE or TWO short sentences to the user (no markdown), then the plan
exactly like this, with nothing after it:
<plan>{"label":"...","steer":[{"direction":"chill","intensity":0.6}],"targets":{"tempo":90},"reference":null,"referenceWeight":0.6,"seed":null}</plan>
Omit fields you don't need. Never invent directions that aren't listed.`
}

// ---------------------------------------------------------------------------
// Parsing

// ParsePlan extracts and validates the <plan> block (falling back to the
// last JSON object in the text if the tags are missing).
func ParsePlan(text string) (Plan, error) {
	raw := ""
	if i := strings.Index(text, "<plan>"); i >= 0 {
		raw = text[i+len("<plan>"):]
		if j := strings.Index(raw, "</plan>"); j >= 0 {
			raw = raw[:j]
		}
	} else if i, j := strings.Index(text, "{"), strings.LastIndex(text, "}"); i >= 0 && j > i {
		raw = text[i : j+1]
	}
	raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(raw), "```json"), "```"))
	if raw == "" {
		return Plan{}, errors.New("the agent did not return a plan")
	}
	var p Plan
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return Plan{}, fmt.Errorf("the agent returned an unreadable plan: %w", err)
	}
	return p.sanitize(), nil
}

func (p Plan) sanitize() Plan {
	clean := p.Steer[:0:0]
	seen := map[string]bool{}
	for _, s := range p.Steer {
		d, ok := engine.DirectionByID(s.Direction)
		if !ok || seen[d.ID] {
			continue
		}
		seen[d.ID] = true
		clean = append(clean, model.Steer{Direction: d.ID, Intensity: clamp(s.Intensity, 0, 1)})
	}
	p.Steer = clean
	if t := p.Targets; t != nil {
		for _, f := range []**float64{&t.Energy, &t.Valence, &t.Danceability, &t.Acousticness,
			&t.Instrumentalness, &t.Speechiness, &t.Liveness} {
			if *f != nil {
				*f = model.Ptr(clamp(**f, 0, 1))
			}
		}
		if t.Tempo != nil {
			t.Tempo = model.Ptr(clamp(*t.Tempo, 40, 220))
		}
		if t.Loudness != nil {
			t.Loudness = model.Ptr(clamp(*t.Loudness, -40, 0))
		}
		t.Key, t.Mode = nil, nil
		if t.IsEmpty() {
			p.Targets = nil
		}
	}
	if r := p.Reference; r != nil && strings.TrimSpace(r.Query) == "" && (r.Artist == "" || r.Title == "") {
		p.Reference = nil
	}
	if p.Reference != nil {
		if p.ReferenceWeight <= 0 {
			p.ReferenceWeight = 0.6
		}
		p.ReferenceWeight = clamp(p.ReferenceWeight, 0.1, 1)
	} else {
		p.ReferenceWeight = 0
	}
	if len(p.Label) > 40 {
		p.Label = p.Label[:40]
	}
	return p
}

func clamp(v, lo, hi float64) float64 {
	if math.IsNaN(v) {
		return lo
	}
	return math.Max(lo, math.Min(hi, v))
}

// proseFilter forwards streamed text until "<plan>" appears, holding back
// any tail that could be the start of the tag so it never flashes on screen.
type proseFilter struct {
	buf  string
	done bool
}

const planTag = "<plan>"

func (f *proseFilter) Write(s string) string {
	if f.done {
		return ""
	}
	f.buf += s
	if i := strings.Index(f.buf, planTag); i >= 0 {
		out := f.buf[:i]
		f.buf, f.done = "", true
		return out
	}
	hold := 0
	for k := min(len(planTag)-1, len(f.buf)); k > 0; k-- {
		if strings.HasSuffix(f.buf, planTag[:k]) {
			hold = k
			break
		}
	}
	out := f.buf[:len(f.buf)-hold]
	f.buf = f.buf[len(f.buf)-hold:]
	return out
}

// Flush releases held-back text if the stream ended without a plan tag.
func (f *proseFilter) Flush() string {
	if f.done {
		return ""
	}
	out := f.buf
	f.buf = ""
	return out
}
