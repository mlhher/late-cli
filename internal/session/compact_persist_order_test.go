package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCompactContextPersistsHistoryBeforeAdvancingHighWater pins the
// commit-order guarantee of a mutating walk: the compacted history file
// (which carries the pointers) is durable BEFORE the high-water sidecar
// advances. When the history save fails — disk full, unwritable path — the
// mark must stay where it was: advancing it would freeze messages below it
// that only exist in compacted form in memory, and the next run could never
// re-walk them.
func TestCompactContextPersistsHistoryBeforeAdvancingHighWater(t *testing.T) {
	tmp := t.TempDir()
	oldSessionDir := SessionDir
	SessionDir = func() (string, error) { return tmp, nil }
	defer func() { SessionDir = oldSessionDir }()

	fixture := defaultFixture()
	segs := fixtureSegments(t, fixture)

	// A history path whose parent is a regular file: SaveHistory's
	// MkdirAll fails with ENOTDIR, simulating an unwritable destination.
	blockedParent := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(blockedParent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(nil, filepath.Join(blockedParent, "history.json"), cloneHistory(fixture), "", false)

	report, err := s.CompactContext(nil, elideFirstScorer(segs), NewCompactStore(), CompactionOptions{})
	if err == nil {
		t.Fatalf("CompactContext() error = nil, want the history-save failure")
	}
	if !strings.Contains(err.Error(), "saving compacted history") {
		t.Fatalf("CompactContext() error = %v, want it to name the history save", err)
	}
	_ = report

	// The in-memory walk DID happen (pointers entered history) ...
	if !strings.Contains(s.History[4].Content.Text, "[[elided") {
		t.Fatalf("history[4] was not compacted in memory: %q", s.History[4].Content.Text)
	}
	// ... but the frozen prefix must NOT advance past the unsaved bytes.
	if got := s.CompactionHighWater(); got != 0 {
		t.Fatalf("CompactionHighWater = %d after a failed history save, want 0 (not advanced)", got)
	}
}

// TestCompactContextPersistsWalkToDisk pins the walk-internal persistence on
// the happy path: a mutating run with a real history path leaves the
// compacted history (pointers included) on disk, and the high-water mark
// advances — the same ordering, observed when nothing fails.
func TestCompactContextPersistsWalkToDisk(t *testing.T) {
	tmp := t.TempDir()
	oldSessionDir := SessionDir
	SessionDir = func() (string, error) { return tmp, nil }
	defer func() { SessionDir = oldSessionDir }()

	fixture := defaultFixture()
	segs := fixtureSegments(t, fixture)
	historyPath := filepath.Join(tmp, "session-compact-walk.json")
	s := New(nil, historyPath, cloneHistory(fixture), "", false)

	if _, err := s.CompactContext(nil, elideFirstScorer(segs), NewCompactStore(), CompactionOptions{}); err != nil {
		t.Fatalf("CompactContext() error = %v, want a clean walk", err)
	}

	loaded, err := LoadHistory(historyPath)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(loaded) != len(fixture) {
		t.Fatalf("on-disk history holds %d messages, want %d (compaction rewrites content, never count)", len(loaded), len(fixture))
	}
	if !strings.Contains(loaded[4].Content.Text, "[[elided") {
		t.Fatalf("on-disk history[4] = %q, want the compacted pointer form", loaded[4].Content.Text)
	}
	if got, want := s.CompactionHighWater(), len(fixture); got != want {
		t.Fatalf("CompactionHighWater = %d, want %d after the clean walk", got, want)
	}
}
