package library

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ayanjhunjhunwala/auris/backend/internal/model"
)

func TestAddListRemovePersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lib.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if _, err := s.Add(Entry{Track: model.Track{Key: "a|1", Title: "One", Features: &model.Features{}}, SavedAt: old}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Entry{Track: model.Track{Key: "b|2", Title: "Two"}, Vibe: "Late night"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Entry{Track: model.Track{Title: "no key"}}); err == nil {
		t.Error("entry without key should be rejected")
	}

	re, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	list := re.List()
	if len(list) != 2 || list[0].Track.Key != "b|2" || list[0].Vibe != "Late night" {
		t.Fatalf("newest first, persisted: %+v", list)
	}
	if list[1].Track.Features != nil {
		t.Error("features should not be persisted")
	}
	if err := re.Remove("a|1"); err != nil || len(re.List()) != 1 {
		t.Fatalf("remove: %v", err)
	}
	if err := re.Remove("nope"); err != ErrNotFound {
		t.Fatalf("remove missing: %v", err)
	}
}
