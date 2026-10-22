// Package model holds the provider-neutral types shared by every layer of
// the backend: source adapters produce them, the engine scores them, and the
// API serialises them for the dashboard.
package model

import "time"

// Features is the Spotify-style audio-feature vector. Pointers distinguish
// "unknown" from a real zero; the engine imputes unknowns rather than
// pretending a track has zero energy.
type Features struct {
	Acousticness     *float64 `json:"acousticness,omitempty"`
	Danceability     *float64 `json:"danceability,omitempty"`
	Energy           *float64 `json:"energy,omitempty"`
	Instrumentalness *float64 `json:"instrumentalness,omitempty"`
	Liveness         *float64 `json:"liveness,omitempty"`
	Loudness         *float64 `json:"loudness,omitempty"` // dB, typically -60..0
	Speechiness      *float64 `json:"speechiness,omitempty"`
	Tempo            *float64 `json:"tempo,omitempty"` // BPM
	Valence          *float64 `json:"valence,omitempty"`
	Key              *int     `json:"key,omitempty"`
	Mode             *int     `json:"mode,omitempty"`
}

// IsEmpty reports whether no feature is known.
func (f *Features) IsEmpty() bool {
	if f == nil {
		return true
	}
	return f.Acousticness == nil && f.Danceability == nil && f.Energy == nil &&
		f.Instrumentalness == nil && f.Liveness == nil && f.Loudness == nil &&
		f.Speechiness == nil && f.Tempo == nil && f.Valence == nil
}

// IDs collects every external identifier we know for a track. Merging two
// sightings of the same song is mostly a matter of unioning these.
type IDs struct {
	ISRC           string `json:"isrc,omitempty"`
	MBID           string `json:"mbid,omitempty"` // MusicBrainz recording MBID
	SpotifyID      string `json:"spotifyId,omitempty"`
	ReccoBeatsID   string `json:"reccobeatsId,omitempty"`
	DeezerID       int64  `json:"deezerId,omitempty"`
	DeezerArtistID int64  `json:"deezerArtistId,omitempty"`
	ReleaseMBID    string `json:"releaseMbid,omitempty"`
}

// Track is a single song as Auris understands it.
type Track struct {
	Key        string    `json:"key"` // normalised "artist|title", the merge key
	Title      string    `json:"title"`
	Artist     string    `json:"artist"`
	ArtistID   string    `json:"artistId,omitempty"` // MusicBrainz artist MBID when known
	Album      string    `json:"album,omitempty"`
	ArtworkURL string    `json:"artworkUrl,omitempty"`
	PreviewURL string    `json:"previewUrl,omitempty"` // 30s mp3 (Deezer)
	URL        string    `json:"url,omitempty"`        // canonical web link
	DurationMs int       `json:"durationMs,omitempty"`
	Popularity *float64  `json:"popularity,omitempty"` // 0..1
	IDs        IDs       `json:"ids"`
	Features   *Features `json:"features,omitempty"`
	Language   string    `json:"language,omitempty"` // ISO 639-1, or "zxx" for instrumental
}

// SourceName identifies an upstream provider.
type SourceName string

const (
	SourceLastFM       SourceName = "lastfm"
	SourceReccoBeats   SourceName = "reccobeats"
	SourceDeezer       SourceName = "deezer"
	SourceListenBrainz SourceName = "listenbrainz"
)

// Evidence is one source's opinion that a candidate resembles the seed.
type Evidence struct {
	Source SourceName `json:"source"`
	Rank   int        `json:"rank"`  // 0-based position in that source's list
	Score  float64    `json:"score"` // source-native similarity normalised to 0..1
	Reason string     `json:"reason,omitempty"`
}

// Candidate is a track proposed by at least one source, before scoring.
type Candidate struct {
	Track    Track      `json:"track"`
	Evidence []Evidence `json:"evidence"`
}

// Breakdown exposes every signal the ranker used, so the dashboard can show
// *why* a track was picked and the learner can train on it.
type Breakdown struct {
	Signals map[string]float64 `json:"signals"`
	Logit   float64            `json:"logit"`
}

// Recommendation is a scored candidate in final order.
type Recommendation struct {
	Track     Track      `json:"track"`
	Score     float64    `json:"score"` // 0..1
	Evidence  []Evidence `json:"evidence"`
	Breakdown Breakdown  `json:"breakdown"`
	Reasons   []string   `json:"reasons"`
}

// SourceStatus is the outcome of one source call within a request.
type SourceStatus string

const (
	StatusOK       SourceStatus = "ok"
	StatusEmpty    SourceStatus = "empty"
	StatusError    SourceStatus = "error"
	StatusDisabled SourceStatus = "disabled"
	StatusSkipped  SourceStatus = "skipped" // missing an ID it needs
	StatusTimeout  SourceStatus = "timeout"
)

// SourceReport is per-request telemetry for one stage/source.
type SourceReport struct {
	Source    SourceName   `json:"source"`
	Stage     string       `json:"stage"` // resolve | reference | candidates | features | language | enrich
	Status    SourceStatus `json:"status"`
	LatencyMs int64        `json:"latencyMs"`
	Count     int          `json:"count"`
	Error     string       `json:"error,omitempty"`
}

// SeedQuery identifies the song the user wants neighbours for. Either free
// text or artist+title is enough; extra IDs skip resolution steps.
type SeedQuery struct {
	Query    string `json:"query,omitempty"`
	Artist   string `json:"artist,omitempty"`
	Title    string `json:"title,omitempty"`
	DeezerID int64  `json:"deezerId,omitempty"`
	ISRC     string `json:"isrc,omitempty"`
}

// Steer is a request to bend results along named directions ("upbeat",
// "chill", ...). Intensity is 0..1 - where on the spoke the user clicked.
type Steer struct {
	Direction string  `json:"direction"`
	Intensity float64 `json:"intensity"`
}

// SimilarRequest is the body of POST /api/similar.
type SimilarRequest struct {
	Seed    SeedQuery    `json:"seed"`
	Limit   int          `json:"limit,omitempty"`
	Steer   []Steer      `json:"steer,omitempty"`
	Sources []SourceName `json:"sources,omitempty"` // optional allow-list

	// Targets pins absolute feature values ("tempo 140", "energy 0.9").
	// Applied after Steer and Reference, so explicit values win.
	Targets *Features `json:"targets,omitempty"`
	// Reference is "make it sound like this other song": its audio profile
	// pulls the target toward it by ReferenceWeight (0..1, default 0.6) and
	// it joins the seed list of sources that accept several seeds.
	Reference       *SeedQuery `json:"reference,omitempty"`
	ReferenceWeight float64    `json:"referenceWeight,omitempty"`

	// Languages keeps only songs sung in these ISO 639-1 languages ("zxx"
	// for instrumental). Empty means any.
	Languages []string `json:"languages,omitempty"`
}

// SimilarResponse is the payload of POST /api/similar.
type SimilarResponse struct {
	Seed            Track            `json:"seed"`
	Target          *Features        `json:"target,omitempty"`    // seed features after steering
	Reference       *Track           `json:"reference,omitempty"` // resolved "sound like" track
	Recommendations []Recommendation `json:"recommendations"`
	Sources         []SourceReport   `json:"sources"`
	CandidatePool   int              `json:"candidatePool"`
	TookMs          int64            `json:"tookMs"`
	GeneratedAt     time.Time        `json:"generatedAt"`
}

// Ptr is a small helper for building optional fields.
func Ptr[T any](v T) *T { return &v }
