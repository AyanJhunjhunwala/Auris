// Package deezer adapts the public Deezer API (https://developers.deezer.com/api).
//
// Keyless, generous (50 requests / 5 s) and rich in presentation data, so it
// does three jobs:
//   - Search bar + seed resolution (/search, /track/{id} for ISRC and BPM)
//   - Candidate source via the artist graph (/artist/{id}/related -> /top)
//   - Enrichment: album art and 30-second previews for every result
package deezer

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/ayanjhunjhunwala/auris/backend/internal/httpx"
	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
	"github.com/ayanjhunjhunwala/auris/backend/internal/sources"
)

const DefaultBaseURL = "https://api.deezer.com"

type Client struct {
	http    *httpx.Client
	BaseURL string
	// RelatedArtists x TopPerArtist bounds the artist-graph fan-out.
	RelatedArtists int
	TopPerArtist   int
}

func New(h *httpx.Client) *Client {
	return &Client{http: h, BaseURL: DefaultBaseURL, RelatedArtists: 6, TopPerArtist: 3}
}

func (c *Client) Name() model.SourceName { return model.SourceDeezer }
func (c *Client) Enabled() bool          { return true }

// apiError: Deezer returns {"error":{...}} with HTTP 200.
type apiError struct {
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error"`
}

func (e apiError) err() error {
	if e.Error == nil {
		return nil
	}
	return fmt.Errorf("deezer %s (%d): %s", e.Error.Type, e.Error.Code, e.Error.Message)
}

type artist struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	PictureMedium string `json:"picture_medium"`
}

type album struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	CoverMedium string `json:"cover_medium"`
	CoverBig    string `json:"cover_big"`
	CoverXL     string `json:"cover_xl"`
}

type track struct {
	ID       int64       `json:"id"`
	Title    string      `json:"title"`
	Link     string      `json:"link"`
	Duration int         `json:"duration"` // seconds
	Rank     sources.Num `json:"rank"`
	Preview  string      `json:"preview"`
	ISRC     string      `json:"isrc"` // only on /track/{id}
	BPM      sources.Num `json:"bpm"`  // only on /track/{id}; 0 = unknown
	Gain     sources.Num `json:"gain"`
	Artist   artist      `json:"artist"`
	Album    album       `json:"album"`
}

type list[T any] struct {
	apiError
	Data []T `json:"data"`
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := c.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return c.http.GetJSON(ctx, string(c.Name()), u, "", out)
}

func (t track) toModel() model.Track {
	tr := model.Track{
		Key:        sources.TrackKey(t.Artist.Name, t.Title),
		Title:      t.Title,
		Artist:     t.Artist.Name,
		Album:      t.Album.Title,
		ArtworkURL: firstNonEmpty(t.Album.CoverBig, t.Album.CoverXL, t.Album.CoverMedium),
		PreviewURL: t.Preview,
		URL:        t.Link,
		DurationMs: t.Duration * 1000,
		IDs:        model.IDs{DeezerID: t.ID, DeezerArtistID: t.Artist.ID, ISRC: t.ISRC},
	}
	if t.Rank > 0 {
		// Deezer rank tops out around 1e6 for global hits.
		tr.Popularity = model.Ptr(sources.Clamp01(float64(t.Rank) / 1_000_000))
	}
	if t.BPM > 0 {
		tr.Features = &model.Features{Tempo: model.Ptr(float64(t.BPM))}
	}
	return tr
}

// Search powers the search bar.
func (c *Client) Search(ctx context.Context, q string, limit int) ([]model.Track, error) {
	var r list[track]
	if err := c.get(ctx, "/search", url.Values{"q": {q}, "limit": {strconv.Itoa(limit)}}, &r); err != nil {
		return nil, err
	}
	if err := r.err(); err != nil {
		return nil, err
	}
	out := make([]model.Track, 0, len(r.Data))
	for _, t := range r.Data {
		out = append(out, t.toModel())
	}
	return out, nil
}

// ResolveSeed turns a query into a full track (with ISRC and BPM, which
// only the /track/{id} endpoint returns).
func (c *Client) ResolveSeed(ctx context.Context, q model.SeedQuery) (model.Track, error) {
	id := q.DeezerID
	if id == 0 && q.ISRC != "" {
		var t struct {
			apiError
			track
		}
		if err := c.get(ctx, "/track/isrc:"+url.PathEscape(q.ISRC), nil, &t); err == nil && t.err() == nil && t.ID != 0 {
			return t.toModel(), nil
		}
	}
	if id == 0 {
		query := q.Query
		if q.Artist != "" && q.Title != "" {
			query = fmt.Sprintf(`artist:"%s" track:"%s"`, q.Artist, q.Title)
		}
		if strings.TrimSpace(query) == "" {
			return model.Track{}, fmt.Errorf("empty seed query")
		}
		hits, err := c.Search(ctx, query, 10)
		if err != nil {
			return model.Track{}, err
		}
		best := pickSeed(hits, q.Artist)
		if best == nil {
			return model.Track{}, sources.ErrNotFound
		}
		id = best.IDs.DeezerID
	}
	var t struct {
		apiError
		track
	}
	if err := c.get(ctx, "/track/"+strconv.FormatInt(id, 10), nil, &t); err != nil {
		return model.Track{}, err
	}
	if err := t.err(); err != nil {
		return model.Track{}, err
	}
	return t.toModel(), nil
}

// pickSeed chooses the search hit to treat as the seed. A wrong seed (a
// karaoke cut or a cover) poisons every downstream source, so skip junk
// versions, prefer the requested artist, and otherwise trust Deezer's
// relevance order.
func pickSeed(hits []model.Track, artist string) *model.Track {
	want := sources.PrimaryArtist(artist)
	var first *model.Track
	for i := range hits {
		h := &hits[i]
		if sources.IsJunkVersion(h.Title, h.Artist) {
			continue
		}
		if want == "" || sources.PrimaryArtist(h.Artist) == want {
			return h
		}
		if first == nil {
			first = h
		}
	}
	return first
}

// Similar walks the artist graph: related artists' top tracks. It is the
// weakest signal (artist-level, not track-level) but always available and it
// brings in artists the track-level sources have never seen together.
func (c *Client) Similar(ctx context.Context, seed model.Track, opts sources.Options) ([]model.Candidate, error) {
	if seed.IDs.DeezerArtistID == 0 {
		return nil, sources.ErrMissingID
	}
	var rel list[artist]
	path := fmt.Sprintf("/artist/%d/related", seed.IDs.DeezerArtistID)
	if err := c.get(ctx, path, url.Values{"limit": {strconv.Itoa(c.RelatedArtists)}}, &rel); err != nil {
		return nil, err
	}
	if err := rel.err(); err != nil {
		return nil, err
	}
	artists := rel.Data[:min(len(rel.Data), c.RelatedArtists)]

	type result struct {
		idx    int
		tracks []track
	}
	results := make(chan result, len(artists))
	for i, a := range artists {
		go func() {
			var top list[track]
			p := fmt.Sprintf("/artist/%d/top", a.ID)
			if err := c.get(ctx, p, url.Values{"limit": {strconv.Itoa(c.TopPerArtist)}}, &top); err != nil || top.err() != nil {
				results <- result{idx: i}
				return
			}
			results <- result{idx: i, tracks: top.Data}
		}()
	}
	byArtist := make([][]track, len(artists))
	for range artists {
		r := <-results
		byArtist[r.idx] = r.tracks
	}

	var out []model.Candidate
	for ai, tracks := range byArtist {
		artistScore := sources.RankScore(ai, len(artists))
		for ti, t := range tracks[:min(len(tracks), c.TopPerArtist)] {
			out = append(out, model.Candidate{Track: t.toModel(), Evidence: []model.Evidence{{
				Source: model.SourceDeezer, Rank: len(out),
				Score:  artistScore * (1 - 0.15*float64(ti)),
				Reason: fmt.Sprintf("Top track of %s, a Deezer related artist", artists[ai].Name),
			}}})
		}
	}
	return out, nil
}

// SimilarArtists maps Deezer's related artists to rank-based similarity.
func (c *Client) SimilarArtists(ctx context.Context, seed model.Track) (map[string]float64, error) {
	if seed.IDs.DeezerArtistID == 0 {
		return nil, sources.ErrMissingID
	}
	var rel list[artist]
	path := fmt.Sprintf("/artist/%d/related", seed.IDs.DeezerArtistID)
	if err := c.get(ctx, path, url.Values{"limit": {"25"}}, &rel); err != nil {
		return nil, err
	}
	if err := rel.err(); err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(rel.Data))
	for i, a := range rel.Data {
		out[sources.PrimaryArtist(a.Name)] = sources.RankScore(i, len(rel.Data))
	}
	return out, nil
}

// Enrich fills artwork, preview and Deezer IDs for a track found elsewhere.
// With an ISRC it is one exact lookup; otherwise a search constrained to
// the same lead artist.
func (c *Client) Enrich(ctx context.Context, t *model.Track) error {
	if t.ArtworkURL != "" && t.PreviewURL != "" && t.IDs.DeezerID != 0 {
		return nil
	}
	if t.IDs.ISRC != "" {
		var hit struct {
			apiError
			track
		}
		if err := c.get(ctx, "/track/isrc:"+url.PathEscape(t.IDs.ISRC), nil, &hit); err == nil && hit.err() == nil && hit.ID != 0 {
			applyHit(t, hit.toModel())
			return nil
		}
	}
	hits, err := c.Search(ctx, fmt.Sprintf(`artist:"%s" track:"%s"`, t.Artist, sources.NormTitle(t.Title)), 3)
	if err != nil {
		return err
	}
	want := sources.PrimaryArtist(t.Artist)
	for _, h := range hits {
		if sources.PrimaryArtist(h.Artist) == want {
			applyHit(t, h)
			return nil
		}
	}
	return sources.ErrNotFound
}

func applyHit(t *model.Track, h model.Track) {
	t.ArtworkURL = firstNonEmpty(t.ArtworkURL, h.ArtworkURL)
	t.PreviewURL = firstNonEmpty(t.PreviewURL, h.PreviewURL)
	t.Album = firstNonEmpty(t.Album, h.Album)
	t.URL = firstNonEmpty(t.URL, h.URL)
	if t.IDs.DeezerID == 0 {
		t.IDs.DeezerID, t.IDs.DeezerArtistID = h.IDs.DeezerID, h.IDs.DeezerArtistID
	}
	if t.Popularity == nil {
		t.Popularity = h.Popularity
	}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
