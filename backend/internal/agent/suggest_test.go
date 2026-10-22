package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
)

func TestSuggestFromLLM(t *testing.T) {
	l := &fakeLLM{chunks: []string{`Sure: {"suggestions":[
		{"label":"Darker.","plan":{"steer":[{"direction":"darker","intensity":0.8}]}},
		{"label":"Slower","plan":{"targets":{"tempo":80}}},
		{"label":"Like Portishead","plan":{"reference":{"query":"Portishead"}}},
		{"label":"Nonsense","plan":{"steer":[{"direction":"sideways","intensity":1}]}},
		{"label":"darker","plan":{"steer":[{"direction":"darker","intensity":0.3}]}}
	]}`}}
	a := &Agent{LLM: l}
	res := a.Suggest(context.Background(), model.Track{Key: "llm|song", Title: "Song", Artist: "Artist"})
	if res.Source != "llm" || len(res.Suggestions) != 5 {
		t.Fatalf("res=%+v", res)
	}
	if res.Suggestions[0].Label != "Darker" || res.Suggestions[0].ID != "darker" {
		t.Errorf("label not tidied: %+v", res.Suggestions[0])
	}
	ref := res.Suggestions[2]
	if ref.Kind != "reference" || ref.Plan.ReferenceWeight != 0.6 || ref.ID != "like-portishead" {
		t.Errorf("reference suggestion: %+v", ref)
	}
	for _, s := range res.Suggestions {
		if s.Label == "Nonsense" {
			t.Error("plan with only unknown directions must be dropped")
		}
	}
	// Topped up from heuristics without repeating "Darker".
	labels := map[string]int{}
	for _, s := range res.Suggestions {
		labels[s.Label]++
	}
	if labels["Darker"] != 1 {
		t.Errorf("duplicate labels: %v", labels)
	}

	// Cached: a second call must not hit the model.
	l.err = errors.New("should not be called")
	if again := a.Suggest(context.Background(), model.Track{Key: "llm|song"}); again.Source != "llm" {
		t.Errorf("not cached: %+v", again)
	}
}

func TestSuggestFallsBackToHeuristics(t *testing.T) {
	a := &Agent{LLM: &fakeLLM{err: errors.New("down")}}
	res := a.Suggest(context.Background(), model.Track{Key: "fallback|song", Features: &model.Features{
		Valence: model.Ptr(0.2), Energy: model.Ptr(0.3), Tempo: model.Ptr(100.0), Acousticness: model.Ptr(0.7),
	}})
	if res.Source != "heuristic" || len(res.Suggestions) != 5 {
		t.Fatalf("res=%+v", res)
	}
	want := []string{"Brighter", "Slower", "Faster", "Heavier", "Danceable"}
	for i, w := range want {
		if res.Suggestions[i].Label != w {
			t.Errorf("suggestion %d = %q want %q", i, res.Suggestions[i].Label, w)
		}
	}
	if *res.Suggestions[1].Plan.Targets.Tempo != 75 {
		t.Errorf("slower should target seed tempo - 25: %v", *res.Suggestions[1].Plan.Targets.Tempo)
	}
	if got := (&Agent{}).Suggest(context.Background(), model.Track{}); len(got.Suggestions) != 5 {
		t.Errorf("no LLM, no features: %+v", got)
	}
}
