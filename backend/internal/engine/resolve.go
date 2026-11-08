package engine

import (
	"strings"

	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
	"github.com/ayanjhunjhunwala/auris/backend/internal/sources"
)

// Entity resolution helpers for the candidate pool.
//
// Matching runs in three tiers, cheapest first:
//
//  1. Strong IDs (ISRC, MBID, Spotify, Deezer) and the exact TrackKey.
//  2. A folded match key: TrackKey after accent folding, "&" -> "and" and a
//     leading "the " dropped, so "Beyoncé"/"Beyonce" and "The Beatles"/
//     "Beatles" land on the same key.
//  3. Fuzzy: within the same artist block, Jaro-Winkler similarity on the
//     folded titles, confirmed by duration when both sides have one.
//
// TrackKey itself is unchanged (it keys the library, caches and served
// signals); the folded key only exists inside the pool.

const (
	// Titles this similar (0..1) are treated as the same recording when the
	// durations agree, or when neither side reports a duration.
	fuzzyWithDuration = 0.88
	fuzzyNoDuration   = 0.94
	// Durations further apart than this are different recordings.
	durationTolerance = 3000 // ms
)

// foldAccents maps common Latin diacritics to ASCII. The backend is
// stdlib-only, so this is a small table rather than full NFKD.
var foldAccents = strings.NewReplacer(
	"à", "a", "á", "a", "â", "a", "ã", "a", "ä", "a", "å", "a", "ā", "a", "ă", "a", "ą", "a",
	"ç", "c", "ć", "c", "č", "c",
	"ď", "d", "đ", "d",
	"è", "e", "é", "e", "ê", "e", "ë", "e", "ē", "e", "ė", "e", "ę", "e", "ě", "e",
	"ğ", "g",
	"ì", "i", "í", "i", "î", "i", "ï", "i", "ī", "i", "ı", "i",
	"ł", "l",
	"ñ", "n", "ń", "n", "ň", "n",
	"ò", "o", "ó", "o", "ô", "o", "õ", "o", "ö", "o", "ø", "o", "ō", "o", "ő", "o",
	"ř", "r",
	"ś", "s", "š", "s", "ş", "s", "ß", "ss",
	"ť", "t",
	"ù", "u", "ú", "u", "û", "u", "ü", "u", "ū", "u", "ů", "u", "ű", "u",
	"ý", "y", "ÿ", "y",
	"ź", "z", "ż", "z", "ž", "z",
	"æ", "ae", "œ", "oe",
)

func fold(s string) string {
	return foldAccents.Replace(strings.ToLower(s))
}

// matchArtist is the folded lead artist: the blocking key for fuzzy matching.
func matchArtist(artist string) string {
	a := sources.PrimaryArtist(fold(artist))
	return strings.TrimPrefix(a, "the ")
}

// matchTitle is the folded, normalised title.
func matchTitle(title string) string {
	t := fold(title)
	t = strings.ReplaceAll(t, "&", " and ")
	return sources.NormTitle(t)
}

// matchKey is the tier-2 key.
func matchKey(t model.Track) string {
	return "m:" + matchArtist(t.Artist) + "|" + matchTitle(t.Title)
}

// sameRecording is the tier-3 comparator. Callers have already blocked on
// matchArtist, so only the title and duration are compared here. It returns
// the similarity and whether it clears the threshold.
func sameRecording(a, b model.Track) (float64, bool) {
	if !sameVariants(a.Title, b.Title) {
		return 0, false // "Song" vs "Song (Live)" are different recordings
	}
	ta, tb := matchTitle(a.Title), matchTitle(b.Title)
	if !sameNumbers(ta, tb) {
		return 0, false // "Kapitel 218" vs "Kapitel 279", "Part 1" vs "Part 2"
	}
	sim := jaroWinkler(ta, tb)
	if a.DurationMs > 0 && b.DurationMs > 0 {
		d := a.DurationMs - b.DurationMs
		if d < 0 {
			d = -d
		}
		if d > durationTolerance {
			return sim, false
		}
		return sim, sim >= fuzzyWithDuration
	}
	return sim, sim >= fuzzyNoDuration
}

// sameNumbers reports whether two titles contain the same digit runs in the
// same order. Titles that differ only by a number (chapters, parts, volumes)
// look nearly identical to Jaro-Winkler but are different recordings.
func sameNumbers(a, b string) bool {
	na, nb := digitRuns(a), digitRuns(b)
	if len(na) != len(nb) {
		return false
	}
	for i := range na {
		if na[i] != nb[i] {
			return false
		}
	}
	return true
}

func digitRuns(s string) []string {
	var out []string
	start := -1
	for i, r := range s {
		isDigit := r >= '0' && r <= '9'
		if isDigit && start < 0 {
			start = i
		}
		if !isDigit && start >= 0 {
			out = append(out, strings.TrimLeft(s[start:i], "0"))
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, strings.TrimLeft(s[start:], "0"))
	}
	return out
}

func sameVariants(a, b string) bool {
	va, vb := sources.VariantTags(a), sources.VariantTags(b)
	if len(va) != len(vb) {
		return false
	}
	for k := range va {
		if !vb[k] {
			return false
		}
	}
	return true
}

// jaroWinkler returns the Jaro-Winkler similarity of two strings, 0..1.
func jaroWinkler(s1, s2 string) float64 {
	a, b := []rune(s1), []rune(s2)
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	window := max(len(a), len(b))/2 - 1
	if window < 0 {
		window = 0
	}
	matchA := make([]bool, len(a))
	matchB := make([]bool, len(b))
	matches := 0
	for i := range a {
		lo, hi := max(0, i-window), min(len(b)-1, i+window)
		for j := lo; j <= hi; j++ {
			if !matchB[j] && a[i] == b[j] {
				matchA[i], matchB[j] = true, true
				matches++
				break
			}
		}
	}
	if matches == 0 {
		return 0
	}
	transpositions, k := 0, 0
	for i := range a {
		if !matchA[i] {
			continue
		}
		for !matchB[k] {
			k++
		}
		if a[i] != b[k] {
			transpositions++
		}
		k++
	}
	m := float64(matches)
	jaro := (m/float64(len(a)) + m/float64(len(b)) + (m-float64(transpositions)/2)/m) / 3
	prefix := 0
	for prefix < min(4, len(a), len(b)) && a[prefix] == b[prefix] {
		prefix++
	}
	return jaro + float64(prefix)*0.1*(1-jaro)
}
