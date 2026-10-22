// Package sources defines the contracts every upstream adapter implements,
// plus helpers shared by the adapters. The engine depends only on these
// interfaces; cmd/auris wires the concrete adapters in.
package sources

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
)

// ErrMissingID means a source cannot run because the seed lacks an
// identifier it needs (e.g. ListenBrainz without a recording MBID). The
// engine reports it as "skipped" rather than "error".
var ErrMissingID = errors.New("seed is missing an identifier this source needs")

// ErrNotFound means the upstream answered but had no match.
var ErrNotFound = errors.New("not found")

// Named is implemented by every adapter.
type Named interface {
	Name() model.SourceName
	Enabled() bool
}

// Searcher powers the search bar's autocomplete.
type Searcher interface {
	Named
	Search(ctx context.Context, q string, limit int) ([]model.Track, error)
}

// SeedResolver turns a user query into a concrete track with IDs.
type SeedResolver interface {
	Named
	ResolveSeed(ctx context.Context, q model.SeedQuery) (model.Track, error)
}

// IDResolver fills in identifiers this source needs on an existing seed.
type IDResolver interface {
	Named
	ResolveIDs(ctx context.Context, seed *model.Track) error
}

// Options tune candidate generation.
type Options struct {
	Limit int
	// Target is the steered audio profile. Sources that can condition on
	// audio features (ReccoBeats) use it to shift their candidate pool.
	Target *model.Features
	// ExtraSeeds (e.g. a "sound like" reference) are added to the seed list
	// by sources that accept several seeds.
	ExtraSeeds []model.Track
}

// CandidateSource proposes tracks similar to a seed.
type CandidateSource interface {
	Named
	Similar(ctx context.Context, seed model.Track, opts Options) ([]model.Candidate, error)
}

// ArtistNeighbors returns artists similar to the seed's artist, keyed by
// PrimaryArtist(name), with a 0..1 similarity. Artist-level similarity is
// coarse but very reliable, so the ranker uses it to vet track candidates.
type ArtistNeighbors interface {
	Named
	SimilarArtists(ctx context.Context, seed model.Track) (map[string]float64, error)
}

// FeatureSource attaches audio features to tracks in place, returning how
// many tracks gained features.
type FeatureSource interface {
	Named
	AttachFeatures(ctx context.Context, tracks []*model.Track) (int, error)
}

// Enricher fills presentation metadata (artwork, preview, links) in place.
type Enricher interface {
	Named
	Enrich(ctx context.Context, t *model.Track) error
}

// ---------------------------------------------------------------------------
// Normalisation: the merge key decides whether two sources are talking about
// the same song, so it has to survive "feat." credits, remaster suffixes and
// punctuation differences between providers.

var (
	bracketed    = regexp.MustCompile(`\s*[\(\[][^\)\]]*[\)\]]`)
	noiseInParen = regexp.MustCompile(`(?i)\b(feat|ft|featuring|with|remaster(ed)?|version|edit|mono|stereo|deluxe|bonus|explicit|clean)\b`)
	dashSuffix   = regexp.MustCompile(`(?i)\s+-\s+.*\b(remaster(ed)?|version|edit|mono|stereo|single|radio|bonus)\b.*$`)
	artistSplit  = regexp.MustCompile(`(?i)\s*(,|&|\bfeat\.?|\bft\.?|\bfeaturing\b|\bwith\b|\bx\b|\band\b|\+)\s*`)
	spaces       = regexp.MustCompile(`\s+`)
)

// NormTitle strips decorations that differ between providers but keeps
// meaningful qualifiers like "Remix" or "Live" (those are different songs).
func NormTitle(s string) string {
	s = bracketed.ReplaceAllStringFunc(s, func(m string) string {
		if noiseInParen.MatchString(m) {
			return ""
		}
		return m
	})
	s = dashSuffix.ReplaceAllString(s, "")
	return squash(s)
}

// PrimaryArtist returns the normalised lead artist of a credit.
func PrimaryArtist(s string) string {
	parts := artistSplit.Split(strings.TrimSpace(s), 2)
	return squash(parts[0])
}

// TrackKey is the merge key for a song.
func TrackKey(artist, title string) string {
	return PrimaryArtist(artist) + "|" + NormTitle(title)
}

func squash(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	return strings.TrimSpace(spaces.ReplaceAllString(b.String(), " "))
}

// SpotifyIDFromURL extracts the base-62 ID from an open.spotify.com URL.
func SpotifyIDFromURL(u string) string {
	const marker = "open.spotify.com/track/"
	i := strings.Index(u, marker)
	if i < 0 {
		return ""
	}
	id := u[i+len(marker):]
	if j := strings.IndexAny(id, "?/#"); j >= 0 {
		id = id[:j]
	}
	return id
}

// RankScore converts a list position into a 0..1 similarity for sources
// that return an ordered list without scores. The curve is gentle at the top
// so the first few items are nearly equal.
func RankScore(rank, n int) float64 {
	if n <= 1 {
		return 1
	}
	return 1 - 0.8*float64(rank)/float64(n-1)
}

// Num decodes JSON numbers that some providers (Last.fm) emit as strings.
type Num float64

func (n *Num) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil // tolerate garbage rather than fail the whole payload
	}
	*n = Num(f)
	return nil
}

// Clamp01 limits v to [0,1].
func Clamp01(v float64) float64 { return max(0, min(1, v)) }

// FirstJSONArray handles endpoints that sometimes return [[...]] and
// sometimes [...]: it returns the inner rows either way.
func FirstJSONArray(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := strings.TrimLeft(string(raw), " \t\r\n")
	if strings.HasPrefix(trimmed, "[[") || strings.HasPrefix(trimmed, "[ [") {
		var outer []json.RawMessage
		if err := json.Unmarshal(raw, &outer); err != nil {
			return nil, err
		}
		// Flatten all inner arrays.
		var all []json.RawMessage
		for _, inner := range outer {
			var rows []json.RawMessage
			if err := json.Unmarshal(inner, &rows); err == nil {
				all = append(all, rows...)
			}
		}
		return json.Marshal(all)
	}
	return raw, nil
}

// ---------------------------------------------------------------------------
// Version hygiene: catalogues are full of karaoke tracks, tributes, sped-up
// edits and live cuts that match a song by name but not by sound.

var (
	junkTitle  = regexp.MustCompile(`(?i)\b(karaoke|instrumental version|backing track|in the style of|originally performed|made famous|sped ?up|slowed|reverb|8d audio|nightcore|lullaby|music box|tribute|cover version|piano version|string quartet)\b`)
	junkArtist = regexp.MustCompile(`(?i)\b(karaoke|tribute|lullaby|cover band|nightcore|sped up|8d|vitamin string quartet|piano tribute|the hit crew)\b`)
	// Versions that are legitimate but different from a studio original.
	variantTag = regexp.MustCompile(`(?i)\b(live|remix|acoustic|demo|unplugged|remaster(ed)?|radio edit|extended|edit)\b`)
)

// IsJunkVersion reports karaoke/tribute/sped-up style uploads.
func IsJunkVersion(title, artist string) bool {
	return junkTitle.MatchString(title) || junkArtist.MatchString(artist)
}

// VariantTags returns lowercase markers like "live" or "remix" found in a
// title, so a live seed can keep live results and a studio seed can drop them.
func VariantTags(title string) map[string]bool {
	out := map[string]bool{}
	for _, m := range variantTag.FindAllString(title, -1) {
		m = strings.ToLower(m)
		if strings.HasPrefix(m, "remaster") || m == "radio edit" || m == "edit" || m == "extended" {
			continue // harmless re-releases of the same recording
		}
		out[m] = true
	}
	return out
}
