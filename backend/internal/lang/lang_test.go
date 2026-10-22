package lang

import (
	"context"
	"testing"

	"github.com/ayanjhunjhunwala/auris/backend/internal/llm"
	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
)

func TestHeuristic(t *testing.T) {
	cases := []struct {
		title, artist, want string
		sure                bool
	}{
		{"Dynamite", "방탄소년단", "ko", true},
		{"紅蓮華", "LiSA", "zh", true}, // Han only: zh unless kana appears
		{"うっせぇわ", "Ado", "ja", true},
		{"Кукла колдуна", "Король и Шут", "ru", true},
		{"La vida es un carnaval", "Celia Cruz", "es", true},
		{"Je ne regrette rien", "Édith Piaf", "fr", true},
		{"Teardrop", "Massive Attack", "en", false},
	}
	for _, c := range cases {
		g := Heuristic(c.title, c.artist)
		if g.Code != c.want || g.Sure != c.sure {
			t.Errorf("%s: got %+v want %s/%v", c.title, g, c.want, c.sure)
		}
	}
}

type fakeLLM struct {
	reply string
	calls int
}

func (f *fakeLLM) Enabled() bool { return true }
func (f *fakeLLM) Stream(_ context.Context, _ []llm.Message, cb llm.Callbacks) (string, *llm.Usage, error) {
	f.calls++
	return f.reply, nil, nil
}

func TestDetectUsesLLMForUnsureAndCaches(t *testing.T) {
	l := &fakeLLM{reply: `{"1":"es","2":"EN"}`}
	d := NewDetector(l)
	tracks := []*model.Track{
		{Key: "bad bunny|titi me pregunto", Title: "Tití Me Preguntó", Artist: "Bad Bunny"},
		{Key: "massive attack|teardrop", Title: "Teardrop", Artist: "Massive Attack"},
		{Key: "bts|x", Title: "봄날", Artist: "BTS"},
		{Key: "aphex|x", Title: "Avril 14th", Artist: "Aphex Twin", Features: &model.Features{Instrumentalness: model.Ptr(0.9)}},
	}
	d.Detect(context.Background(), tracks)
	got := []string{tracks[0].Language, tracks[1].Language, tracks[2].Language, tracks[3].Language}
	want := []string{"es", "en", "ko", Instrumental}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("track %d: %s want %s", i, got[i], want[i])
		}
	}
	if l.calls != 1 {
		t.Fatalf("want one batched call, got %d", l.calls)
	}
	again := []*model.Track{{Key: "bad bunny|titi me pregunto", Title: "Tití Me Preguntó", Artist: "Bad Bunny"}}
	d.Detect(context.Background(), again)
	if again[0].Language != "es" || l.calls != 1 {
		t.Fatalf("not cached: %s calls=%d", again[0].Language, l.calls)
	}
}
