package engine

import (
	"strconv"
	"strings"

	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
	"github.com/ayanjhunjhunwala/auris/backend/internal/sources"
)

// pool deduplicates candidates across sources (entity resolution). Two
// sightings are the same song if they share any strong ID (MBID, ISRC,
// Spotify, Deezer), the exact TrackKey, the folded match key, or, within the
// same artist block, a fuzzy title match confirmed by duration (see
// resolve.go).
//
// Clusters live in a union-find. When one sighting matches two existing
// clusters (say, an ISRC links a Deezer record to a MusicBrainz one), the
// clusters are unioned, so the result doesn't depend on which source answered
// first. items always holds one representative per cluster.
type pool struct {
	items  []*model.Candidate // cluster representatives, in arrival order
	nodes  []*model.Candidate // every cluster ever created, by node id
	parent []int              // union-find parent pointers over nodes
	index  map[string]int     // match key -> some node in its cluster
	blocks map[string][]int   // folded lead artist -> node ids (fuzzy tier)

	exclude map[string]bool // ID keys of the seed (and reference) track
	// variants the seed itself carries ("live", "remix"...); candidates
	// with other variants are a different recording and get dropped.
	seedVariants map[string]bool
	seeds        []model.Track
}

// newPool's first track is the seed; any others are excluded as well.
func newPool(exclude ...model.Track) *pool {
	p := &pool{index: map[string]int{}, blocks: map[string][]int{}, exclude: map[string]bool{}, seedVariants: map[string]bool{}}
	if len(exclude) > 0 {
		p.seedVariants = sources.VariantTags(exclude[0].Title)
	}
	for _, t := range exclude {
		if t.Key == "" {
			t.Key = sources.TrackKey(t.Artist, t.Title)
		}
		for _, k := range allKeys(t) {
			p.exclude[k] = true
		}
		p.seeds = append(p.seeds, t)
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

// allKeys is idKeys plus the folded match key.
func allKeys(t model.Track) []string {
	return append(idKeys(t), matchKey(t))
}

func (p *pool) isSeed(t model.Track) bool {
	for _, k := range allKeys(t) {
		if p.exclude[k] {
			return true
		}
	}
	block := matchArtist(t.Artist)
	for _, s := range p.seeds {
		if matchArtist(s.Artist) == block {
			if _, ok := sameRecording(s, t); ok {
				return true
			}
		}
	}
	return false
}

func (p *pool) find(i int) int {
	for p.parent[i] != i {
		p.parent[i] = p.parent[p.parent[i]] // path halving
		i = p.parent[i]
	}
	return i
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

	// Tiers 1 and 2: every cluster this sighting shares a key with.
	var roots []int
	seen := map[int]bool{}
	for _, k := range allKeys(c.Track) {
		if n, ok := p.index[k]; ok {
			if r := p.find(n); !seen[r] {
				seen[r] = true
				roots = append(roots, r)
			}
		}
	}
	// Tier 3: fuzzy title match inside the artist block.
	block := matchArtist(c.Track.Artist)
	fuzzy := false
	if len(roots) == 0 {
		best, bestSim := -1, 0.0
		for _, n := range p.blocks[block] {
			r := p.find(n)
			if sim, ok := sameRecording(p.nodes[r].Track, c.Track); ok && sim > bestSim {
				best, bestSim = r, sim
			}
		}
		if best >= 0 {
			roots = append(roots, best)
			fuzzy = true
		}
	}

	rec := model.SourceRecord{Artist: c.Track.Artist, Title: c.Track.Title}
	if len(c.Evidence) > 0 {
		rec.Source = c.Evidence[0].Source
	}
	if len(roots) > 0 {
		rec.Match = matchKind(p.nodes[roots[0]].Track, c.Track, fuzzy)
	}
	c.Records = []model.SourceRecord{rec}

	if len(roots) == 0 {
		cc := c
		id := len(p.nodes)
		p.nodes = append(p.nodes, &cc)
		p.parent = append(p.parent, id)
		p.items = append(p.items, &cc)
		p.indexNode(id, cc.Track, block)
		return
	}

	// Merge into the first cluster, then union any others it bridges.
	root := roots[0]
	mergeInto(p.nodes[root], c)
	for _, r := range roots[1:] {
		mergeInto(p.nodes[root], *p.nodes[r])
		p.parent[r] = root
		p.dropItem(p.nodes[r])
	}
	p.indexNode(root, p.nodes[root].Track, block)
}

// indexNode points every key of t (and its artist block) at node id.
func (p *pool) indexNode(id int, t model.Track, block string) {
	for _, k := range allKeys(t) {
		p.index[k] = id
	}
	for _, n := range p.blocks[block] {
		if n == id {
			return
		}
	}
	p.blocks[block] = append(p.blocks[block], id)
}

func (p *pool) dropItem(c *model.Candidate) {
	for i, it := range p.items {
		if it == c {
			p.items = append(p.items[:i], p.items[i+1:]...)
			return
		}
	}
}

// matchKind says which tier joined t to the cluster represented by root.
func matchKind(root, t model.Track, fuzzy bool) string {
	switch {
	case fuzzy:
		return "fuzzy"
	case sources.TrackKey(root.Artist, root.Title) == sources.TrackKey(t.Artist, t.Title):
		return "exact"
	case matchKey(root) == matchKey(t):
		return "folded"
	default:
		return "id"
	}
}

func mergeInto(dst *model.Candidate, src model.Candidate) {
	for _, r := range src.Records {
		dup := false
		for _, d := range dst.Records {
			if d.Source == r.Source && d.Artist == r.Artist && d.Title == r.Title {
				dup = true
				break
			}
		}
		if !dup {
			dst.Records = append(dst.Records, r)
		}
	}
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
