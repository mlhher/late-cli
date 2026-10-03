package compaction

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			lines = append(lines, sc.Text())
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return lines
}

func TestShadowLog_AppendAndReadBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "shadow.jsonl")
	l, err := NewShadowLogAt(path)
	if err != nil {
		t.Fatalf("NewShadowLogAt() error = %v", err)
	}
	if l.Path() != path {
		t.Errorf("Path() = %q, want %q", l.Path(), path)
	}

	ts := time.Unix(1700000000, 0).UTC()
	entries := []ShadowEntry{
		{TS: ts, TaskHash: "abc123", SegmentID: "seg-1", Tokens: 120, Score: 0.42, Decision: DecisionKeep},
		{SegmentID: "seg-2", Tokens: 30, Score: 0.9}, // defaults: TS and Decision
	}
	for i, e := range entries {
		if err := l.Append(e); err != nil {
			t.Fatalf("Append(%d) error = %v", i, err)
		}
	}

	lines := readLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	var got []ShadowEntry
	for i, line := range lines {
		var e ShadowEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("line %d is not valid JSON: %v", i, err)
		}
		got = append(got, e)
	}
	if got[0] != entries[0] {
		t.Errorf("entry 0 = %+v, want %+v", got[0], entries[0])
	}
	if got[1].TS.IsZero() {
		t.Error("entry 1 TS was not defaulted to now")
	}
	if got[1].Decision != DecisionKeep {
		t.Errorf("entry 1 Decision = %q, want %q", got[1].Decision, DecisionKeep)
	}

	// The created directory must be private (0700).
	if info, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("stat dir: %v", err)
	} else if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir perms = %o, want 700", perm)
	}
}

func TestShadowLog_ReplayMath(t *testing.T) {
	l, err := NewShadowLogAt(filepath.Join(t.TempDir(), "shadow.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	entries := []ShadowEntry{
		{SegmentID: "seg-keep", Tokens: 50, Score: 0.9},
		{SegmentID: "seg-drop", Tokens: 100, Score: 0.2},
		{SegmentID: "seg-drop", Tokens: 100, Score: 0.3}, // same segment re-scored
		{SegmentID: "seg-mid", Tokens: 25, Score: 0.5},   // exactly at threshold: kept
	}
	for _, e := range entries {
		if err := l.Append(e); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}

	report, err := l.Replay(0.5)
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	want := ReplayReport{
		Threshold:      0.5,
		Entries:        4,
		UniqueSegments: 3,
		ElidedEntries:  2, // both seg-drop decisions (0.2, 0.3 < 0.5)
		ElidedSegments: 1,
		TokensTotal:    275,
		TokensElided:   200,
	}
	if report != want {
		t.Errorf("Replay() = %+v, want %+v", report, want)
	}
}

func TestShadowLog_AppendRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shadow.jsonl")
	l, err := NewShadowLogAt(path)
	if err != nil {
		t.Fatal(err)
	}
	run := RunSummary{
		Shadow:       true,
		Scanned:      6,
		Scored:       5,
		Elided:       3,
		TokensBefore: 1000,
		TokensAfter:  700,
		TokensSaved:  300,
	}
	if err := l.AppendRun("task-hash", run); err != nil {
		t.Fatalf("AppendRun() error = %v", err)
	}

	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	var e ShadowEntry
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil {
		t.Fatalf("run line is not valid JSON: %v (%q)", err, lines[0])
	}
	if e.Type != EntryTypeHistoryRun {
		t.Errorf("Type = %q, want %q", e.Type, EntryTypeHistoryRun)
	}
	if e.TaskHash != "task-hash" {
		t.Errorf("TaskHash = %q, want task-hash", e.TaskHash)
	}
	if e.TS.IsZero() {
		t.Error("run entry TS was not defaulted to now")
	}
	if e.SegmentID != "" || e.Decision != "" {
		t.Errorf("run entry must carry no per-segment fields, got segment_id=%q decision=%q", e.SegmentID, e.Decision)
	}
	if e.Run == nil {
		t.Fatal("Run is nil")
	}
	if *e.Run != run {
		t.Errorf("Run = %+v, want %+v", *e.Run, run)
	}
}

// TestShadowLog_AppendOutcome: outcomes append as first-class lines —
// Type/ItemID/Turn persisted, TS defaulted, no decision defaulted in — and
// unknown kinds or empty item ids are rejected before anything is written.
func TestShadowLog_AppendOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shadow.jsonl")
	l, err := NewShadowLogAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.AppendOutcome(EntryTypeExpand, "r:1a2b3c4d", 0); err != nil {
		t.Fatalf("AppendOutcome(expand) error = %v", err)
	}
	if err := l.AppendOutcome(EntryTypeHit, "seg-2", 7); err != nil {
		t.Fatalf("AppendOutcome(hit) error = %v", err)
	}
	if err := l.AppendOutcome("bogus", "seg-1", 0); err == nil {
		t.Error("AppendOutcome(unknown kind) error = nil, want an error")
	}
	if err := l.AppendOutcome(EntryTypeExpand, "", 0); err == nil {
		t.Error("AppendOutcome(empty item id) error = nil, want an error")
	}

	lines := readLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2 (rejected outcomes must not write)", len(lines))
	}
	var first, second ShadowEntry
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("outcome line invalid: %v (%q)", err, lines[0])
	}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("outcome line invalid: %v (%q)", err, lines[1])
	}
	if first.Type != EntryTypeExpand || first.ItemID != "r:1a2b3c4d" {
		t.Errorf("first outcome = type %q item %q, want expand/r:1a2b3c4d", first.Type, first.ItemID)
	}
	if first.Turn != 0 {
		t.Errorf("first outcome Turn = %d, want 0", first.Turn)
	}
	if second.Type != EntryTypeHit || second.ItemID != "seg-2" || second.Turn != 7 {
		t.Errorf("second outcome = type %q item %q turn %d, want hit/seg-2/7", second.Type, second.ItemID, second.Turn)
	}
	// Outcomes are not decisions: no SegmentID, no defaulted Decision.
	for i, e := range []ShadowEntry{first, second} {
		if e.SegmentID != "" {
			t.Errorf("outcome %d carries segment_id %q, want none (outcomes name ItemID)", i, e.SegmentID)
		}
		if e.Decision != "" {
			t.Errorf("outcome %d carries decision %q, want none", i, e.Decision)
		}
		if e.TS.IsZero() {
			t.Errorf("outcome %d TS was not defaulted to now", i)
		}
	}
}

// TestShadowLog_Stats: decisions count, elided-at-own-threshold counts,
// distinct outcome item ids, and non-decision entries skipped.
func TestShadowLog_Stats(t *testing.T) {
	l, err := NewShadowLogAt(filepath.Join(t.TempDir(), "shadow.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	entries := []ShadowEntry{
		{SegmentID: "seg-a", Tokens: 10, Score: 0.1, Threshold: 0.35}, // elided at own threshold
		{SegmentID: "seg-b", Tokens: 20, Score: 0.2, Threshold: 0.35}, // elided at own threshold
		{SegmentID: "seg-c", Tokens: 30, Score: 0.9, Threshold: 0.35}, // kept
		{SegmentID: "seg-legacy", Score: 0.1},                         // legacy: no threshold, never elided
		{SegmentID: "seg-d", Tokens: 5, Score: 0.5, Threshold: 0},     // 0 threshold = none in force
		{SegmentID: "seg-e", Tokens: 5, Score: 0.5, Threshold: 0.5},   // exactly at: kept
	}
	for _, e := range entries {
		if err := l.Append(e); err != nil {
			t.Fatalf("Append(%s) error = %v", e.SegmentID, err)
		}
	}
	// Non-decision lines must not count as decisions.
	if err := l.AppendRun("task", RunSummary{Scanned: 1}); err != nil {
		t.Fatalf("AppendRun() error = %v", err)
	}
	if err := l.Append(ShadowEntry{Type: EntryTypeTripwire, Action: TripwireAction, Tokens: 99}); err != nil {
		t.Fatalf("Append(tripwire) error = %v", err)
	}
	// Outcomes: r:1 twice (one distinct), seg-a once (hit), seg-b once.
	for _, oc := range []struct {
		kind, id string
	}{{EntryTypeExpand, "r:1"}, {EntryTypeExpand, "r:1"}, {EntryTypeHit, "seg-a"}, {EntryTypeExpand, "seg-b"}} {
		if err := l.AppendOutcome(oc.kind, oc.id, 0); err != nil {
			t.Fatalf("AppendOutcome(%s, %s) error = %v", oc.kind, oc.id, err)
		}
	}

	st, err := l.Stats()
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	want := Stats{Decisions: 6, ElidedDecisions: 2, Expands: 2, Hits: 1}
	if st != want {
		t.Errorf("Stats() = %+v, want %+v", st, want)
	}
}

// TestShadowLog_FalseNegativeRate: the rate counts distinct segments elided
// at their own recorded threshold that got a LATER expand outcome — an
// expand recorded before the elision does not link.
func TestShadowLog_FalseNegativeRate(t *testing.T) {
	l, err := NewShadowLogAt(filepath.Join(t.TempDir(), "shadow.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// seg-a: elided, then expanded → missed.
	if err := l.Append(ShadowEntry{SegmentID: "seg-a", Score: 0.1, Threshold: 0.35}); err != nil {
		t.Fatal(err)
	}
	if err := l.AppendOutcome(EntryTypeExpand, "seg-a", 0); err != nil {
		t.Fatal(err)
	}
	// seg-b: elided, never expanded → denominator only.
	if err := l.Append(ShadowEntry{SegmentID: "seg-b", Score: 0.2, Threshold: 0.35}); err != nil {
		t.Fatal(err)
	}
	// seg-c: expanded BEFORE the elision decision → not "later", not missed.
	if err := l.AppendOutcome(EntryTypeExpand, "seg-c", 0); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(ShadowEntry{SegmentID: "seg-c", Score: 0.05, Threshold: 0.35}); err != nil {
		t.Fatal(err)
	}
	// seg-d: kept (score above threshold) and expanded — irrelevant either way.
	if err := l.Append(ShadowEntry{SegmentID: "seg-d", Score: 0.9, Threshold: 0.35}); err != nil {
		t.Fatal(err)
	}
	if err := l.AppendOutcome(EntryTypeExpand, "seg-d", 0); err != nil {
		t.Fatal(err)
	}
	// A record-level outcome (r:…) must not link to any segment id.
	if err := l.AppendOutcome(EntryTypeExpand, "r:abcd1234", 0); err != nil {
		t.Fatal(err)
	}

	got, err := l.FalseNegativeRate()
	if err != nil {
		t.Fatalf("FalseNegativeRate() error = %v", err)
	}
	want := 1.0 / 3.0 // seg-a missed of {seg-a, seg-b, seg-c} elided
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("FalseNegativeRate() = %v, want %v", got, want)
	}
}

// TestShadowLog_FalseNegativeRateEmptyLedger: no elided decisions (empty
// log, missing file, or only kept entries) is a 0 rate, not an error.
func TestShadowLog_FalseNegativeRateEmptyLedger(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		l, err := NewShadowLogAt(filepath.Join(t.TempDir(), "never-written.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		got, err := l.FalseNegativeRate()
		if err != nil || got != 0 {
			t.Errorf("FalseNegativeRate() = (%v, %v), want (0, nil)", got, err)
		}
	})
	t.Run("only kept decisions", func(t *testing.T) {
		l, err := NewShadowLogAt(filepath.Join(t.TempDir(), "shadow.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if err := l.Append(ShadowEntry{SegmentID: "seg-a", Score: 0.9, Threshold: 0.35}); err != nil {
			t.Fatal(err)
		}
		got, err := l.FalseNegativeRate()
		if err != nil || got != 0 {
			t.Errorf("FalseNegativeRate() = (%v, %v), want (0, nil)", got, err)
		}
	})
}

// TestShadowLog_ReplayTable: each row re-decides every recorded entry from
// its OWN score against the GIVEN threshold (never the entry's recorded
// one), sums relocated tokens, counts distinct later-expanded segments as
// still missed, and sorts rows by threshold ascending.
func TestShadowLog_ReplayTable(t *testing.T) {
	l, err := NewShadowLogAt(filepath.Join(t.TempDir(), "shadow.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	entries := []ShadowEntry{
		{SegmentID: "seg-a", Tokens: 10, Score: 0.9, Threshold: 0.35},
		{SegmentID: "seg-b", Tokens: 20, Score: 0.2, Threshold: 0.35},
		{SegmentID: "seg-b", Tokens: 20, Score: 0.3, Threshold: 0.35}, // re-scored
		{SegmentID: "seg-c", Tokens: 5, Score: 0.5, Threshold: 0.35},
	}
	for _, e := range entries {
		if err := l.Append(e); err != nil {
			t.Fatalf("Append(%s) error = %v", e.SegmentID, err)
		}
	}
	// seg-b was later expanded: still missed at every threshold that
	// relocates either of its entries.
	if err := l.AppendOutcome(EntryTypeExpand, "seg-b", 0); err != nil {
		t.Fatal(err)
	}
	// Deliberately unsorted: the table must come back ascending.
	rows, err := l.ReplayTable([]float64{0.35, 0.1, 0.25})
	if err != nil {
		t.Fatalf("ReplayTable() error = %v", err)
	}
	want := []ReplayRow{
		{Threshold: 0.1, Kept: 4, Relocated: 0, TokensSaved: 0, StillMissed: 0},
		{Threshold: 0.25, Kept: 3, Relocated: 1, TokensSaved: 20, StillMissed: 1},
		{Threshold: 0.35, Kept: 2, Relocated: 2, TokensSaved: 40, StillMissed: 1},
	}
	if len(rows) != len(want) {
		t.Fatalf("ReplayTable() returned %d rows, want %d", len(rows), len(want))
	}
	for i, row := range rows {
		if row != want[i] {
			t.Errorf("ReplayTable()[%d] = %+v, want %+v", i, row, want[i])
		}
	}
}

// TestShadowLog_ReplayTableMissingFileIsEmpty: a missing log yields a zero
// row per threshold, not an error.
func TestShadowLog_ReplayTableMissingFileIsEmpty(t *testing.T) {
	l, err := NewShadowLogAt(filepath.Join(t.TempDir(), "never-written.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := l.ReplayTable([]float64{0.35, 0.5})
	if err != nil {
		t.Fatalf("ReplayTable() error = %v", err)
	}
	want := []ReplayRow{{Threshold: 0.35}, {Threshold: 0.5}}
	if len(rows) != len(want) {
		t.Fatalf("ReplayTable() returned %d rows, want %d", len(rows), len(want))
	}
	for i, row := range rows {
		if row != want[i] {
			t.Errorf("ReplayTable()[%d] = %+v, want %+v", i, row, want[i])
		}
	}
}

func TestShadowLog_ReplaySkipsHistoryRunEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shadow.jsonl")
	l, err := NewShadowLogAt(path)
	if err != nil {
		t.Fatal(err)
	}
	// A real log mixes per-segment decisions with run summary lines; only
	// the decisions may count toward the replay math.
	for _, e := range []ShadowEntry{
		{SegmentID: "seg-keep", Tokens: 50, Score: 0.9},
		{SegmentID: "seg-drop", Tokens: 100, Score: 0.2},
	} {
		if err := l.Append(e); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}
	if err := l.AppendRun("task-hash", RunSummary{Scanned: 4, Scored: 4, Elided: 1, TokensBefore: 500, TokensAfter: 400, TokensSaved: 100}); err != nil {
		t.Fatalf("AppendRun() error = %v", err)
	}
	if err := l.Append(ShadowEntry{SegmentID: "seg-drop", Tokens: 100, Score: 0.3}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	report, err := l.Replay(0.5)
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	want := ReplayReport{
		Threshold:      0.5,
		Entries:        3, // the run summary line must not count
		UniqueSegments: 2,
		ElidedEntries:  2, // both seg-drop decisions (0.2, 0.3 < 0.5)
		ElidedSegments: 1,
		TokensTotal:    250,
		TokensElided:   200,
	}
	if report != want {
		t.Errorf("Replay() = %+v, want %+v", report, want)
	}
	if report.MalformedLines != 0 {
		t.Errorf("MalformedLines = %d, want 0 (run summaries are skipped, not malformed)", report.MalformedLines)
	}
}

func TestShadowLog_ReplayMissingFileIsEmpty(t *testing.T) {
	l, err := NewShadowLogAt(filepath.Join(t.TempDir(), "never-written.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := l.Replay(0.5)
	if err != nil {
		t.Fatalf("Replay() error = %v (a missing log is empty, not an error)", err)
	}
	if report.Entries != 0 || report.Threshold != 0.5 {
		t.Errorf("Replay() = %+v, want an empty report at threshold 0.5", report)
	}
}

func TestShadowLog_ReplaySkipsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shadow.jsonl")
	l, err := NewShadowLogAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(ShadowEntry{SegmentID: "seg-1", Tokens: 10, Score: 0.1}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("this is not json\n\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := l.Append(ShadowEntry{SegmentID: "seg-2", Tokens: 20, Score: 0.9}); err != nil {
		t.Fatal(err)
	}

	report, err := l.Replay(0.5)
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if report.MalformedLines != 1 {
		t.Errorf("MalformedLines = %d, want 1", report.MalformedLines)
	}
	if report.Entries != 2 || report.ElidedEntries != 1 {
		t.Errorf("Replay() = %+v, want 2 entries with 1 elided", report)
	}
}

func TestShadowLog_ConcurrentAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shadow.jsonl")
	l, err := NewShadowLogAt(path)
	if err != nil {
		t.Fatal(err)
	}

	const (
		goroutines = 20
		perWorker  = 10
	)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				e := ShadowEntry{
					SegmentID: fmt.Sprintf("seg-%d-%d", g, i),
					Tokens:    1,
					Score:     0.5,
				}
				if err := l.Append(e); err != nil {
					t.Errorf("Append() error = %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	lines := readLines(t, path)
	if len(lines) != goroutines*perWorker {
		t.Fatalf("got %d lines, want %d — appends were lost or torn", len(lines), goroutines*perWorker)
	}
	seen := make(map[string]bool, len(lines))
	for i, line := range lines {
		var e ShadowEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("line %d torn or invalid: %v (%q)", i, err, line)
		}
		if seen[e.SegmentID] {
			t.Errorf("duplicate entry for %s — a write was interleaved", e.SegmentID)
		}
		seen[e.SegmentID] = true
	}
}

func TestShadowLog_AppendFailureOnUnwritablePath(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewShadowLogAt(filepath.Join(blocker, "shadow.jsonl")); err == nil {
		t.Error("NewShadowLogAt() under a file path error = nil, want a mkdir failure")
	}

	// A log whose path is a directory makes every append fail.
	l, err := NewShadowLogAt(filepath.Join(dir, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(l.Path(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(ShadowEntry{SegmentID: "seg-1"}); err == nil {
		t.Error("Append() to a directory error = nil, want a failure")
	}
}

func TestShadowLog_EmptyPathRejected(t *testing.T) {
	if _, err := NewShadowLogAt(""); err == nil {
		t.Error("NewShadowLogAt(\"\") error = nil, want an error")
	}
}

func TestDefaultShadowPath(t *testing.T) {
	p, err := DefaultShadowPath()
	if err != nil {
		t.Skipf("no home dir available: %v", err)
	}
	if !strings.HasSuffix(p, filepath.Join(".local", "share", "late", "compaction-shadow.jsonl")) {
		t.Errorf("DefaultShadowPath() = %q, want it under ~/.local/share/late/compaction-shadow.jsonl", p)
	}
}

func TestHashTask(t *testing.T) {
	a := HashTask("write the parser")
	b := HashTask("write the parser")
	c := HashTask("write the parser!")
	if a != b {
		t.Errorf("HashTask is not deterministic: %q vs %q", a, b)
	}
	if a == c {
		t.Error("HashTask collided on different inputs")
	}
	if len(a) != 16 {
		t.Errorf("HashTask length = %d, want 16 hex chars", len(a))
	}
	// Distinct tasks must hash differently from their raw text (the point
	// of hashing is that the raw text is not logged).
	if strings.Contains(a, "parser") {
		t.Error("HashTask leaked raw task text")
	}
}
