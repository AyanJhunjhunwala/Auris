package sources

import (
	"encoding/json"
	"testing"
)

func TestTrackKeyMergesProviderVariants(t *testing.T) {
	same := [][2][2]string{
		{{"The Weeknd", "Blinding Lights"}, {"The Weeknd", "Blinding Lights - 2020 Remaster"}},
		{{"Drake feat. Rihanna", "Take Care"}, {"Drake", "Take Care (feat. Rihanna)"}},
		{{"Daft Punk, Pharrell Williams", "Get Lucky"}, {"Daft Punk & Pharrell Williams", "Get Lucky [Radio Edit]"}},
		{{"Beyoncé", "Halo"}, {"BEYONCÉ", "Halo!"}},
	}
	for _, c := range same {
		a, b := TrackKey(c[0][0], c[0][1]), TrackKey(c[1][0], c[1][1])
		if a != b {
			t.Errorf("expected same key: %q vs %q", a, b)
		}
	}
	different := [][2][2]string{
		{{"Avicii", "Levels"}, {"Avicii", "Levels (Skrillex Remix)"}},
		{{"Nirvana", "Lithium"}, {"Nirvana", "Lithium (Live)"}},
	}
	for _, c := range different {
		if TrackKey(c[0][0], c[0][1]) == TrackKey(c[1][0], c[1][1]) {
			t.Errorf("remix/live should be distinct: %v", c)
		}
	}
}

func TestSpotifyIDFromURL(t *testing.T) {
	cases := map[string]string{
		"https://open.spotify.com/track/00aqkszH1FdUiJJWvX6iEl":        "00aqkszH1FdUiJJWvX6iEl",
		"https://open.spotify.com/track/00aqkszH1FdUiJJWvX6iEl?si=abc": "00aqkszH1FdUiJJWvX6iEl",
		"https://open.spotify.com/artist/1Xyo4u8uXC1ZmMpatF05PJ":       "",
		"": "",
	}
	for in, want := range cases {
		if got := SpotifyIDFromURL(in); got != want {
			t.Errorf("SpotifyIDFromURL(%q)=%q want %q", in, got, want)
		}
	}
}

func TestNumAcceptsStringsAndNumbers(t *testing.T) {
	var v struct {
		A Num `json:"a"`
		B Num `json:"b"`
		C Num `json:"c"`
	}
	if err := json.Unmarshal([]byte(`{"a":"12.5","b":3,"c":""}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.A != 12.5 || v.B != 3 || v.C != 0 {
		t.Fatalf("got %+v", v)
	}
}

func TestFirstJSONArrayFlattensNested(t *testing.T) {
	flat, err := FirstJSONArray(json.RawMessage(`[[{"x":1}],[{"x":2}]]`))
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]int
	if err := json.Unmarshal(flat, &rows); err != nil || len(rows) != 2 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	same, _ := FirstJSONArray(json.RawMessage(`[{"x":1}]`))
	if string(same) != `[{"x":1}]` {
		t.Fatalf("flat input changed: %s", same)
	}
}

func TestRankScore(t *testing.T) {
	if RankScore(0, 10) != 1 || RankScore(9, 10) < 0.19 || RankScore(9, 10) > 0.21 {
		t.Fatalf("unexpected curve: %v %v", RankScore(0, 10), RankScore(9, 10))
	}
}

func TestVersionHygiene(t *testing.T) {
	junk := [][2]string{
		{"Teardrop (Karaoke Version)", "Sing King"},
		{"Blinding Lights - Sped Up", "The Weeknd"},
		{"Creep", "Vitamin String Quartet"},
		{"Hello (In the Style of Adele)", "Ameritz"},
		{"Lullaby Renditions of Radiohead", "Rockabye Baby!"},
	}
	for _, j := range junk {
		if !IsJunkVersion(j[0], j[1]) {
			t.Errorf("should be junk: %v", j)
		}
	}
	if IsJunkVersion("Teardrop", "Massive Attack") || IsJunkVersion("Slowdive", "Siouxsie and the Banshees") {
		t.Error("real songs flagged as junk")
	}
	v := VariantTags("Lithium (Live at Reading) - 2011 Remaster")
	if !v["live"] || len(v) != 1 {
		t.Errorf("variants: %v", v)
	}
	if len(VariantTags("Get Lucky (Radio Edit)")) != 0 {
		t.Error("radio edit is the same recording")
	}
}
