package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOpenCompactionStoreAtPersistsAndReloads: the main() wiring helper
// returns a working file-backed store — a record stored before a reopen is
// still readable after it (the restart/resume contract of Step 12).
func TestOpenCompactionStoreAtPersistsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compaction-store.jsonl")
	store := openCompactionStoreAt(path)
	if store == nil {
		t.Fatal("openCompactionStoreAt must never return nil")
	}
	if store.Path() != path {
		t.Fatalf("Path() = %q, want %q", store.Path(), path)
	}
	store.Put("r:deadbeef", "original text")

	reopened := openCompactionStoreAt(path)
	if text, ok := reopened.Get("r:deadbeef"); !ok || text != "original text" {
		t.Errorf("reopened Get = (%q, %v), want the stored original", text, ok)
	}
}

// TestOpenCompactionStoreAtDegradesToInMemory: an unopenable path (its
// parent is a regular file) must still return a working in-memory store —
// compaction degrades but keeps working, the shadow-log warning pattern —
// and reports the degrade via its empty Path.
func TestOpenCompactionStoreAtDegradesToInMemory(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := openCompactionStoreAt(filepath.Join(blocker, "store.jsonl"))
	if store == nil {
		t.Fatal("the degraded fallback must return a usable store, not nil")
	}
	if store.Path() != "" {
		t.Errorf("degraded store Path() = %q, want empty (in-memory)", store.Path())
	}
	store.Put("r:deadbeef", "kept in memory")
	if text, ok := store.Get("r:deadbeef"); !ok || text != "kept in memory" {
		t.Errorf("degraded store Get = (%q, %v), want the in-memory text", text, ok)
	}
	if store.Len() != 1 {
		t.Errorf("degraded store Len() = %d, want 1", store.Len())
	}
}
