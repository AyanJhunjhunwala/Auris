// Package lang guesses the language a song is sung in, for the language
// filter. Two tiers:
//
//   - Heuristic: Unicode script of the title/artist settles non-Latin
//     languages (Hangul -> ko, kana -> ja, Cyrillic -> ru, ...); for Latin
//     script, stopword hits in the title pick es/pt/fr/de/it, defaulting
//     to English.
//   - LLM (optional): one batched call per request classifies the tracks the
//     heuristic can't be sure of. Models know artists ("Bad Bunny" -> es)
//     far better than titles do. Results are cached per track.
package lang

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ayanjhunjhunwala/auris/backend/internal/cache"
	"github.com/ayanjhunjhunwala/auris/backend/internal/llm"
	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
)

// Instrumental is the ISO 639-2 "no linguistic content" code.
const Instrumental = "zxx"

// Supported lists the filter options the UI offers, in display order.
var Supported = []struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}{
	{"en", "English"}, {"es", "Spanish"}, {"pt", "Portuguese"}, {"fr", "French"},
	{"de", "German"}, {"it", "Italian"}, {"ja", "Japanese"}, {"ko", "Korean"},
	{"zh", "Chinese"}, {"hi", "Hindi"}, {"ar", "Arabic"}, {"ru", "Russian"},
	{Instrumental, "Instrumental"},
}

// Guess is a language with a confidence flag.
type Guess struct {
	Code string
	Sure bool
}

var scripts = []struct {
	table *unicode.RangeTable
	code  string
}{
	{unicode.Hangul, "ko"},
	{unicode.Hiragana, "ja"},
	{unicode.Katakana, "ja"},
	{unicode.Han, "zh"}, // after kana: Japanese titles mix both
	{unicode.Cyrillic, "ru"},
	{unicode.Arabic, "ar"},
	{unicode.Devanagari, "hi"},
	{unicode.Thai, "th"},
	{unicode.Hebrew, "he"},
	{unicode.Greek, "el"},
	{unicode.Bengali, "bn"},
	{unicode.Tamil, "ta"},
	{unicode.Gurmukhi, "pa"},
}

var stopwords = map[string][]string{
	"es": {"el", "la", "los", "las", "de", "que", "y", "en", "mi", "tu", "te", "me", "amor", "corazón", "corazon", "noche", "por", "para", "con", "sin", "una", "un", "yo", "quiero", "vida"},
	"pt": {"o", "os", "as", "do", "da", "dos", "não", "nao", "meu", "minha", "você", "voce", "amor", "coração", "coracao", "saudade", "com", "sem", "eu", "um", "uma"},
	"fr": {"le", "la", "les", "de", "des", "du", "et", "je", "tu", "ne", "pas", "mon", "ma", "moi", "toi", "amour", "c'est", "pour", "avec", "sans", "une", "un", "nous"},
	"de": {"der", "die", "das", "und", "ich", "du", "nicht", "ist", "mein", "dein", "liebe", "mit", "ohne", "für", "fur", "ein", "eine", "wir", "auf"},
	"it": {"il", "lo", "la", "gli", "le", "di", "che", "e", "non", "mio", "mia", "amore", "cuore", "per", "con", "senza", "una", "un", "io", "sei"},
}

// Heuristic guesses from text alone.
func Heuristic(title, artist string) Guess {
	text := title + " " + artist
	for _, s := range scripts {
		for _, r := range text {
			if unicode.Is(s.table, r) {
				return Guess{s.code, true}
			}
		}
	}
	words := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return !unicode.IsLetter(r) && r != '\''
	})
	best, bestScore, second := "", 0, 0
	for code, list := range stopwords {
		score := 0
		for _, w := range words {
			for _, s := range list {
				if w == s {
					score++
				}
			}
		}
		if score > bestScore {
			best, second, bestScore = code, bestScore, score
		} else if score > second {
			second = score
		}
	}
	if bestScore >= 1 && bestScore > second {
		return Guess{best, bestScore >= 2}
	}
	// Latin script with no foreign signal: in a Western catalogue that is
	// most likely English, but not something to be sure of.
	return Guess{"en", false}
}

// LLM is the slice of llm.Client the detector needs.
type LLM interface {
	Enabled() bool
	Stream(ctx context.Context, msgs []llm.Message, cb llm.Callbacks) (string, *llm.Usage, error)
}

type Detector struct {
	LLM   LLM
	cache *cache.LRU[string]
}

func NewDetector(l LLM) *Detector {
	return &Detector{LLM: l, cache: cache.New[string](20000, 30*24*time.Hour)}
}

// Detect sets Track.Language on each track. Instrumental tracks (by audio
// features) are tagged zxx; heuristics settle the rest unless unsure, in
// which case one batched LLM call decides (if configured).
func (d *Detector) Detect(ctx context.Context, tracks []*model.Track) {
	var unsure []*model.Track
	for _, t := range tracks {
		if f := t.Features; f != nil && f.Instrumentalness != nil && *f.Instrumentalness >= 0.6 {
			t.Language = Instrumental
			continue
		}
		if code, ok := d.cache.Get(t.Key); ok && t.Key != "" {
			t.Language = code
			continue
		}
		g := Heuristic(t.Title, t.Artist)
		t.Language = g.Code
		if !g.Sure {
			unsure = append(unsure, t)
		}
	}
	if len(unsure) == 0 || d.LLM == nil || !d.LLM.Enabled() {
		return
	}
	// Small batches in parallel: one call for the whole head is slow enough
	// to time out, and a failed batch only costs its own tracks.
	var wg sync.WaitGroup
	var mu sync.Mutex
	for lo := 0; lo < len(unsure); lo += batchSize {
		batch := unsure[lo:min(lo+batchSize, len(unsure))]
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := d.classify(ctx, batch)
			if err != nil {
				return // keep heuristic guesses
			}
			mu.Lock()
			defer mu.Unlock()
			for _, t := range batch {
				if code, ok := got[t.Key]; ok {
					t.Language = code
					d.cache.Set(t.Key, code)
				}
			}
		}()
	}
	wg.Wait()
}

// batchSize is how many songs one classification call covers.
const batchSize = 30

func (d *Detector) classify(ctx context.Context, tracks []*model.Track) (map[string]string, error) {
	var b strings.Builder
	for i, t := range tracks {
		fmt.Fprintf(&b, "%d. %s - %s\n", i+1, t.Artist, t.Title)
	}
	msgs := []llm.Message{
		{Role: "system", Content: `For each numbered song, give the ISO 639-1 code of the main language of its lyrics, or "zxx" if it is instrumental. Use what you know about the artist. Respond with ONLY a JSON object mapping the number to the code, e.g. {"1":"en","2":"es"}.`},
		{Role: "user", Content: b.String()},
	}
	noop := func(string) {}
	text, _, err := d.LLM.Stream(ctx, msgs, llm.Callbacks{OnContent: noop, OnReasoning: noop})
	if err != nil {
		return nil, err
	}
	i, j := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if i < 0 || j <= i {
		return nil, fmt.Errorf("no JSON in language response")
	}
	var raw map[string]string
	if err := json.Unmarshal([]byte(text[i:j+1]), &raw); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k, code := range raw {
		var n int
		if _, err := fmt.Sscanf(k, "%d", &n); err != nil || n < 1 || n > len(tracks) {
			continue
		}
		code = strings.ToLower(strings.TrimSpace(code))
		if len(code) == 2 || code == Instrumental {
			out[tracks[n-1].Key] = code
		}
	}
	return out, nil
}

// Matches reports whether a track passes the filter.
func Matches(t model.Track, want map[string]bool) bool {
	return want[t.Language]
}
