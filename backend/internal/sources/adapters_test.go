package sources_test

// Contract tests for every adapter. Each fake server serves payloads in the
// shape the provider documents (see links in each package doc) and asserts
// the request we build matches the documented API.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ayanjhunjhunwala/auris/backend/internal/httpx"
	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
	"github.com/ayanjhunjhunwala/auris/backend/internal/sources"
	"github.com/ayanjhunjhunwala/auris/backend/internal/sources/deezer"
	"github.com/ayanjhunjhunwala/auris/backend/internal/sources/lastfm"
	"github.com/ayanjhunjhunwala/auris/backend/internal/sources/listenbrainz"
	"github.com/ayanjhunjhunwala/auris/backend/internal/sources/reccobeats"
)

func server(t *testing.T, routes map[string]func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for prefix, h := range routes {
			if strings.HasPrefix(r.URL.Path, prefix) {
				w.Header().Set("Content-Type", "application/json")
				h(w, r)
				return
			}
		}
		t.Errorf("unexpected request %s", r.URL)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func client() *httpx.Client { return httpx.New("auris-test/1.0", nil, nil) }

// --- Last.fm ----------------------------------------------------------------

func TestLastFMSimilar(t *testing.T) {
	srv := server(t, map[string]func(http.ResponseWriter, *http.Request){
		"/": func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("method") != "track.getSimilar" || q.Get("api_key") != "k" || q.Get("format") != "json" ||
				q.Get("artist") != "Disturbed" || q.Get("track") != "Stricken" {
				t.Errorf("bad query: %s", r.URL.RawQuery)
			}
			// Trimmed from the documented sample response.
			w.Write([]byte(`{"similartracks":{"track":[
				{"name":"Down With the Sickness","playcount":8066450,"mbid":"a8009036-b13e-4cb8-b2c7-2c3f9735b735","match":1,"url":"https://www.last.fm/music/Disturbed/_/Down+With+the+Sickness","duration":278,
				 "artist":{"name":"Disturbed","mbid":"dc75517c-d268-486d-bd5b-0eaff34eeef9","url":"https://www.last.fm/music/Disturbed"},
				 "image":[{"#text":"https://lastfm.freetls.fastly.net/i/u/300x300/2a96cbd8b46e442fc41c2b86b821562f.png","size":"extralarge"}]},
				{"name":"Before I Forget","playcount":"9225371","mbid":"","match":"0.602882","url":"u","duration":263,
				 "artist":{"name":"Slipknot","mbid":"ceb7b6be-1dbb-4d80-b157-5588bc884764","url":"u"},"image":[]}
			],"@attr":{"artist":"Disturbed"}}}`))
		},
	})
	c := lastfm.New(client(), "k")
	c.BaseURL = srv.URL + "/"
	got, err := c.Similar(context.Background(), model.Track{Artist: "Disturbed", Title: "Stricken"}, sources.Options{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d candidates", len(got))
	}
	if got[0].Track.ArtworkURL != "" {
		t.Errorf("placeholder art should be dropped, got %q", got[0].Track.ArtworkURL)
	}
	if got[1].Evidence[0].Score < 0.6 || got[1].Evidence[0].Score > 0.61 || got[1].Track.DurationMs != 263000 {
		t.Errorf("string-encoded numbers mis-parsed: %+v", got[1])
	}
}

func TestLastFMErrorInBody(t *testing.T) {
	srv := server(t, map[string]func(http.ResponseWriter, *http.Request){
		"/": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"error":10,"message":"Invalid API key - You must be granted a valid key by last.fm"}`))
		},
	})
	c := lastfm.New(client(), "bad")
	c.BaseURL = srv.URL + "/"
	if _, err := c.Similar(context.Background(), model.Track{Artist: "a", Title: "b"}, sources.Options{}); err == nil || !strings.Contains(err.Error(), "10") {
		t.Fatalf("expected lastfm error 10, got %v", err)
	}
}

func TestLastFMDisabledWithoutKey(t *testing.T) {
	if lastfm.New(client(), "").Enabled() {
		t.Fatal("should be disabled without key")
	}
}

// --- ReccoBeats -------------------------------------------------------------

func TestReccoBeatsFeaturesAndSteeredRecommendation(t *testing.T) {
	srv := server(t, map[string]func(http.ResponseWriter, *http.Request){
		"/v1/audio-features": func(w http.ResponseWriter, r *http.Request) {
			if ids := r.URL.Query().Get("ids"); ids != "USUM72104140,4uLU6hMCjMI75M1A2tKUQC" {
				t.Errorf("ids=%q", ids)
			}
			// Order deliberately differs from the request.
			w.Write([]byte(`{"content":[
				{"id":"rb-2","href":"https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC","isrc":"GBARL9300135","acousticness":0.1,"danceability":0.7,"energy":0.9,"instrumentalness":0,"key":5,"liveness":0.1,"loudness":-5.6,"mode":1,"speechiness":0.05,"tempo":118,"valence":0.8},
				{"id":"8212bab8-5911-48a0-b177-24923ef2329a","href":"https://open.spotify.com/track/00aqkszH1FdUiJJWvX6iEl","isrc":"USUM72104140","acousticness":0.2,"danceability":0.5,"energy":0.6,"instrumentalness":0.01,"key":1,"liveness":0.1,"loudness":-7,"mode":0,"speechiness":0.04,"tempo":114,"valence":0.3}
			]}`))
		},
		"/v1/track/recommendation": func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("seeds") != "8212bab8-5911-48a0-b177-24923ef2329a" || q.Get("energy") != "0.850" || q.Get("size") != "30" {
				t.Errorf("bad recommendation query: %s", r.URL.RawQuery)
			}
			w.Write([]byte(`{"content":[{"id":"878dadea-33c5-4c08-bdb9-e2b117475a99","trackTitle":"All Too Well","artists":[{"id":"c7b330b5","name":"Taylor Swift","href":"https://open.spotify.com/artist/06HL4z0CvFAxyc27GXpf02"}],"durationMs":329160,"isrc":"USCJY1231021","ean":null,"upc":null,"href":"https://open.spotify.com/track/00vJzaoxM3Eja1doBUhX0P","availableCountries":"US","popularity":34}]}`))
		},
	})
	c := reccobeats.New(client())
	c.BaseURL = srv.URL + "/v1"

	seed := &model.Track{IDs: model.IDs{ISRC: "USUM72104140"}}
	other := &model.Track{IDs: model.IDs{SpotifyID: "4uLU6hMCjMI75M1A2tKUQC"}}
	n, err := c.AttachFeatures(context.Background(), []*model.Track{seed, other})
	if err != nil || n != 2 {
		t.Fatalf("attached=%d err=%v", n, err)
	}
	if *seed.Features.Energy != 0.6 || seed.IDs.ReccoBeatsID != "8212bab8-5911-48a0-b177-24923ef2329a" || seed.IDs.SpotifyID != "00aqkszH1FdUiJJWvX6iEl" {
		t.Fatalf("seed not joined correctly: %+v", seed)
	}
	if *other.Features.Energy != 0.9 {
		t.Fatalf("other not joined: %+v", other.Features)
	}

	cands, err := c.Similar(context.Background(), *seed, sources.Options{Limit: 30, Target: &model.Features{Energy: model.Ptr(0.85)}})
	if err != nil || len(cands) != 1 {
		t.Fatalf("cands=%v err=%v", cands, err)
	}
	tr := cands[0].Track
	if tr.Artist != "Taylor Swift" || tr.IDs.SpotifyID != "00vJzaoxM3Eja1doBUhX0P" || *tr.Popularity != 0.34 {
		t.Fatalf("bad track mapping: %+v", tr)
	}
}

func TestReccoBeatsNeedsSeedID(t *testing.T) {
	c := reccobeats.New(client())
	if _, err := c.Similar(context.Background(), model.Track{Title: "x"}, sources.Options{}); err != sources.ErrMissingID {
		t.Fatalf("want ErrMissingID, got %v", err)
	}
}

// --- Deezer -----------------------------------------------------------------

const deezerTrack = `{"id":3135556,"title":"Harder, Better, Faster, Stronger","link":"https://www.deezer.com/track/3135556","duration":224,"rank":956167,"preview":"https://cdnt-preview.dzcdn.net/x.mp3","isrc":"GBDUW0000059","bpm":123.4,"gain":-12.4,
	"artist":{"id":27,"name":"Daft Punk"},"album":{"id":302127,"title":"Discovery","cover_medium":"https://e-cdns-images.dzcdn.net/m.jpg","cover_big":"https://e-cdns-images.dzcdn.net/b.jpg"}}`

func TestDeezerResolveSeedAndRelated(t *testing.T) {
	srv := server(t, map[string]func(http.ResponseWriter, *http.Request){
		"/search": func(w http.ResponseWriter, r *http.Request) {
			if q := r.URL.Query().Get("q"); q != `artist:"Daft Punk" track:"Harder Better Faster Stronger"` {
				t.Errorf("q=%q", q)
			}
			w.Write([]byte(`{"data":[` + strings.Replace(deezerTrack, `"isrc":"GBDUW0000059","bpm":123.4,`, "", 1) + `],"total":1}`))
		},
		"/track/3135556": func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(deezerTrack)) },
		"/artist/27/related": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"data":[{"id":1,"name":"Justice"},{"id":2,"name":"Air"}],"total":2}`))
		},
		"/artist/1/top": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"data":[{"id":10,"title":"D.A.N.C.E.","artist":{"id":1,"name":"Justice"},"album":{"title":"Cross"}}]}`))
		},
		"/artist/2/top": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"error":{"type":"DataException","message":"no data","code":800}}`))
		},
	})
	c := deezer.New(client())
	c.BaseURL = srv.URL

	seed, err := c.ResolveSeed(context.Background(), model.SeedQuery{Artist: "Daft Punk", Title: "Harder Better Faster Stronger"})
	if err != nil {
		t.Fatal(err)
	}
	if seed.IDs.ISRC != "GBDUW0000059" || seed.IDs.DeezerArtistID != 27 || *seed.Features.Tempo != 123.4 || seed.ArtworkURL == "" {
		t.Fatalf("seed incomplete: %+v", seed)
	}

	cands, err := c.Similar(context.Background(), seed, sources.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Track.Artist != "Justice" {
		t.Fatalf("related walk wrong (an erroring artist must be skipped, not fail all): %+v", cands)
	}
}

func TestDeezerErrorInBody(t *testing.T) {
	srv := server(t, map[string]func(http.ResponseWriter, *http.Request){
		"/search": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"error":{"type":"Exception","message":"Quota limit exceeded","code":4}}`))
		},
	})
	c := deezer.New(client())
	c.BaseURL = srv.URL
	if _, err := c.Search(context.Background(), "x", 5); err == nil || !strings.Contains(err.Error(), "Quota") {
		t.Fatalf("expected quota error, got %v", err)
	}
}

// --- ListenBrainz -----------------------------------------------------------

func TestListenBrainzResolveAndSimilar(t *testing.T) {
	srv := server(t, map[string]func(http.ResponseWriter, *http.Request){
		"/ws/2/isrc/USUM72104140": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("fmt") != "json" {
				t.Error("musicbrainz needs fmt=json")
			}
			w.Write([]byte(`{"isrc":"USUM72104140","recordings":[{"id":"seed-mbid","title":"Wicked Games","length":325000}]}`))
		},
		"/similar-recordings/json": func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("recording_mbids") != "seed-mbid" || q.Get("algorithm") != "algo" {
				t.Errorf("bad query %s", r.URL.RawQuery)
			}
			// Out of order, with a duplicate and the seed itself.
			w.Write([]byte(`[
				{"recording_mbid":"b","recording_name":"Often","artist_credit_name":"The Weeknd","release_name":"Beauty Behind the Madness","score":40},
				{"recording_mbid":"a","recording_name":"Love Me Harder","artist_credit_name":"Ariana Grande & The Weeknd","score":120,"caa_release_mbid":"rel","caa_id":42},
				{"recording_mbid":"b","recording_name":"Often","artist_credit_name":"The Weeknd","score":40},
				{"recording_mbid":"seed-mbid","recording_name":"Wicked Games","artist_credit_name":"The Weeknd","score":999}
			]`))
		},
	})
	c := listenbrainz.New(client(), "algo")
	c.LabsURL, c.MusicBrainzURL = srv.URL, srv.URL+"/ws/2"

	seed := model.Track{Artist: "The Weeknd", Title: "Wicked Games", IDs: model.IDs{ISRC: "USUM72104140"}}
	if err := c.ResolveIDs(context.Background(), &seed); err != nil || seed.IDs.MBID != "seed-mbid" {
		t.Fatalf("mbid=%q err=%v", seed.IDs.MBID, err)
	}
	cands, err := c.Similar(context.Background(), seed, sources.Options{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 || cands[0].Track.IDs.MBID != "a" || cands[1].Track.IDs.MBID != "b" {
		t.Fatalf("expected sorted, deduped, seed removed: %+v", cands)
	}
	if cands[0].Track.ArtworkURL != "https://coverartarchive.org/release/rel/42-250.jpg" {
		t.Errorf("artwork=%q", cands[0].Track.ArtworkURL)
	}
	if cands[0].Evidence[0].Score != 1 || cands[1].Evidence[0].Score >= cands[0].Evidence[0].Score {
		t.Errorf("scores not normalised: %v %v", cands[0].Evidence, cands[1].Evidence)
	}
}

func TestListenBrainzFallsBackToSearch(t *testing.T) {
	srv := server(t, map[string]func(http.ResponseWriter, *http.Request){
		"/recording-search/json": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("query") != "Portishead Strangers" {
				t.Errorf("query=%q", r.URL.Query().Get("query"))
			}
			w.Write([]byte(`[{"recording_mbid":"wrong","recording_name":"Strangers (live)","artist_credit_name":"Portishead"},
			                 {"recording_mbid":"right","recording_name":"Strangers","artist_credit_name":"Portishead"}]`))
		},
	})
	c := listenbrainz.New(client(), "algo")
	c.LabsURL = srv.URL
	seed := model.Track{Artist: "Portishead", Title: "Strangers"}
	if err := c.ResolveIDs(context.Background(), &seed); err != nil || seed.IDs.MBID != "right" {
		t.Fatalf("mbid=%q err=%v", seed.IDs.MBID, err)
	}
}

func TestReccoBeatsSendsExtraSeeds(t *testing.T) {
	srv := server(t, map[string]func(http.ResponseWriter, *http.Request){
		"/v1/track/recommendation": func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("seeds"); got != "seed-id,ref-spotify" {
				t.Errorf("seeds=%q", got)
			}
			w.Write([]byte(`{"content":[]}`))
		},
	})
	c := reccobeats.New(client())
	c.BaseURL = srv.URL + "/v1"
	_, err := c.Similar(context.Background(), model.Track{IDs: model.IDs{ReccoBeatsID: "seed-id"}},
		sources.Options{ExtraSeeds: []model.Track{{IDs: model.IDs{SpotifyID: "ref-spotify"}}, {}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDeezerEnrichPrefersISRC(t *testing.T) {
	srv := server(t, map[string]func(http.ResponseWriter, *http.Request){
		"/track/isrc:GBDUW0000059": func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(deezerTrack)) },
	})
	c := deezer.New(client())
	c.BaseURL = srv.URL
	tr := model.Track{Title: "whatever", Artist: "Daft Punk", IDs: model.IDs{ISRC: "GBDUW0000059"}}
	if err := c.Enrich(context.Background(), &tr); err != nil {
		t.Fatal(err)
	}
	if tr.ArtworkURL == "" || tr.PreviewURL == "" || tr.IDs.DeezerID != 3135556 {
		t.Fatalf("not enriched via ISRC: %+v", tr)
	}
}

func TestDeezerSeedSkipsKaraokeAndPrefersArtist(t *testing.T) {
	srv := server(t, map[string]func(http.ResponseWriter, *http.Request){
		"/search": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"data":[
				{"id":1,"title":"Teardrop (Karaoke Version)","artist":{"id":9,"name":"Sing King"},"album":{}},
				{"id":2,"title":"Teardrop","artist":{"id":8,"name":"Newton Faulkner"},"album":{}},
				{"id":3,"title":"Teardrop","artist":{"id":7,"name":"Massive Attack"},"album":{}}]}`))
		},
		"/track/3": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"id":3,"title":"Teardrop","artist":{"id":7,"name":"Massive Attack"},"album":{}}`))
		},
		"/track/2": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"id":2,"title":"Teardrop","artist":{"id":8,"name":"Newton Faulkner"},"album":{}}`))
		},
	})
	c := deezer.New(client())
	c.BaseURL = srv.URL
	seed, err := c.ResolveSeed(context.Background(), model.SeedQuery{Artist: "Massive Attack", Title: "Teardrop"})
	if err != nil || seed.IDs.DeezerID != 3 {
		t.Fatalf("want the Massive Attack original, got %+v %v", seed, err)
	}
	seed, err = c.ResolveSeed(context.Background(), model.SeedQuery{Query: "teardrop"})
	if err != nil || seed.IDs.DeezerID != 2 {
		t.Fatalf("free text: first non-karaoke hit, got %+v %v", seed, err)
	}
}

func TestLastFMSimilarArtists(t *testing.T) {
	srv := server(t, map[string]func(http.ResponseWriter, *http.Request){
		"/": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("method") != "artist.getSimilar" {
				t.Errorf("method=%s", r.URL.Query().Get("method"))
			}
			w.Write([]byte(`{"similarartists":{"artist":[{"name":"Portishead","match":"1"},{"name":"Tricky","match":0.62}]}}`))
		},
	})
	c := lastfm.New(client(), "k")
	c.BaseURL = srv.URL + "/"
	m, err := c.SimilarArtists(context.Background(), model.Track{Artist: "Massive Attack"})
	if err != nil || m["portishead"] != 1 || m["tricky"] != 0.62 {
		t.Fatalf("m=%v err=%v", m, err)
	}
}
