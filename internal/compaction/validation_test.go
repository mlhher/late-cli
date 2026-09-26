package compaction

import "testing"

// TestStore_PutRejectsEmptyIDOrText pins the write-side validation: a record
// without an id is unreachable through any pointer and a record without text
// has nothing to reconstruct — both are no-ops that leave no trace in the
// records, the first-appearance order, or the digest.
func TestStore_PutRejectsEmptyIDOrText(t *testing.T) {
	s := NewStore()

	s.Put("", "orphan text")
	if got := s.Len(); got != 0 {
		t.Errorf("Put with an empty id stored a record (Len = %d)", got)
	}
	s.Put("id-1", "")
	if got := s.Len(); got != 0 {
		t.Errorf("Put with empty text stored a record (Len = %d)", got)
	}
	s.PutRecord(Record{ID: "", Text: "orphan text"})
	s.PutRecord(Record{ID: "id-1", Text: ""})
	if got := s.Len(); got != 0 {
		t.Errorf("PutRecord with an empty id or text stored a record (Len = %d)", got)
	}
	if entries := s.Digest(24_000); len(entries) != 0 {
		t.Errorf("Digest after rejected puts = %d entries, want 0", len(entries))
	}

	// Valid writes are untouched by the validation.
	s.Put("id-2", "real original")
	if text, ok := s.Get("id-2"); !ok || text != "real original" {
		t.Errorf("Get(id-2) = (%q, %v), want the stored original", text, ok)
	}
}
