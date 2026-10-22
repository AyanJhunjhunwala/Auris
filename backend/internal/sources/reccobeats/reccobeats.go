// Package reccobeats adapts the ReccoBeats API (https://reccobeats.com/docs).
//
// ReccoBeats is a free, keyless stand-in for Spotify's deprecated
// recommendations and audio-features endpoints:
//   - GET /v1/audio-features?ids=  (ISRC, Spotify or ReccoBeats IDs; max 40)
//   - GET /v1/track/recommendation?seeds=&size=&energy=&valence=...
//
// The recommendation endpoint accepts target audio features, which is what
// makes "make it more upbeat" change the candidate pool, not just the order.
package reccobeats

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

const (
	DefaultBaseURL = "https://api.reccobeats.com/v1"
	maxIDsPerCall  = 40  // documented limit for /audio-features
	maxRecSize     = 100 // documented validation limit for size
	maxSeeds       = 5   // Spotify-style seed cap; ReccoBeats doesn't document one
)

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

type Client struct {
	http    *httpx.Client
	BaseURL string
}

func New(h *httpx.Client) *Client { return &Client{http: h, BaseURL: DefaultBaseURL} }

func (c *Client) Name() model.SourceName { return model.SourceReccoBeats }
func (c *Client) Enabled() bool          { return true }

type artist struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Href string `json:"href"`
}

type track struct {
	ID         string   `json:"id"`
	TrackTitle string   `json:"trackTitle"`
	Artists    []artist `json:"artists"`
	DurationMs int      `json:"durationMs"`
	ISRC       string   `json:"isrc"`
	Href       string   `json:"href"`
	Popularity *int     `json:"popularity"`
}

type audioFeatures struct {
	ID               string   `json:"id"`
	Href             string   `json:"href"`
	ISRC             string   `json:"isrc"`
	Acousticness     *float64 `json:"acousticness"`
	Danceability     *float64 `json:"danceability"`
	Energy           *float64 `json:"energy"`
	Instrumentalness *float64 `json:"instrumentalness"`
	Key              *int     `json:"key"`
	Liveness         *float64 `json:"liveness"`
	Loudness         *float64 `json:"loudness"`
	Mode             *int     `json:"mode"`
	Speechiness      *float64 `json:"speechiness"`
	Tempo            *float64 `json:"tempo"`
	Valence          *float64 `json:"valence"`
}

func (a audioFeatures) toModel() *model.Features {
	return &model.Features{
		Acousticness: a.Acousticness, Danceability: a.Danceability, Energy: a.Energy,
		Instrumentalness: a.Instrumentalness, Liveness: a.Liveness, Loudness: a.Loudness,
		Speechiness: a.Speechiness, Tempo: a.Tempo, Valence: a.Valence, Key: a.Key, Mode: a.Mode,
	}
}

type listResponse[T any] struct {
	Content []T `json:"content"`
}

// lookupID picks the identifier ReccoBeats can resolve, best first.
func lookupID(t *model.Track) string {
	switch {
	case t.IDs.ReccoBeatsID != "":
		return t.IDs.ReccoBeatsID
	case t.IDs.SpotifyID != "":
		return t.IDs.SpotifyID
	case t.IDs.ISRC != "":
		return t.IDs.ISRC
	}
	return ""
}

// AttachFeatures batches tracks into /audio-features calls and writes the
// features (plus the ReccoBeats/Spotify IDs it learns) back onto each track.
func (c *Client) AttachFeatures(ctx context.Context, tracks []*model.Track) (int, error) {
	var pending []*model.Track
	for _, t := range tracks {
		if t.Features.IsEmpty() && lookupID(t) != "" {
			pending = append(pending, t)
		}
	}
	if len(pending) == 0 {
		return 0, sources.ErrMissingID
	}

	attached := 0
	var firstErr error
	for start := 0; start < len(pending); start += maxIDsPerCall {
		batch := pending[start:min(start+maxIDsPerCall, len(pending))]
		ids := make([]string, len(batch))
		for i, t := range batch {
			ids[i] = lookupID(t)
		}
		var r listResponse[audioFeatures]
		// IDs are alphanumeric/UUIDs, so commas can stay literal as in the docs.
		u := c.BaseURL + "/audio-features?ids=" + strings.Join(ids, ",")
		if err := c.http.GetJSON(ctx, string(c.Name()), u, "", &r); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		attached += match(batch, r.Content)
	}
	if attached == 0 && firstErr != nil {
		return 0, firstErr
	}
	return attached, nil
}

// match pairs returned feature rows with the tracks that requested them. The
// response is not guaranteed to preserve order or include every ID, so we
// join on whichever identifier lines up.
func match(batch []*model.Track, rows []audioFeatures) int {
	n := 0
	for _, row := range rows {
		spotify := sources.SpotifyIDFromURL(row.Href)
		for _, t := range batch {
			if !t.Features.IsEmpty() {
				continue
			}
			hit := (row.ID != "" && row.ID == t.IDs.ReccoBeatsID) ||
				(spotify != "" && spotify == t.IDs.SpotifyID) ||
				(row.ISRC != "" && strings.EqualFold(row.ISRC, t.IDs.ISRC))
			if !hit {
				continue
			}
			t.Features = row.toModel()
			if t.IDs.ReccoBeatsID == "" {
				t.IDs.ReccoBeatsID = row.ID
			}
			if t.IDs.SpotifyID == "" {
				t.IDs.SpotifyID = spotify
			}
			n++
			break
		}
	}
	return n
}

// Similar asks the recommendation engine for neighbours of the seed,
// optionally conditioned on a steered target profile.
func (c *Client) Similar(ctx context.Context, seed model.Track, opts sources.Options) ([]model.Candidate, error) {
	seedID := seed.IDs.ReccoBeatsID
	if seedID == "" {
		seedID = seed.IDs.SpotifyID
	}
	if seedID == "" {
		return nil, sources.ErrMissingID
	}
	size := min(max(opts.Limit, 10), maxRecSize)
	seeds := []string{seedID}
	for _, t := range opts.ExtraSeeds {
		id := firstNonEmpty(t.IDs.ReccoBeatsID, t.IDs.SpotifyID)
		if id != "" && len(seeds) < maxSeeds {
			seeds = append(seeds, id)
		}
	}
	q := url.Values{}
	q.Set("seeds", strings.Join(seeds, ","))
	q.Set("size", strconv.Itoa(size))
	if t := opts.Target; t != nil {
		set := func(k string, v *float64, lo, hi float64) {
			if v != nil {
				q.Set(k, strconv.FormatFloat(max(lo, min(hi, *v)), 'f', 3, 64))
			}
		}
		set("acousticness", t.Acousticness, 0, 1)
		set("danceability", t.Danceability, 0, 1)
		set("energy", t.Energy, 0, 1)
		set("instrumentalness", t.Instrumentalness, 0, 1)
		set("speechiness", t.Speechiness, 0, 1)
		set("valence", t.Valence, 0, 1)
		set("tempo", t.Tempo, 0, 250)
	}

	var r listResponse[track]
	if err := c.http.GetJSON(ctx, string(c.Name()), c.BaseURL+"/track/recommendation?"+q.Encode(), "", &r); err != nil {
		return nil, fmt.Errorf("reccobeats recommendation: %w", err)
	}
	out := make([]model.Candidate, 0, len(r.Content))
	for i, t := range r.Content {
		tr := toTrack(t)
		out = append(out, model.Candidate{Track: tr, Evidence: []model.Evidence{{
			Source: model.SourceReccoBeats, Rank: i, Score: sources.RankScore(i, len(r.Content)),
			Reason: "Close in ReccoBeats' audio-feature space",
		}}})
	}
	return out, nil
}

func toTrack(t track) model.Track {
	artistName := ""
	names := make([]string, 0, len(t.Artists))
	for _, a := range t.Artists {
		names = append(names, a.Name)
	}
	if len(names) > 0 {
		artistName = strings.Join(names, ", ")
	}
	tr := model.Track{
		Key:        sources.TrackKey(artistName, t.TrackTitle),
		Title:      t.TrackTitle,
		Artist:     artistName,
		DurationMs: t.DurationMs,
		URL:        t.Href,
		IDs: model.IDs{
			ISRC: t.ISRC, ReccoBeatsID: t.ID, SpotifyID: sources.SpotifyIDFromURL(t.Href),
		},
	}
	if t.Popularity != nil {
		tr.Popularity = model.Ptr(float64(*t.Popularity) / 100)
	}
	return tr
}
