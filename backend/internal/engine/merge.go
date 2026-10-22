package engine

import (
	"strconv"
	"strings"

	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
	"github.com/ayanjhunjhunwala/auris/backend/internal/sources"
)

// pool deduplicates candidates across sources. Two sightings are the same
// song if they share any strong ID (MBID, ISRC, Spotify, Deezer) or, failing
// that, the normalised artist|title key.
type pool struct {
	items   []*model.Candidate
	index   map[string]int
	exclude map[string]bool // ID keys of the seed (and reference) track
	// variants the seed itself carries ("live", "remix"...); candidates
	// with other variants are a different recording and get dropped.
	seedVariants map[string]bool
}

// newPool's first track is the seed; any others are excluded as well.
func newPool(exclude ...model.Track) *pool {
	p := &pool{index: map[string]int{}, exclude: map[string]bool{}, seedVariants: map[string]bool{}}
	if len(exclude) > 0 {
		p.seedVariants = sources.VariantTags(exclude[0].Title)
	}
	for _, t := range exclude {
		for _, k := range idKeys(t) {
			p.exclude[k] = true
		}
	}
	return p
}

func idKeys(t model.Track) []string {
	keys := []string{"k:" + t.Key}
	if t.IDs.MBID != "" {
		keys = append(keys, "mbid:"+t.IDs.MBID)
	}
	if t.IDs.ISRC != "" {
		keys = append(keys, "isrc:"+strings.ToUpper(t.IDs.ISRC))
	}
	if t.IDs.SpotifyID != "" {
		keys = append(keys, "sp:"+t.IDs.SpotifyID)
	}
	if t.IDs.DeezerID != 0 {
		keys = append(keys, "dz:"+strconv.FormatInt(t.IDs.DeezerID, 10))
	}
	return keys
}

func (p *pool) isSeed(t model.Track) bool {
	for _, k := range idKeys(t) {
		if p.exclude[k] {
			return true
		}
	}
	return false
}

func (p *pool) add(c model.Candidate) {
	if c.Track.Key == "" {
		c.Track.Key = sources.TrackKey(c.Track.Artist, c.Track.Title)
	}
	if c.Track.Title == "" || c.Track.Artist == "" || p.isSeed(c.Track) ||
		sources.IsJunkVersion(c.Track.Title, c.Track.Artist) {
		return
	}
	for v := range sources.VariantTags(c.Track.Title) {
		if !p.seedVariants[v] {
			return // e.g. a live cut when the seed is the studio version
		}
	}
	keys := idKeys(c.Track)
	for _, k := range keys {
		if i, ok := p.index[k]; ok {
			mergeInto(p.items[i], c)
			for _, k2 := range idKeys(p.items[i].Track) {
				p.index[k2] = i
			}
			return
		}
	}
	cc := c
	p.items = append(p.items, &cc)
	for _, k := range keys {
		p.index[k] = len(p.items) - 1
	}
}

func mergeInto(dst *model.Candidate, src model.Candidate) {
	d, s := &dst.Track, src.Track
	fill := func(a *string, b string) {
		if *a == "" {
			*a = b
		}
	}
	fill(&d.Album, s.Album)
	fill(&d.ArtworkURL, s.ArtworkURL)
	fill(&d.PreviewURL, s.PreviewURL)
	fill(&d.URL, s.URL)
	fill(&d.ArtistID, s.ArtistID)
	fill(&d.IDs.ISRC, s.IDs.ISRC)
	fill(&d.IDs.MBID, s.IDs.MBID)
	fill(&d.IDs.SpotifyID, s.IDs.SpotifyID)
	fill(&d.IDs.ReccoBeatsID, s.IDs.ReccoBeatsID)
	fill(&d.IDs.ReleaseMBID, s.IDs.ReleaseMBID)
	if d.IDs.DeezerID == 0 {
		d.IDs.DeezerID, d.IDs.DeezerArtistID = s.IDs.DeezerID, s.IDs.DeezerArtistID
	}
	if d.DurationMs == 0 {
		d.DurationMs = s.DurationMs
	}
	if d.Popularity == nil {
		d.Popularity = s.Popularity
	}
	d.Features = mergeFeatures(d.Features, s.Features)

	for _, e := range src.Evidence {
		found := false
		for i := range dst.Evidence {
			if dst.Evidence[i].Source == e.Source {
				found = true
				if e.Score > dst.Evidence[i].Score {
					dst.Evidence[i] = e
				}
			}
		}
		if !found {
			dst.Evidence = append(dst.Evidence, e)
		}
	}
}

func mergeFeatures(a, b *model.Features) *model.Features {
	if a.IsEmpty() {
		return b
	}
	if b.IsEmpty() {
		return a
	}
	out := *a
	for _, d := range dims {
		if get(&out, d.name) == nil {
			if v := get(b, d.name); v != nil {
				set(&out, d.name, *v)
			}
		}
	}
	return &out
}
