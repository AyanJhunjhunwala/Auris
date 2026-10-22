// Package library is the user's saved-songs collection: a small JSON file
// on disk, written atomically. Each entry remembers why it was saved (the
// vibe label and steering in effect) so the library doubles as a map of
// past discoveries.
package library

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
)

const maxEntries = 5000

var ErrNotFound = errors.New("not in library")

type Entry struct {
	Track   model.Track   `json:"track"`
	SavedAt time.Time     `json:"savedAt"`
	From    string        `json:"from,omitempty"`  // seed it was discovered from ("Artist - Title")
	Vibe    string        `json:"vibe,omitempty"`  // agent label, if any
	Steer   []model.Steer `json:"steer,omitempty"` // steering in effect when saved
}

type Store struct {
	mu      sync.RWMutex
	path    string
	entries map[string]Entry // by track key
}

// Open loads the library from path (missing file = empty library).
func Open(path string) (*Store, error) {
	s := &Store{path: path, entries: map[string]Entry{}}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Entry
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, err
	}
	for _, e := range list {
		if e.Track.Key != "" {
			s.entries[e.Track.Key] = e
		}
	}
	return s, nil
}

// List returns entries, newest first.
func (s *Store) List() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sortedLocked()
}

func (s *Store) sortedLocked() []Entry {
	out := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SavedAt.After(out[j].SavedAt) })
	return out
}

// Add saves (or re-saves, refreshing metadata) a track.
func (s *Store) Add(e Entry) (Entry, error) {
	if e.Track.Key == "" || e.Track.Title == "" {
		return Entry{}, errors.New("track needs key and title")
	}
	e.Track.Features = nil // re-fetched on demand; keeps the file small
	if e.SavedAt.IsZero() {
		e.SavedAt = time.Now().UTC()
	}
	s.mu.Lock()
	if _, exists := s.entries[e.Track.Key]; !exists && len(s.entries) >= maxEntries {
		s.mu.Unlock()
		return Entry{}, errors.New("library is full")
	}
	s.entries[e.Track.Key] = e
	snapshot := s.sortedLocked()
	s.mu.Unlock()
	return e, s.save(snapshot)
}

func (s *Store) Remove(key string) error {
	s.mu.Lock()
	if _, ok := s.entries[key]; !ok {
		s.mu.Unlock()
		return ErrNotFound
	}
	delete(s.entries, key)
	snapshot := s.sortedLocked()
	s.mu.Unlock()
	return s.save(snapshot)
}

func (s *Store) save(list []Entry) error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
