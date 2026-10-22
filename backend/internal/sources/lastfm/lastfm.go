// Package lastfm adapts the Last.fm 2.0 API (https://www.last.fm/api).
//
// Used for: track.getSimilar (collaborative-filtering neighbours from
// scrobble data - the strongest "people who play X also play Y" signal) and
// track.search as a fallback for the search bar. Needs LASTFM_API_KEY.
package lastfm

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

const DefaultBaseURL = "https://ws.audioscrobbler.com/2.0/"

// placeholderImage is the grey star Last.fm returns for every track since it
// stopped serving real track art; treat it as "no artwork".
const placeholderImage = "2a96cbd8b46e442fc41c2b86b821562f"

type Client struct {
	http    *httpx.Client
	apiKey  string
	BaseURL string
}

func New(h *httpx.Client, apiKey string) *Client {
	return &Client{http: h, apiKey: apiKey, BaseURL: DefaultBaseURL}
}

func (c *Client) Name() model.SourceName { return model.SourceLastFM }
func (c *Client) Enabled() bool          { return c.apiKey != "" }

type image struct {
	URL  string `json:"#text"`
	Size string `json:"size"`
}

// apiError: Last.fm reports errors in the body, often with HTTP 200.
type apiError struct {
	Code    int    `json:"error"`
	Message string `json:"message"`
}

func (e apiError) err() error {
	if e.Code == 0 {
		return nil
	}
	return fmt.Errorf("lastfm error %d: %s", e.Code, e.Message)
}

func (c *Client) call(ctx context.Context, method string, params url.Values, out any) error {
	if !c.Enabled() {
		return fmt.Errorf("lastfm: LASTFM_API_KEY not set")
	}
	params.Set("method", method)
	params.Set("format", "json")
	cacheKey := "lastfm:" + params.Encode() // before the key is added
	params.Set("api_key", c.apiKey)
	return c.http.GetJSON(ctx, string(c.Name()), c.BaseURL+"?"+params.Encode(), cacheKey, out)
}

type similarResponse struct {
	apiError
	SimilarTracks struct {
		Track []struct {
			Name      string      `json:"name"`
			MBID      string      `json:"mbid"`
			Match     sources.Num `json:"match"`
			Playcount sources.Num `json:"playcount"`
			URL       string      `json:"url"`
			Duration  sources.Num `json:"duration"` // seconds
			Artist    struct {
				Name string `json:"name"`
				MBID string `json:"mbid"`
			} `json:"artist"`
			Image []image `json:"image"`
		} `json:"track"`
	} `json:"similartracks"`
}

// Similar calls track.getSimilar. Last.fm's "match" is already a 0..1
// similarity derived from co-listening.
func (c *Client) Similar(ctx context.Context, seed model.Track, opts sources.Options) ([]model.Candidate, error) {
	p := url.Values{}
	if seed.IDs.MBID != "" {
		p.Set("mbid", seed.IDs.MBID)
	}
	p.Set("artist", seed.Artist)
	p.Set("track", seed.Title)
	p.Set("autocorrect", "1")
	p.Set("limit", strconv.Itoa(max(opts.Limit, 10)))

	var r similarResponse
	if err := c.call(ctx, "track.getSimilar", p, &r); err != nil {
		return nil, err
	}
	if err := r.err(); err != nil {
		// An MBID Last.fm doesn't know yields error 6; retry by name only.
		if seed.IDs.MBID != "" {
			seed.IDs.MBID = ""
			return c.Similar(ctx, seed, opts)
		}
		return nil, err
	}

	out := make([]model.Candidate, 0, len(r.SimilarTracks.Track))
	for i, t := range r.SimilarTracks.Track {
		tr := model.Track{
			Key:        sources.TrackKey(t.Artist.Name, t.Name),
			Title:      t.Name,
			Artist:     t.Artist.Name,
			ArtistID:   t.Artist.MBID,
			URL:        t.URL,
			DurationMs: int(t.Duration) * 1000,
			ArtworkURL: bestImage(t.Image),
			IDs:        model.IDs{MBID: t.MBID},
		}
		out = append(out, model.Candidate{Track: tr, Evidence: []model.Evidence{{
			Source: model.SourceLastFM, Rank: i, Score: sources.Clamp01(float64(t.Match)),
			Reason: "Last.fm listeners who play the seed also play this",
		}}})
	}
	return out, nil
}

type similarArtistsResponse struct {
	apiError
	SimilarArtists struct {
		Artist []struct {
			Name  string      `json:"name"`
			Match sources.Num `json:"match"`
		} `json:"artist"`
	} `json:"similarartists"`
}

// SimilarArtists calls artist.getSimilar (match is already 0..1).
func (c *Client) SimilarArtists(ctx context.Context, seed model.Track) (map[string]float64, error) {
	p := url.Values{}
	p.Set("artist", seed.Artist)
	p.Set("autocorrect", "1")
	p.Set("limit", "100")
	var r similarArtistsResponse
	if err := c.call(ctx, "artist.getSimilar", p, &r); err != nil {
		return nil, err
	}
	if err := r.err(); err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(r.SimilarArtists.Artist))
	for _, a := range r.SimilarArtists.Artist {
		out[sources.PrimaryArtist(a.Name)] = sources.Clamp01(float64(a.Match))
	}
	return out, nil
}

type searchResponse struct {
	apiError
	Results struct {
		TrackMatches struct {
			Track []struct {
				Name      string      `json:"name"`
				Artist    string      `json:"artist"`
				URL       string      `json:"url"`
				Listeners sources.Num `json:"listeners"`
				MBID      string      `json:"mbid"`
				Image     []image     `json:"image"`
			} `json:"track"`
		} `json:"trackmatches"`
	} `json:"results"`
}

// Search calls track.search.
func (c *Client) Search(ctx context.Context, q string, limit int) ([]model.Track, error) {
	p := url.Values{}
	p.Set("track", q)
	p.Set("limit", strconv.Itoa(limit))
	var r searchResponse
	if err := c.call(ctx, "track.search", p, &r); err != nil {
		return nil, err
	}
	if err := r.err(); err != nil {
		return nil, err
	}
	out := make([]model.Track, 0, len(r.Results.TrackMatches.Track))
	for _, t := range r.Results.TrackMatches.Track {
		out = append(out, model.Track{
			Key: sources.TrackKey(t.Artist, t.Name), Title: t.Name, Artist: t.Artist,
			URL: t.URL, ArtworkURL: bestImage(t.Image), IDs: model.IDs{MBID: t.MBID},
		})
	}
	return out, nil
}

func bestImage(imgs []image) string {
	best := ""
	for _, im := range imgs { // ordered small -> mega
		if im.URL != "" && !strings.Contains(im.URL, placeholderImage) {
			best = im.URL
		}
	}
	return best
}
