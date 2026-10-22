// Package listenbrainz adapts the MetaBrainz stack:
//   - labs.api.listenbrainz.org/similar-recordings/json - recordings that
//     co-occur in real ListenBrainz listening sessions (keyless)
//   - labs.api.listenbrainz.org/recording-search/json - fuzzy name -> MBID
//   - musicbrainz.org/ws/2/isrc/{isrc} - exact ISRC -> MBID
//
// MetaBrainz asks for a descriptive User-Agent and at most ~1 req/s, which
// the shared httpx client enforces via per-source limiters.
package listenbrainz

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"

	"github.com/ayanjhunjhunwala/auris/backend/internal/httpx"
	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
	"github.com/ayanjhunjhunwala/auris/backend/internal/sources"
)

const (
	DefaultLabsURL        = "https://labs.api.listenbrainz.org"
	DefaultMusicBrainzURL = "https://musicbrainz.org/ws/2"
	// MusicBrainzSource is the telemetry/limiter key for musicbrainz.org.
	MusicBrainzSource = "musicbrainz"
)

type Client struct {
	http           *httpx.Client
	LabsURL        string
	MusicBrainzURL string
	Algorithm      string
}

func New(h *httpx.Client, algorithm string) *Client {
	return &Client{http: h, LabsURL: DefaultLabsURL, MusicBrainzURL: DefaultMusicBrainzURL, Algorithm: algorithm}
}

func (c *Client) Name() model.SourceName { return model.SourceListenBrainz }
func (c *Client) Enabled() bool          { return true }

type isrcResponse struct {
	Recordings []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"recordings"`
}

type searchRow struct {
	RecordingMBID    string `json:"recording_mbid"`
	RecordingName    string `json:"recording_name"`
	ArtistCreditName string `json:"artist_credit_name"`
	ReleaseName      string `json:"release_name"`
	ReleaseMBID      string `json:"release_mbid"`
}

// ResolveIDs finds the seed's MusicBrainz recording MBID: exact via ISRC
// when we have one, otherwise a fuzzy labs search on "artist title".
func (c *Client) ResolveIDs(ctx context.Context, seed *model.Track) error {
	if seed.IDs.MBID != "" {
		return nil
	}
	if seed.IDs.ISRC != "" {
		var r isrcResponse
		u := fmt.Sprintf("%s/isrc/%s?fmt=json", c.MusicBrainzURL, url.PathEscape(seed.IDs.ISRC))
		if err := c.http.GetJSON(ctx, MusicBrainzSource, u, "", &r); err == nil && len(r.Recordings) > 0 {
			seed.IDs.MBID = r.Recordings[0].ID
			return nil
		}
	}
	var raw json.RawMessage
	q := url.Values{"query": {seed.Artist + " " + seed.Title}}
	if err := c.http.GetJSON(ctx, string(c.Name()), c.LabsURL+"/recording-search/json?"+q.Encode(), "", &raw); err != nil {
		return err
	}
	rows, err := decodeRows[searchRow](raw)
	if err != nil {
		return err
	}
	want := sources.TrackKey(seed.Artist, seed.Title)
	for _, r := range rows {
		if sources.TrackKey(r.ArtistCreditName, r.RecordingName) == want {
			seed.IDs.MBID = r.RecordingMBID
			return nil
		}
	}
	// No exact key match: accept the top fuzzy hit only if the artist agrees.
	if len(rows) > 0 && sources.PrimaryArtist(rows[0].ArtistCreditName) == sources.PrimaryArtist(seed.Artist) {
		seed.IDs.MBID = rows[0].RecordingMBID
		return nil
	}
	return sources.ErrNotFound
}

type similarRow struct {
	RecordingMBID    string      `json:"recording_mbid"`
	RecordingName    string      `json:"recording_name"`
	ArtistCreditName string      `json:"artist_credit_name"`
	ReleaseName      string      `json:"release_name"`
	ReleaseMBID      string      `json:"release_mbid"`
	CAAReleaseMBID   string      `json:"caa_release_mbid"`
	CAAID            sources.Num `json:"caa_id"`
	Score            sources.Num `json:"score"`
}

// Similar returns recordings that co-occur with the seed in listening
// sessions. Scores are raw co-occurrence counts, normalised here by the
// list maximum.
func (c *Client) Similar(ctx context.Context, seed model.Track, opts sources.Options) ([]model.Candidate, error) {
	if seed.IDs.MBID == "" {
		return nil, sources.ErrMissingID
	}
	q := url.Values{"recording_mbids": {seed.IDs.MBID}, "algorithm": {c.Algorithm}}
	var raw json.RawMessage
	if err := c.http.GetJSON(ctx, string(c.Name()), c.LabsURL+"/similar-recordings/json?"+q.Encode(), "", &raw); err != nil {
		return nil, err
	}
	rows, err := decodeRows[similarRow](raw)
	if err != nil {
		return nil, err
	}
	// The labs API may return rows out of order and with duplicates.
	slices.SortFunc(rows, func(a, b similarRow) int {
		return cmp.Or(cmp.Compare(b.Score, a.Score), cmp.Compare(a.RecordingMBID, b.RecordingMBID))
	})
	rows = slices.CompactFunc(rows, func(a, b similarRow) bool { return a.RecordingMBID == b.RecordingMBID })
	rows = slices.DeleteFunc(rows, func(r similarRow) bool { return r.RecordingMBID == seed.IDs.MBID || r.RecordingName == "" })
	if opts.Limit > 0 && len(rows) > opts.Limit {
		rows = rows[:opts.Limit]
	}
	if len(rows) == 0 {
		return nil, nil
	}
	top := float64(rows[0].Score)
	out := make([]model.Candidate, 0, len(rows))
	for i, r := range rows {
		score := sources.RankScore(i, len(rows))
		if top > 0 {
			// Blend count-based and rank-based so one dominant pair
			// doesn't flatten everything below it to ~0.
			score = 0.5*score + 0.5*float64(r.Score)/top
		}
		tr := model.Track{
			Key:    sources.TrackKey(r.ArtistCreditName, r.RecordingName),
			Title:  r.RecordingName,
			Artist: r.ArtistCreditName,
			Album:  r.ReleaseName,
			URL:    "https://musicbrainz.org/recording/" + r.RecordingMBID,
			IDs:    model.IDs{MBID: r.RecordingMBID, ReleaseMBID: r.ReleaseMBID},
		}
		if r.CAAReleaseMBID != "" && r.CAAID > 0 {
			tr.ArtworkURL = fmt.Sprintf("https://coverartarchive.org/release/%s/%d-250.jpg", r.CAAReleaseMBID, int64(r.CAAID))
		}
		out = append(out, model.Candidate{Track: tr, Evidence: []model.Evidence{{
			Source: model.SourceListenBrainz, Rank: i, Score: sources.Clamp01(score),
			Reason: "Played in the same ListenBrainz listening sessions",
		}}})
	}
	return out, nil
}

func decodeRows[T any](raw json.RawMessage) ([]T, error) {
	flat, err := sources.FirstJSONArray(raw)
	if err != nil {
		return nil, err
	}
	var rows []T
	if err := json.Unmarshal(flat, &rows); err != nil {
		return nil, fmt.Errorf("listenbrainz: unexpected payload: %w", err)
	}
	return rows, nil
}
