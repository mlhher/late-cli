package compaction

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// openFileStore opens a file-backed store under t.TempDir() and fails the
// test on error.
func openFileStore(t *testing.T, name string) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore(%s) error = %v", path, err)
	}
	return s, path
}

// storeLines reads a JSONL file and returns its non-blank lines.
func storeLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store %s: %v", path, err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// sampleRecord builds a fully-populated record whose text exercises JSON
// escaping (quotes, backslashes, newlines).
func sampleRecord(id string) Record {
	return Record{
		ID:         id,
		Text:       "line one \"quoted\" \\slash\nline two",
		Kind:       RecordKindElidedSegment,
		Origin:     Origin{Source: "tool:Bash", Ref: "call_9", Turn: 0},
		Tokens:     42,
		Summary:    `line one "quoted" \slash …`,
		SegmentIDs: []string{"seg-2", "seg-3"},
	}
}

// TestStore_PersistReloadRoundTrip is the Step 12 core pin: a record put
// into a file-backed store comes back byte-identical — full Record, not
// just text — from a freshly reopened store, and the legacy Get view still
// answers with the text.
func TestStore_PersistReloadRoundTrip(t *testing.T) {
	s, path := openFileStore(t, "store.jsonl")
	want := sampleRecord("r:aaaa1111")
	s.PutRecord(want)
	legacy := "legacy original text"
	s.Put("elide-3", legacy)

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen OpenStore(%s) error = %v", path, err)
	}
	got, ok := reopened.GetRecord("r:aaaa1111")
	if !ok {
		t.Fatalf("reopened store must hold a record for r:aaaa1111")
	}
	if !reflect.DeepEqual(got, &want) {
		t.Errorf("reopened record = %+v, want %+v", got, &want)
	}
	if text, ok := reopened.Get("r:aaaa1111"); !ok || text != want.Text {
		t.Errorf("reopened Get = (%q, %v), want the record text", text, ok)
	}
	if text, ok := reopened.Get("elide-3"); !ok || text != legacy {
		t.Errorf("reopened Get(elide-3) = (%q, %v), want the legacy original", text, ok)
	}
	if reopened.Len() != 2 {
		t.Errorf("reopened Len() = %d, want 2", reopened.Len())
	}
	// Records() reports first-appearance order and the same snapshots.
	recs := reopened.Records()
	if len(recs) != 2 || recs[0].ID != "r:aaaa1111" || recs[1].ID != "elide-3" {
		t.Errorf("Records() ids = %v, want [r:aaaa1111 elide-3]", recIDs(recs))
	}
	if !reflect.DeepEqual(recs[0], got) {
		t.Errorf("Records()[0] = %+v, want the same snapshot as GetRecord", recs[0])
	}
}

// TestStore_PutDefaultsAndLegacyView: a record written through the legacy
// string Put is stored as a full record — default kind, estimated token
// count, pointer summary — so even legacy-written ids carry metadata.
func TestStore_PutDefaultsAndLegacyView(t *testing.T) {
	s, _ := openFileStore(t, "store.jsonl")
	s.Put("elide-7", "some original run text")

	rec, ok := s.GetRecord("elide-7")
	if !ok {
		t.Fatal("Put must create a readable record")
	}
	if rec.Text != "some original run text" {
		t.Errorf("Text = %q, want the stored original", rec.Text)
	}
	if rec.Kind != RecordKindElidedSegment {
		t.Errorf("Kind = %q, want the default %q", rec.Kind, RecordKindElidedSegment)
	}
	if rec.Tokens <= 0 {
		t.Errorf("Tokens = %d, want the estimated count", rec.Tokens)
	}
	if want := Summarise("some original run text", SummaryMaxChars); rec.Summary != want {
		t.Errorf("Summary = %q, want %q", rec.Summary, want)
	}
}

// TestStore_PutIdempotentKeepsFirstRecord: re-storing under a stored id
// keeps the first record, appends no second line, and reload sees the
// first content — content ids make duplicate puts the norm, and the file
// must not grow for them.
func TestStore_PutIdempotentKeepsFirstRecord(t *testing.T) {
	s, path := openFileStore(t, "store.jsonl")
	first := sampleRecord("r:aaaa2222")
	s.PutRecord(first)
	// Same id, different everything: must be ignored wholesale.
	dup := first
	dup.Text = "second content"
	dup.Tokens = 999
	dup.Origin = Origin{Source: "history"}
	s.PutRecord(dup)
	s.Put("r:aaaa2222", "third content")

	if text, _ := s.Get("r:aaaa2222"); text != first.Text {
		t.Errorf("Get = %q, want the first record's text kept", text)
	}
	if s.Len() != 1 {
		t.Errorf("Len() = %d, want 1", s.Len())
	}
	lines := storeLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("store file has %d lines, want 1 (no line for an idempotent re-put)", len(lines))
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	got, ok := reopened.GetRecord("r:aaaa2222")
	if !ok || !reflect.DeepEqual(got, &first) {
		t.Errorf("reopened record = %+v (%v), want the first record %+v", got, ok, &first)
	}
}

// TestStore_TouchCountersSurviveReload: Touch bumps the expand/hit
// counters in memory, persists them (last-writer-wins full-record lines),
// leaves unknown ids and flag-less touches alone, and the counters survive
// a reopen.
func TestStore_TouchCountersSurviveReload(t *testing.T) {
	s, path := openFileStore(t, "store.jsonl")
	s.PutRecord(sampleRecord("r:aaaa3333"))
	id := "r:aaaa3333"
	if !s.Touch(id, true, false) || !s.Touch(id, true, false) || !s.Touch(id, false, true) {
		t.Fatal("Touch on a stored record must report found")
	}
	if rec, _ := s.GetRecord(id); rec.ExpandCount != 2 || rec.HitCount != 1 {
		t.Errorf("counters = (expand %d, hit %d), want (2, 1)", rec.ExpandCount, rec.HitCount)
	}
	if s.Touch("r:unknown", true, true) {
		t.Error("Touch on an unknown id must report not-found")
	}
	before := len(storeLines(t, path))
	if !s.Touch(id, false, false) {
		t.Error("flag-less Touch on a stored record reports found")
	}
	if len(storeLines(t, path)) != before {
		t.Error("a flag-less Touch must not append a line")
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	rec, ok := reopened.GetRecord(id)
	if !ok {
		t.Fatal("reopened store must hold the touched record")
	}
	if rec.ExpandCount != 2 || rec.HitCount != 1 {
		t.Errorf("reopened counters = (expand %d, hit %d), want (2, 1)", rec.ExpandCount, rec.HitCount)
	}
	if rec.Tokens != 42 {
		t.Errorf("reopened Tokens = %d, want the untouched 42", rec.Tokens)
	}
}

// TestStore_NextIDResumesPastLoadedLegacyIDs: the legacy counter restarts
// past every elide-N id already on disk, so minted keys stay distinct from
// loaded ones across restarts.
func TestStore_NextIDResumesPastLoadedLegacyIDs(t *testing.T) {
	s, path := openFileStore(t, "store.jsonl")
	s.Put("elide-1", "one")
	s.Put("elide-3", "three")

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	if got := reopened.NextID(); got != "elide-4" {
		t.Errorf("NextID() = %q, want elide-4 (resumed past the loaded elide-3)", got)
	}
	// In-memory stores keep the 1-based counter.
	if got := NewStore().NextID(); got != "elide-1" {
		t.Errorf("fresh NextID() = %q, want elide-1", got)
	}
}

// TestStore_ConcurrentPutsWriteWholeLines: concurrent puts on a file-backed
// store are race-free (run under -race), every persisted line is complete
// JSON (crash-atomic per line), and a reopen sees every record.
func TestStore_ConcurrentPutsWriteWholeLines(t *testing.T) {
	s, path := openFileStore(t, "store.jsonl")
	const n = 32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			rec := sampleRecord(contentIDFor(k))
			s.PutRecord(rec)
			// Concurrent reads race the writes; both are mutex-guarded.
			s.Get(rec.ID)
			s.GetRecord(rec.ID)
		}(i)
	}
	wg.Wait()
	if s.Len() != n {
		t.Errorf("Len() = %d, want %d", s.Len(), n)
	}
	for i, line := range storeLines(t, path) {
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil || rec.ID == "" {
			t.Fatalf("store line %d is not a complete JSON record: %v (%q)", i+1, err, line)
		}
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	for k := 0; k < n; k++ {
		rec, ok := reopened.GetRecord(contentIDFor(k))
		if !ok {
			t.Errorf("reopened store lost record %d", k)
		} else if rec.Tokens != 42 {
			t.Errorf("record %d tokens = %d, want 42", k, rec.Tokens)
		}
	}
}

// contentIDFor mints a distinct id per concurrent writer.
func contentIDFor(k int) string {
	return fmt.Sprintf("r:conc%04d", k)
}

// TestStore_TornTrailingLineSkippedAndRepaired: a crash mid-append leaves
// a torn trailing line; loading skips it, the reopen repairs the framing,
// and records appended afterwards stay readable instead of welding onto
// the tail.
func TestStore_TornTrailingLineSkippedAndRepaired(t *testing.T) {
	s, path := openFileStore(t, "store.jsonl")
	s.PutRecord(sampleRecord("r:aaaa4444"))
	s.Put("elide-5", "second record")

	// Simulate the crash: a torn record line without its newline.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":"r:torn","text":"cut o`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen with torn tail error = %v", err)
	}
	if _, ok := reopened.GetRecord("r:torn"); ok {
		t.Error("the torn line must not load as a record")
	}
	if reopened.Len() != 2 {
		t.Errorf("reopened Len() = %d, want the 2 intact records", reopened.Len())
	}
	// The repaired framing keeps the next append on its own line.
	reopened.Put("r:aaaa5555", "third record")

	again, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen after append error = %v", err)
	}
	if text, ok := again.Get("r:aaaa5555"); !ok || text != "third record" {
		t.Errorf("Get(r:aaaa5555) = (%q, %v), want the post-repair record", text, ok)
	}
	if again.Len() != 3 {
		t.Errorf("Len() = %d, want 3", again.Len())
	}
}

// TestStore_OpenMissingFileAndPerms: opening a store whose file does not
// exist yet is an empty store (not an error); the first append creates the
// file 0600 and OpenStore created the parent directories 0700.
func TestStore_OpenMissingFileAndPerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not apply on Windows")
	}
	path := filepath.Join(t.TempDir(), "nested", "deeper", "store.jsonl")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore on a missing file error = %v, want an empty store", err)
	}
	if s.Len() != 0 {
		t.Errorf("fresh store Len() = %d, want 0", s.Len())
	}
	if _, ok := s.Get("r:anything"); ok {
		t.Error("fresh store must report misses")
	}
	s.Put("r:first", "text")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("store file not created on first append: %v", err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Errorf("store file mode = %o, want 600", got)
	}
	dirSt, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("store dir missing: %v", err)
	}
	if got := dirSt.Mode().Perm(); got != 0o700 {
		t.Errorf("store dir mode = %o, want 700", got)
	}
}

// TestStore_InMemoryAndEmptyPath: NewStore() and OpenStore("") are purely
// in-memory (no path, nothing persisted) — the tests/legacy contract.
func TestStore_InMemoryAndEmptyPath(t *testing.T) {
	s := NewStore()
	if s.Path() != "" {
		t.Errorf("NewStore().Path() = %q, want empty", s.Path())
	}
	s.Put("r:mem", "in-memory")
	if text, ok := s.Get("r:mem"); !ok || text != "in-memory" {
		t.Errorf("Get = (%q, %v), want the in-memory text", text, ok)
	}

	empty, err := OpenStore("")
	if err != nil {
		t.Fatalf("OpenStore(\"\") error = %v, want the in-memory store", err)
	}
	if empty.Path() != "" {
		t.Errorf("OpenStore(\"\").Path() = %q, want empty", empty.Path())
	}
	empty.Put("r:mem2", "also in-memory")
	if empty.Len() != 1 {
		t.Errorf("Len() = %d, want 1", empty.Len())
	}
}

// TestStore_NilStoreNoOps pins the nil-store contract: every method is a
// safe no-op with zero-value results.
func TestStore_NilStoreNoOps(t *testing.T) {
	var s *Store
	if text, ok := s.Get("r:x"); text != "" || ok {
		t.Error("nil Get must report a miss")
	}
	if rec, ok := s.GetRecord("r:x"); rec != nil || ok {
		t.Error("nil GetRecord must report a miss")
	}
	if recs := s.Records(); recs != nil {
		t.Error("nil Records must be nil")
	}
	if s.Len() != 0 {
		t.Error("nil Len must be 0")
	}
	if id := s.NextID(); id != "" {
		t.Error("nil NextID must be empty")
	}
	if p := s.Path(); p != "" {
		t.Error("nil Path must be empty")
	}
	s.Put("r:x", "text")                         // must not panic
	s.PutRecord(Record{ID: "r:x", Text: "text"}) // must not panic
	if s.Touch("r:x", true, true) {
		t.Error("nil Touch must report not-found")
	}
}

// recIDs maps records to their ids (test helper).
func recIDs(recs []*Record) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.ID)
	}
	return out
}
