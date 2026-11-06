package engine

import (
	"math"
	"testing"

	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
	"github.com/ayanjhunjhunwala/auris/backend/internal/sources"
)

func track(artist, title string, ms int, ids model.IDs) model.Candidate {
	return model.Candidate{Track: model.Track{
		Key: sources.TrackKey(artist, title), Artist: artist, Title: title, DurationMs: ms, IDs: ids,
	}}
}

func seedPool() *pool {
	return newPool(model.Track{Key: sources.TrackKey("Seed", "Seed Song"), Artist: "Seed", Title: "Seed Song"})
}

func TestJaroWinklerKnownValues(t *testing.T) {
	cases := []struct {
		a, b string
		want float64
	}{
		{"martha", "marhta", 0.961},
		{"dwayne", "duane", 0.840},
		{"dixon", "dicksonx", 0.813},
		{"same", "same", 1},
		{"", "x", 0},
	}
	for _, c := range cases {
		if got := jaroWinkler(c.a, c.b); math.Abs(got-c.want) > 0.001 {
			t.Errorf("jaroWinkler(%q, %q) = %.3f, want %.3f", c.a, c.b, got, c.want)
		}
	}
}

func TestPoolMergesFoldedSpellings(t *testing.T) {
	pairs := [][2]model.Candidate{
		{track("Beyoncé", "Déjà Vu", 0, model.IDs{}), track("Beyonce", "Deja Vu", 0, model.IDs{})},
		{track("The Beatles", "Let It Be", 0, model.IDs{}), track("Beatles", "Let It Be", 0, model.IDs{})},
		{track("Simon", "Bridge Over Troubled Water & More", 0, model.IDs{}), track("Simon", "Bridge Over Troubled Water and More", 0, model.IDs{})},
	}
	for _, pr := range pairs {
		p := seedPool()
		p.add(pr[0])
		p.add(pr[1])
		if len(p.items) != 1 {
			t.Errorf("%q / %q: want 1 cluster, got %d", pr[0].Track.Title, pr[1].Track.Title, len(p.items))
		}
	}
}

func TestPoolFuzzyMatchNeedsAgreeingDuration(t *testing.T) {
	// A typo-level title difference merges when the durations agree...
	p := seedPool()
	p.add(track("Massive Attack", "Teardrop", 331000, model.IDs{}))
	p.add(track("Massive Attack", "Tear Drop", 332000, model.IDs{}))
	if len(p.items) != 1 {
		t.Fatalf("want fuzzy merge, got %d clusters", len(p.items))
	}
	// ...but not when they are clearly different recordings.
	p = seedPool()
	p.add(track("Massive Attack", "Teardrop", 331000, model.IDs{}))
	p.add(track("Massive Attack", "Tear Drop", 245000, model.IDs{}))
	if len(p.items) != 2 {
		t.Fatalf("durations 86 s apart must not merge, got %d clusters", len(p.items))
	}
	// Different songs by the same artist stay apart.
	p = seedPool()
	p.add(track("Massive Attack", "Teardrop", 331000, model.IDs{}))
	p.add(track("Massive Attack", "Angel", 379000, model.IDs{}))
	if len(p.items) != 2 {
		t.Fatalf("different songs merged")
	}
}

func TestPoolUnionsBridgedClusters(t *testing.T) {
	a := track("Artist One", "Song", 0, model.IDs{ISRC: "USAAA0000001"})
	b := track("Artist 1", "Song (Single)", 0, model.IDs{MBID: "mbid-1"})
	bridge := track("Artist One", "Song", 0, model.IDs{ISRC: "USAAA0000001", MBID: "mbid-1"})

	for _, order := range [][]model.Candidate{{a, b, bridge}, {bridge, a, b}, {b, bridge, a}} {
		p := seedPool()
		for _, c := range order {
			p.add(c)
		}
		if len(p.items) != 1 {
			t.Fatalf("order %v: want one cluster after bridging, got %d", titles(order), len(p.items))
		}
		ids := p.items[0].Track.IDs
		if ids.ISRC == "" || ids.MBID == "" {
			t.Fatalf("merged record lost IDs: %+v", ids)
		}
	}
}

func TestPoolKeepsLiveAndRemixApart(t *testing.T) {
	p := newPool(model.Track{Key: sources.TrackKey("Seed", "Seed (Live)"), Artist: "Seed", Title: "Seed (Live)"})
	p.add(track("Nirvana", "Lithium (Live)", 0, model.IDs{}))
	p.add(track("Nirvana", "Lithium", 0, model.IDs{}))
	if len(p.items) != 2 {
		t.Fatalf("live and studio merged: %d clusters", len(p.items))
	}
}

func TestPoolExcludesSeedSpelledDifferently(t *testing.T) {
	p := newPool(model.Track{Key: sources.TrackKey("Beyoncé", "Halo"), Artist: "Beyoncé", Title: "Halo"})
	p.add(track("Beyonce", "Halo", 0, model.IDs{}))
	if len(p.items) != 0 {
		t.Fatalf("seed came back as a candidate")
	}
}

func titles(cs []model.Candidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Track.Title
	}
	return out
}
