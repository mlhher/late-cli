package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"late/internal/compaction"
)

// fakeExpandStore is a minimal ExpandStore for unit-testing the tool layer.
type fakeExpandStore struct {
	originals map[string]string
	getCalled bool
}

func (s *fakeExpandStore) Get(id string) (string, bool) {
	s.getCalled = true
	text, ok := s.originals[id]
	return text, ok
}

func TestExpandTool_Metadata(t *testing.T) {
	e := ExpandTool{Store: &fakeExpandStore{}}
	if e.Name() != "expand" {
		t.Errorf("Name() = %q, want expand", e.Name())
	}
	if e.RequiresConfirmation(nil) {
		t.Error("RequiresConfirmation() = true, want false (read-only lookup)")
	}
	if !strings.Contains(e.Description(), "ORIGINAL") {
		t.Errorf("Description() should advertise original retrieval: %q", e.Description())
	}
	var params map[string]any
	if err := json.Unmarshal(e.Parameters(), &params); err != nil {
		t.Fatalf("Parameters() is not valid JSON: %v", err)
	}
	if params["type"] != "object" {
		t.Errorf("Parameters() type = %v, want object", params["type"])
	}
}

func TestExpandTool_Execute(t *testing.T) {
	store := &fakeExpandStore{originals: map[string]string{
		"elide-3": "the original segment text\n\n",
	}}
	e := ExpandTool{Store: store}

	// Known id → the stored original, byte-for-byte.
	got, err := e.Execute(context.Background(), []byte(`{"id":"elide-3"}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got != "the original segment text\n\n" {
		t.Errorf("Execute() = %q, want the stored original", got)
	}
	if !store.getCalled {
		t.Error("Execute() never consulted the store")
	}

	// Surrounding whitespace on the id is tolerated.
	if _, err := e.Execute(context.Background(), []byte(`{"id":" elide-3 "}`)); err != nil {
		t.Errorf("Execute() with padded id error = %v", err)
	}

	// Unknown id → the documented error result.
	_, err = e.Execute(context.Background(), []byte(`{"id":"elide-999"}`))
	if err == nil || !strings.Contains(err.Error(), "unknown elided id") {
		t.Errorf("Execute(unknown id) error = %v, want the unknown-elided-id error", err)
	}

	// Missing id → a required-parameter error.
	_, err = e.Execute(context.Background(), []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "id is required") {
		t.Errorf("Execute(missing id) error = %v, want the required-id error", err)
	}

	// Malformed arguments surface as invalid parameters.
	_, err = e.Execute(context.Background(), []byte(`not json`))
	if err == nil || !strings.Contains(err.Error(), "invalid parameters") {
		t.Errorf("Execute(bad json) error = %v, want an invalid-parameters error", err)
	}
}

// TestExpandTool_StoreWithoutEntries: a tool pointing at an empty store must
// not panic; it reports the id as unknown.
func TestExpandTool_StoreWithoutEntries(t *testing.T) {
	e := ExpandTool{Store: &fakeExpandStore{}}
	_, err := e.Execute(context.Background(), []byte(`{"id":"elide-1"}`))
	if err == nil || !strings.Contains(err.Error(), "unknown elided id") {
		t.Errorf("Execute() error = %v, want the unknown-elided-id error", err)
	}
}

// readShadowOutcomes parses a shadow log's JSONL lines into entries; the
// fake-free helper keeps the outcome assertions independent of the
// compaction package's own test helpers. A missing log file is zero
// outcomes: the log is only created on the first append.
func readShadowOutcomes(t *testing.T, path string) []compaction.ShadowEntry {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read shadow log: %v", err)
	}
	var out []compaction.ShadowEntry
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e compaction.ShadowEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("shadow line %d invalid: %v (%q)", i, err, line)
		}
		out = append(out, e)
	}
	return out
}

// TestExpandTool_WritesOutcomes: a successful expand bumps the record's
// expand counter and appends one expand outcome per record id AND one per
// contributing segment id — the Step 13 attribution ledger.
func TestExpandTool_WritesOutcomes(t *testing.T) {
	shadowPath := filepath.Join(t.TempDir(), "shadow.jsonl")
	shadow, err := compaction.NewShadowLogAt(shadowPath)
	if err != nil {
		t.Fatal(err)
	}
	store := compaction.NewStore().WithShadowLog(shadow)
	store.PutRecord(compaction.Record{
		ID:         "r:1a2b3c4d",
		Text:       "the original run",
		Tokens:     42,
		SegmentIDs: []string{"seg-1", "seg-2"},
	})

	e := ExpandTool{Store: store}
	if _, err := e.Execute(context.Background(), []byte(`{"id":"r:1a2b3c4d"}`)); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// The record's expand counter moved (Store.Touch ran).
	rec, ok := store.GetRecord("r:1a2b3c4d")
	if !ok {
		t.Fatal("record vanished from the store")
	}
	if rec.ExpandCount != 1 {
		t.Errorf("ExpandCount = %d, want 1", rec.ExpandCount)
	}

	outcomes := readShadowOutcomes(t, shadowPath)
	if len(outcomes) != 3 { // 1 record id + 2 segment ids
		t.Fatalf("got %d outcome lines, want 3", len(outcomes))
	}
	ids := make(map[string]bool, len(outcomes))
	for _, oc := range outcomes {
		if oc.Type != compaction.EntryTypeExpand {
			t.Errorf("outcome type = %q, want %q", oc.Type, compaction.EntryTypeExpand)
		}
		if oc.ItemID == "" {
			t.Error("outcome carries no item id")
		}
		ids[oc.ItemID] = true
	}
	for _, want := range []string{"r:1a2b3c4d", "seg-1", "seg-2"} {
		if !ids[want] {
			t.Errorf("no expand outcome attributed to %q (got %v)", want, ids)
		}
	}

	// A second retrieval appends another round of outcomes (the ledger is
	// append-only; Stats counts distinct ids).
	if _, err := e.Execute(context.Background(), []byte(`{"id":"r:1a2b3c4d"}`)); err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
	if got := len(readShadowOutcomes(t, shadowPath)); got != 6 {
		t.Errorf("after two expands got %d outcome lines, want 6", got)
	}
	if rec, _ := store.GetRecord("r:1a2b3c4d"); rec.ExpandCount != 2 {
		t.Errorf("ExpandCount after two expands = %d, want 2", rec.ExpandCount)
	}
}

// TestExpandTool_OutcomesWithoutShadowLog: a store with no shadow log
// attached (and, implicitly, one without persistence) still expands — the
// outcome ledger is best-effort and nil-safe, never a precondition.
func TestExpandTool_OutcomesWithoutShadowLog(t *testing.T) {
	store := compaction.NewStore()
	store.PutRecord(compaction.Record{
		ID:         "elide-3",
		Text:       "the original",
		SegmentIDs: []string{"seg-1"},
	})
	e := ExpandTool{Store: store}
	got, err := e.Execute(context.Background(), []byte(`{"id":"elide-3"}`))
	if err != nil || got != "the original" {
		t.Fatalf("Execute() = (%q, %v), want the original with no error", got, err)
	}
	if rec, _ := store.GetRecord("elide-3"); rec.ExpandCount != 1 {
		t.Errorf("ExpandCount = %d, want 1 (Touch still runs without a shadow log)", rec.ExpandCount)
	}
	if store.ShadowLog() != nil {
		t.Error("ShadowLog() = non-nil, want nil when nothing was attached")
	}
}

// TestExpandTool_UnknownIDRecordsNothing: a failed lookup must not touch the
// counters or the outcome ledger.
func TestExpandTool_UnknownIDRecordsNothing(t *testing.T) {
	shadowPath := filepath.Join(t.TempDir(), "shadow.jsonl")
	shadow, err := compaction.NewShadowLogAt(shadowPath)
	if err != nil {
		t.Fatal(err)
	}
	store := compaction.NewStore().WithShadowLog(shadow)
	store.PutRecord(compaction.Record{ID: "r:good", Text: "kept", SegmentIDs: []string{"seg-1"}})

	e := ExpandTool{Store: store}
	if _, err := e.Execute(context.Background(), []byte(`{"id":"r:missing"}`)); err == nil {
		t.Fatal("Execute(unknown id) error = nil, want the unknown-elided-id error")
	}
	if outcomes := readShadowOutcomes(t, shadowPath); len(outcomes) != 0 {
		t.Errorf("got %d outcome lines, want 0 (failed lookups record nothing)", len(outcomes))
	}
	if rec, _ := store.GetRecord("r:good"); rec.ExpandCount != 0 {
		t.Errorf("ExpandCount = %d, want 0 (the miss must not bump anything)", rec.ExpandCount)
	}
}

func TestExpandTool_CallString(t *testing.T) {
	e := ExpandTool{Store: &fakeExpandStore{}}
	if got := e.CallString([]byte(`{"id":"elide-3"}`)); !strings.Contains(got, "elide-3") {
		t.Errorf("CallString() = %q, want it to name the id", got)
	}
	if got := e.CallString([]byte(`{"id":"r:1a2b3c4d"}`)); !strings.Contains(got, "r:1a2b3c4d") {
		t.Errorf("CallString() = %q, want it to name the content id", got)
	}
}

// TestExpandTool_ContentIDsAndPointerLines: content-addressed ids
// ("r:<8hex>") resolve like legacy "elide-N" ids, and a whole pointer line
// may be passed in place of the bare id — its id is parsed out.
func TestExpandTool_ContentIDsAndPointerLines(t *testing.T) {
	store := &fakeExpandStore{originals: map[string]string{
		"r:1a2b3c4d": "the content-addressed original\n\nwith its tail",
		"elide-3":    "the legacy original",
	}}
	e := ExpandTool{Store: store}

	// Content id, plain.
	got, err := e.Execute(context.Background(), []byte(`{"id":"r:1a2b3c4d"}`))
	if err != nil || got != "the content-addressed original\n\nwith its tail" {
		t.Errorf("Execute(content id) = (%q, %v), want the stored original", got, err)
	}

	// Legacy id still works alongside it.
	if got, err := e.Execute(context.Background(), []byte(`{"id":"elide-3"}`)); err != nil || got != "the legacy original" {
		t.Errorf("Execute(legacy id) = (%q, %v), want the stored original", got, err)
	}

	// A whole pointer line (reference format, escaped quotes included)
	// resolves down to its id.
	pointer := `[[elided id=r:1a2b3c4d lines=3-9 tokens=310 "first \"quoted\" chars"]]`
	args, err := json.Marshal(map[string]string{"id": pointer})
	if err != nil {
		t.Fatalf("marshal pointer arg: %v", err)
	}
	if got, err := e.Execute(context.Background(), args); err != nil || got != "the content-addressed original\n\nwith its tail" {
		t.Errorf("Execute(pointer line) = (%q, %v), want the stored original", got, err)
	}

	// The description and parameter schema advertise the r: id format.
	if !strings.Contains(e.Description(), "r:1a2b3c4d") {
		t.Errorf("Description() must mention the r:<8hex> id format: %q", e.Description())
	}
	var params map[string]any
	if err := json.Unmarshal(e.Parameters(), &params); err != nil {
		t.Fatalf("Parameters() is not valid JSON: %v", err)
	}
	props := params["properties"].(map[string]any)
	idDesc := props["id"].(map[string]any)["description"].(string)
	if !strings.Contains(idDesc, "r:1a2b3c4d") || !strings.Contains(idDesc, "pointer line") {
		t.Errorf("id parameter description must document content ids and pointer lines: %q", idDesc)
	}

	// An unparseable pointer-ish argument stays a clean unknown-id error.
	if _, err := e.Execute(context.Background(), []byte(`{"id":"[[elided nope"}`)); err == nil || !strings.Contains(err.Error(), "unknown elided id") {
		t.Errorf("Execute(bad pointer) error = %v, want the unknown-id error", err)
	}
}
