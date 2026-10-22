package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ayanjhunjhunwala/auris/backend/internal/engine"
	"github.com/ayanjhunjhunwala/auris/backend/internal/llm"
	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
)

type fakeLLM struct {
	chunks []string
	err    error
	got    []llm.Message
}

func (f *fakeLLM) Enabled() bool { return true }
func (f *fakeLLM) Stream(_ context.Context, msgs []llm.Message, cb llm.Callbacks) (string, *llm.Usage, error) {
	f.got = msgs
	if f.err != nil {
		return "", nil, f.err
	}
	if cb.OnReasoning != nil {
		cb.OnReasoning("thinking...")
	}
	var all strings.Builder
	for _, c := range f.chunks {
		all.WriteString(c)
		if cb.OnContent != nil {
			cb.OnContent(c)
		}
	}
	return all.String(), nil, nil
}

type fakeEngine struct{ got model.SimilarRequest }

func (f *fakeEngine) Similar(_ context.Context, r model.SimilarRequest) (*engine.Response, error) {
	f.got = r
	return &engine.Response{RequestID: "r1"}, nil
}

func TestRunStreamsProseAndRunsPlan(t *testing.T) {
	l := &fakeLLM{chunks: []string{
		"Going darker and slower, ", "pulling toward Portishead.<pl",
		`an>{"label":"Late night","steer":[{"direction":"darker","intensity":0.7},{"direction":"sideways","intensity":1}],`,
		`"targets":{"tempo":85,"energy":3},"reference":{"query":"Portishead Roads"}}</plan>`,
	}}
	eng := &fakeEngine{}
	a := &Agent{LLM: l, Engine: eng}
	track := &model.Track{Title: "Teardrop", Artist: "Massive Attack", ArtworkURL: "https://img/x.jpg", IDs: model.IDs{DeezerID: 42}}

	var events []Event
	resp, err := a.Run(context.Background(), Request{Prompt: "darker, slower, more Portishead", Track: track}, func(e Event) { events = append(events, e) })
	if err != nil || resp == nil {
		t.Fatalf("resp=%v err=%v", resp, err)
	}

	var prose strings.Builder
	types := map[string]int{}
	for _, e := range events {
		types[e.Type]++
		if e.Type == "delta" {
			prose.WriteString(e.Data.(map[string]string)["text"])
		}
	}
	if prose.String() != "Going darker and slower, pulling toward Portishead." {
		t.Fatalf("plan markup leaked into prose: %q", prose.String())
	}
	for _, want := range []string{"status", "thinking", "delta", "plan", "result"} {
		if types[want] == 0 {
			t.Errorf("missing %s event", want)
		}
	}

	r := eng.got
	if r.Seed.DeezerID != 42 {
		t.Errorf("seed should default to the right-clicked track: %+v", r.Seed)
	}
	if len(r.Steer) != 1 || r.Steer[0].Direction != "darker" {
		t.Errorf("unknown directions must be dropped: %+v", r.Steer)
	}
	if *r.Targets.Energy != 1 || *r.Targets.Tempo != 85 {
		t.Errorf("targets not clamped: %+v", r.Targets)
	}
	if r.Reference == nil || r.ReferenceWeight != 0.6 {
		t.Errorf("reference default weight: %+v %v", r.Reference, r.ReferenceWeight)
	}

	user := l.got[1].Content.([]llm.Part)
	if len(user) != 2 || user[1].ImageURL.URL != "https://img/x.jpg" || !strings.Contains(user[0].Text, "Teardrop") {
		t.Errorf("track context / artwork not sent: %+v", user)
	}
}

func TestRunErrors(t *testing.T) {
	noop := func(Event) {}
	a := &Agent{LLM: &fakeLLM{chunks: []string{"Sure!"}}, Engine: &fakeEngine{}}
	if _, err := a.Run(context.Background(), Request{Prompt: "x", Track: &model.Track{Artist: "a", Title: "b"}}, noop); err == nil {
		t.Error("missing plan should error")
	}
	a = &Agent{LLM: &fakeLLM{chunks: []string{`ok <plan>{"steer":[]}</plan>`}}, Engine: &fakeEngine{}}
	if _, err := a.Run(context.Background(), Request{Prompt: "x"}, noop); err == nil || !strings.Contains(err.Error(), "needs a song") {
		t.Errorf("no seed: %v", err)
	}
	a = &Agent{LLM: &fakeLLM{err: errors.New("boom")}, Engine: &fakeEngine{}}
	if _, err := a.Run(context.Background(), Request{Prompt: "x"}, noop); err == nil {
		t.Error("llm error should propagate")
	}
	if _, err := (&Agent{}).Run(context.Background(), Request{Prompt: "x"}, noop); err != llm.ErrDisabled {
		t.Errorf("disabled: %v", err)
	}
}

func TestParsePlanFallbacks(t *testing.T) {
	p, err := ParsePlan("Here you go:\n```json\n{\"seed\":{\"artist\":\"Air\",\"title\":\"La femme d'argent\"},\"referenceWeight\":5}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if p.Seed == nil || p.Seed.Artist != "Air" || p.ReferenceWeight != 0 {
		t.Fatalf("plan=%+v (weight must be zeroed without a reference)", p)
	}
	req := p.ToRequest(Request{Track: &model.Track{Artist: "x", Title: "y"}})
	if req.Seed.Artist != "Air" {
		t.Errorf("explicit plan seed should win over the clicked track: %+v", req.Seed)
	}
}

func TestProseFilterHoldsPartialTag(t *testing.T) {
	f := &proseFilter{}
	got := f.Write("abc <") + f.Write("p") + f.Write("x")
	if got != "abc <px" {
		t.Fatalf("got %q", got)
	}
	f = &proseFilter{}
	got = f.Write("hi <pl") + f.Write("an>{}") + f.Write("more")
	if got != "hi " || f.Flush() != "" {
		t.Fatalf("got %q", got)
	}
}
