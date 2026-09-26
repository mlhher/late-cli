package compaction

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStoreDigest_BudgetDropsOldest pins the budget packing: entries start
// in first-appearance (oldest first) order, the oldest are dropped until
// the token sum fits, and the survivors come back largest-last.
func TestStoreDigest_BudgetDropsOldest(t *testing.T) {
	s := NewStore()
	s.PutRecord(Record{ID: "r:old", Text: "old run", Tokens: 100, CreatedTurn: 1})
	s.PutRecord(Record{ID: "r:mid", Text: "mid run", Tokens: 50, CreatedTurn: 2})
	s.PutRecord(Record{ID: "r:new", Text: "new run", Tokens: 200, CreatedTurn: 3})

	// 350 total tokens: a 300-token budget drops the oldest record; the
	// survivors come back largest-last (50 before 200).
	got := s.Digest(300)
	if len(got) != 2 {
		t.Fatalf("Digest(300) returned %d entries, want 2: %+v", len(got), got)
	}
	if got[0].ID != "r:mid" || got[1].ID != "r:new" {
		t.Errorf("Digest(300) = [%s, %s], want [r:mid, r:new] (oldest dropped, largest last)",
			got[0].ID, got[1].ID)
	}

	// An exact budget keeps everything.
	if got := s.Digest(350); len(got) != 3 {
		t.Fatalf("Digest(350) returned %d entries, want 3 (exact budget)", len(got))
	}

	// A budget smaller than any single record keeps nothing.
	if got := s.Digest(10); len(got) != 0 {
		t.Fatalf("Digest(10) returned %d entries, want 0", len(got))
	}

	// A nil store is an empty digest, not a panic.
	if got := (*Store)(nil).Digest(1000); got != nil {
		t.Fatalf("nil store Digest = %+v, want nil", got)
	}
}

// TestStoreDigest_SummaryFallback: the record's pointer summary is the
// digest's summary; a record without one falls back to a truncated first
// line of the original text.
func TestStoreDigest_SummaryFallback(t *testing.T) {
	s := NewStore()
	s.PutRecord(Record{ID: "r:1", Text: "first line\nsecond line", Summary: "explicit summary", Tokens: 10})
	s.PutRecord(Record{ID: "r:2", Text: "   \n  the real first line   \n  more text", Tokens: 10})

	got := s.Digest(1000)
	if len(got) != 2 {
		t.Fatalf("Digest returned %d entries, want 2", len(got))
	}
	byID := map[string]DigestEntry{}
	for _, e := range got {
		byID[e.ID] = e
	}
	if byID["r:1"].Summary != "explicit summary" {
		t.Errorf("r:1 summary = %q, want the record's own pointer summary", byID["r:1"].Summary)
	}
	if byID["r:2"].Summary != "the real first line" {
		t.Errorf("r:2 summary = %q, want the flattened first non-blank line", byID["r:2"].Summary)
	}
	if byID["r:1"].Kind != RecordKindElidedSegment {
		t.Errorf("r:1 kind = %q, want the stored default %q", byID["r:1"].Kind, RecordKindElidedSegment)
	}
	if byID["r:1"].CreatedTurn != 0 {
		t.Errorf("r:1 created_turn = %d, want 0 (no turn plumbing yet)", byID["r:1"].CreatedTurn)
	}
}

// TestStoreDigest_LargestLastTieOrder: equal-size entries keep their
// first-appearance order (the ordering sort is stable).
func TestStoreDigest_LargestLastTieOrder(t *testing.T) {
	s := NewStore()
	s.PutRecord(Record{ID: "r:a", Text: "a", Tokens: 7})
	s.PutRecord(Record{ID: "r:b", Text: "b", Tokens: 3})
	s.PutRecord(Record{ID: "r:c", Text: "c", Tokens: 7})

	got := s.Digest(1000)
	want := []string{"r:b", "r:a", "r:c"} // 3 first, then the two 7s in insertion order
	if len(got) != len(want) {
		t.Fatalf("Digest returned %d entries, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("Digest[%d].ID = %s, want %s", i, got[i].ID, id)
		}
	}
}

// retrieveTestPipeline builds a pipeline over a scripted decisions server
// with a temp shadow log and a fixed clock.
func retrieveTestPipeline(t *testing.T, handler func(int, capturedRequest) (int, string)) (*Pipeline, *ShadowLog) {
	t.Helper()
	d := newDecisionsServer(t, handler)
	shadow, err := NewShadowLogAt(filepath.Join(t.TempDir(), "shadow.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	backend := ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}
	p := NewPipeline(backend, "k", shadow, PipelineOptions{})
	now := time.Unix(1700000000, 0).UTC()
	p.now = func() time.Time { return now }
	return p, shadow
}

// shadowLines reads the log's JSONL lines, treating a not-yet-created file
// (nothing has ever been appended) as zero lines — readLines fatals on the
// missing file, and "nothing was logged" assertions are exactly about that.
func shadowLines(t *testing.T, l *ShadowLog) []string {
	t.Helper()
	if _, err := os.Stat(l.Path()); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("stat %s: %v", l.Path(), err)
	}
	return readLines(t, l.Path())
}

func retrieveTestStore() *Store {
	s := NewStore()
	s.PutRecord(Record{ID: "r:a", Text: "original text a", Summary: "a summary", Tokens: 10})
	s.PutRecord(Record{ID: "r:b", Text: "original text b", Summary: "b summary", Tokens: 20})
	s.PutRecord(Record{ID: "r:c", Text: "original text c", Summary: "c summary", Tokens: 30})
	s.PutRecord(Record{ID: "r:d", Text: "original text d", Summary: "d summary", Tokens: 40})
	return s
}

// TestRetrieve_TopKThresholdTiesAndDecisions pins the core selection: rank
// by score descending, ties by ascending id, take the top k at or above the
// threshold, and log one kind=retrieve decision per scored entry with the
// injected/skipped action.
func TestRetrieve_TopKThresholdTiesAndDecisions(t *testing.T) {
	p, shadow := retrieveTestPipeline(t, fixedScoresHandler(map[string]float64{
		"r:a": 0.9, "r:b": 0.9, "r:c": 0.6, "r:d": 0.4,
	}))

	records, err := p.Retrieve(context.Background(), "fix the login bug", retrieveTestStore(), 2, 1000, 0.5)
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	// a and b tie at 0.9 — ascending ids break the tie; c (0.6) overflows
	// k=2; d (0.4) is below the threshold.
	if len(records) != 2 {
		t.Fatalf("Retrieve returned %d records, want 2", len(records))
	}
	if records[0].ID != "r:a" || records[1].ID != "r:b" {
		t.Errorf("Retrieve = [%s, %s], want [r:a, r:b] (tie broken by id)", records[0].ID, records[1].ID)
	}
	if records[0].Text != "original text a" {
		t.Errorf("record text = %q, want the FULL original from the store", records[0].Text)
	}

	// One kind=retrieve decision per scored entry (all four, not just the
	// chosen two).
	lines := shadowLines(t, shadow)
	if len(lines) != 4 {
		t.Fatalf("shadow log has %d lines, want 4 (one decision per scored entry)", len(lines))
	}
	wantActions := map[string]string{
		"r:a": RetrieveActionInjected,
		"r:b": RetrieveActionInjected,
		"r:c": RetrieveActionSkipped,
		"r:d": RetrieveActionSkipped,
	}
	for _, line := range lines {
		var e ShadowEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("shadow line invalid: %v", err)
		}
		if e.Type != EntryTypeDecision {
			t.Errorf("type = %q, want %q", e.Type, EntryTypeDecision)
		}
		if e.Kind != DecisionKindRetrieve {
			t.Errorf("kind = %q, want %q", e.Kind, DecisionKindRetrieve)
		}
		wantAction := wantActions[e.SegmentID]
		if wantAction == "" {
			t.Errorf("unexpected decision for %q", e.SegmentID)
			continue
		}
		if e.Action != wantAction {
			t.Errorf("decision %s action = %q, want %q", e.SegmentID, e.Action, wantAction)
		}
		if e.Decision != wantAction {
			t.Errorf("decision %s decision field = %q, want %q (mirrors the action)", e.SegmentID, e.Decision, wantAction)
		}
		if e.Threshold != 0.5 {
			t.Errorf("decision %s threshold = %v, want 0.5", e.SegmentID, e.Threshold)
		}
		if e.Score != scoreOf("fix the login bug", e.SegmentID, map[string]float64{
			"r:a": 0.9, "r:b": 0.9, "r:c": 0.6, "r:d": 0.4,
		}) {
			t.Errorf("decision %s score = %v, want the scripted score", e.SegmentID, e.Score)
		}
		if e.Tokens <= 0 {
			t.Errorf("decision %s tokens = %d, want the record's token count", e.SegmentID, e.Tokens)
		}
		if e.TaskHash == "" {
			t.Errorf("decision %s has an empty task_hash", e.SegmentID)
		}
	}
}

// scoreOf looks up a scripted score by id so the table above reads plainly.
func scoreOf(_ string, id string, scores map[string]float64) float64 {
	return scores[id]
}

// TestRetrieve_TaskFramingReachesTheWire: the scoring request's task slot
// carries the ported RETRIEVE_QUESTION wording and the caller's task digest,
// and the items are the digest summaries.
func TestRetrieve_TaskFramingReachesTheWire(t *testing.T) {
	var captured capturedRequest
	var capturedOnce bool
	p, _ := retrieveTestPipeline(t, func(_ int, req capturedRequest) (int, string) {
		if !capturedOnce {
			captured, capturedOnce = req, true
		}
		return http.StatusOK, answersBody(map[string]float64{"r:a": 0.9})
	})

	store := NewStore()
	store.PutRecord(Record{ID: "r:a", Text: "text a", Summary: "summary a", Tokens: 5})

	if _, err := p.Retrieve(context.Background(), "the digest of the task", store, 0, 0, 0); err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if !capturedOnce {
		t.Fatal("the decisions server never received a request")
	}
	task := captured.Req.State.Task
	for _, want := range []string{
		"Is this stored item relevant to the task described in `task` right now?",
		"Answer false if it belongs to unrelated work",
		"The item is relevant to the current step.",
		"The item is not relevant right now.",
		"the digest of the task",
	} {
		if !strings.Contains(task, want) {
			t.Errorf("scoring task %q does not contain %q", task, want)
		}
	}
	var scored *stateItem
	for i := range captured.Req.State.Items {
		if captured.Req.State.Items[i].Ref == "r:a" {
			scored = &captured.Req.State.Items[i]
		}
	}
	if scored == nil {
		t.Fatal("the request carried no state.items entry for r:a")
	}
	if scored.Text != "summary a" {
		t.Errorf("scored item text = %q, want the digest SUMMARY, not the original", scored.Text)
	}
}

// TestRetrieve_EmptyStoreMakesNoRequest: nothing to score, nothing logged,
// no backend round trip.
func TestRetrieve_EmptyStoreMakesNoRequest(t *testing.T) {
	var requests int
	p, shadow := retrieveTestPipeline(t, func(_ int, req capturedRequest) (int, string) {
		requests++
		return echoHandler(1, req)
	})

	records, err := p.Retrieve(context.Background(), "any task", NewStore(), 0, 0, 0)
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if records != nil {
		t.Errorf("Retrieve = %+v, want nil for an empty store", records)
	}
	if requests != 0 {
		t.Errorf("got %d decisions requests, want 0 for an empty store", requests)
	}
	if lines := shadowLines(t, shadow); len(lines) != 0 {
		t.Errorf("shadow log has %d lines, want 0", len(lines))
	}
}

// TestRetrieve_DefaultsApply: k=0, budget=0, and threshold=0 fall back to
// the reference defaults (5, 24k, 0.5).
func TestRetrieve_DefaultsApply(t *testing.T) {
	// Every ref scores a clean 1.0 (a fixed map would leave the unlisted
	// refs unanswered — an error, and a failed retrieval injects nothing).
	p, _ := retrieveTestPipeline(t, func(_ int, req capturedRequest) (int, string) {
		scores := make(map[string]float64, len(req.Req.Questions))
		for ref := range req.Req.Questions {
			scores[ref] = 1.0
		}
		return http.StatusOK, answersBody(scores)
	})

	store := NewStore()
	for i := 1; i <= 7; i++ {
		id := "r:0" + string(rune('0'+i))
		store.PutRecord(Record{ID: id, Text: "text", Summary: "s", Tokens: 10})
	}

	// All seven tie at 1.0, ties break by ascending id, and the default
	// k=5 caps the selection.
	records, err := p.Retrieve(context.Background(), "task", store, 0, 0, 0)
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(records) != DefaultRetrieveK {
		t.Fatalf("Retrieve returned %d records, want the default k=%d", len(records), DefaultRetrieveK)
	}
	for i, want := range []string{"r:01", "r:02", "r:03", "r:04", "r:05"} {
		if records[i].ID != want {
			t.Errorf("records[%d].ID = %s, want %s (ties broken by ascending id)", i, records[i].ID, want)
		}
	}
}

// TestRetrieve_NilInputs: a nil store is an empty digest; a nil pipeline is
// an error like every other pipeline entry point.
func TestRetrieve_NilInputs(t *testing.T) {
	p, _ := retrieveTestPipeline(t, echoHandler)
	records, err := p.Retrieve(context.Background(), "task", nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("Retrieve(nil store) error = %v, want (nil, nil)", err)
	}
	if records != nil {
		t.Errorf("Retrieve(nil store) = %+v, want nil", records)
	}

	var noPipeline *Pipeline
	if _, err := noPipeline.Retrieve(context.Background(), "task", NewStore(), 0, 0, 0); err == nil {
		t.Error("Retrieve on a nil pipeline error = nil, want an error")
	}
}

// TestRetrieve_AuthDisabledIsSilentlyOff: a pipeline disabled by an earlier
// auth rejection retrieves nothing, touches no backend, and logs nothing.
func TestRetrieve_AuthDisabledIsSilentlyOff(t *testing.T) {
	var requests int
	p, shadow := retrieveTestPipeline(t, func(_ int, req capturedRequest) (int, string) {
		requests++
		return echoHandler(1, req)
	})
	p.warnTo = &strings.Builder{} // DisableAuth's one-time warning must not hit stderr in tests
	p.DisableAuth("decisions API error (401): bad key")

	records, err := p.Retrieve(context.Background(), "task", retrieveTestStore(), 0, 0, 0)
	if err != nil {
		t.Fatalf("Retrieve() error = %v, want (nil, nil) while auth-disabled", err)
	}
	if records != nil {
		t.Errorf("Retrieve = %+v, want nil while auth-disabled", records)
	}
	if requests != 0 {
		t.Errorf("got %d requests, want 0 while auth-disabled", requests)
	}
	if lines := shadowLines(t, shadow); len(lines) != 0 {
		t.Errorf("shadow log has %d lines, want 0 while auth-disabled", len(lines))
	}
}

// TestRetrieve_LiveAuthErrorDisablesSession: an auth rejection during a
// retrieval disables the pipeline's scoring for the session (the shared
// one-warning policy), returns no records, and logs no decisions.
func TestRetrieve_LiveAuthErrorDisablesSession(t *testing.T) {
	p, shadow := retrieveTestPipeline(t, func(_ int, _ capturedRequest) (int, string) {
		return http.StatusUnauthorized, `{"error": {"message": "bad key"}}`
	})
	p.warnTo = &strings.Builder{} // the one-time auth warning must not hit stderr in tests

	records, err := p.Retrieve(context.Background(), "task", retrieveTestStore(), 0, 0, 0)
	if err == nil {
		t.Fatal("Retrieve() error = nil, want the auth-class failure surfaced")
	}
	if records != nil {
		t.Errorf("Retrieve = %+v, want nil after an auth rejection", records)
	}
	if lines := shadowLines(t, shadow); len(lines) != 0 {
		t.Errorf("shadow log has %d lines, want 0 (nothing was ranked)", len(lines))
	}
	// The rejection disabled scoring for the session: a second retrieval is
	// silently off.
	if _, err := p.Retrieve(context.Background(), "task", retrieveTestStore(), 0, 0, 0); err != nil {
		t.Errorf("second Retrieve() error = %v, want the silently-off (nil, nil) path", err)
	}
}

// TestRetrieve_ScoringErrorAbortsTheReadSide: a non-auth scoring failure
// returns no records and logs no decisions — the fail-open direction for
// retrieval is "inject nothing", and an aborted retrieval made no decision
// worth logging.
func TestRetrieve_ScoringErrorAbortsTheReadSide(t *testing.T) {
	p, shadow := retrieveTestPipeline(t, func(_ int, _ capturedRequest) (int, string) {
		return http.StatusServiceUnavailable, `{"error": {"message": "down"}}`
	})
	// Shrink the client's retry curve for a fast test.
	shrinkPipelineRetryCurve(p)

	records, err := p.Retrieve(context.Background(), "task", retrieveTestStore(), 0, 0, 0)
	if err == nil {
		t.Fatal("Retrieve() error = nil, want the outage surfaced")
	}
	if records != nil {
		t.Errorf("Retrieve = %+v, want nil on a scoring failure", records)
	}
	if lines := shadowLines(t, shadow); len(lines) != 0 {
		t.Errorf("shadow log has %d lines, want 0 (the retrieval was aborted, not decided)", len(lines))
	}
}

// TestRetrieveDecisionsAreNotElideDecisions: kind=retrieve decisions must
// stay out of the elide math — the replay table, the Stats counters, and
// the false-negative ledger model elision, not relevance.
func TestRetrieveDecisionsAreNotElideDecisions(t *testing.T) {
	p, shadow := retrieveTestPipeline(t, fixedScoresHandler(map[string]float64{
		"r:a": 0.9, "r:b": 0.1, "r:c": 0.8, "r:d": 0.2,
	}))
	if _, err := p.Retrieve(context.Background(), "task", retrieveTestStore(), 5, 1000, 0.5); err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}

	report, err := shadow.Replay(0.1)
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if report.Entries != 0 || report.ElidedEntries != 0 {
		t.Errorf("Replay() = %+v, want zero entries (retrieve decisions are not elide decisions)", report)
	}

	st, err := shadow.Stats()
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if st.Decisions != 0 || st.ElidedDecisions != 0 {
		t.Errorf("Stats() = %+v, want zero decisions (retrieve decisions excluded)", st)
	}

	rows, err := shadow.ReplayTable([]float64{0.1})
	if err != nil {
		t.Fatalf("ReplayTable() error = %v", err)
	}
	if rows[0].Kept != 0 || rows[0].Relocated != 0 {
		t.Errorf("ReplayTable() = %+v, want an empty row for retrieve-only logs", rows[0])
	}

	// The false-negative ledger stays clean too: a mixed log with one real
	// elide decision (later expanded) and one below-threshold retrieve
	// decision reports a rate over the ELIDES only — the skipped retrieval
	// must not dilute it.
	shadow2, err := NewShadowLogAt(filepath.Join(t.TempDir(), "mixed.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	if err := shadow2.Append(ShadowEntry{
		TS: now, SegmentID: "seg-1", Score: 0.1, Decision: DecisionElide,
		Threshold: 0.35, Type: EntryTypeDecision, Kind: DecisionKindAdmit,
	}); err != nil {
		t.Fatal(err)
	}
	// A retrieve decision whose score sits below its own threshold: without
	// the guard it would join the elided set (and never be expanded,
	// halving the rate).
	if err := shadow2.Append(ShadowEntry{
		TS: now, SegmentID: "r:zz", Score: 0.2, Decision: RetrieveActionSkipped,
		Threshold: 0.5, Type: EntryTypeDecision, Kind: DecisionKindRetrieve,
	}); err != nil {
		t.Fatal(err)
	}
	if err := shadow2.Append(ShadowEntry{TS: now, Type: EntryTypeExpand, ItemID: "seg-1"}); err != nil {
		t.Fatal(err)
	}
	fnr, err := shadow2.FalseNegativeRate()
	if err != nil {
		t.Fatalf("FalseNegativeRate() error = %v", err)
	}
	if fnr != 1.0 {
		t.Errorf("FalseNegativeRate() = %v, want 1.0 (the one elided segment was expanded; the skipped retrieval must not dilute)", fnr)
	}
}

// TestRetrievedBlockShape pins the work-area block rendering.
func TestRetrievedBlockShape(t *testing.T) {
	records := []Record{
		{ID: "r:a", Text: "first original"},
		{ID: "r:b", Text: "second original"},
	}
	got := RetrievedBlock(records)
	want := "Retrieved context (scored relevant to the current task):\nfirst original\n---\nsecond original"
	if got != want {
		t.Errorf("RetrievedBlock =\n%q\nwant\n%q", got, want)
	}
	if RetrievedBlock(nil) != "" {
		t.Error("RetrievedBlock(nil) = non-empty, want empty")
	}
	if got := RetrievedBlock([]Record{{ID: "r:x", Text: ""}}); got != "" {
		t.Errorf("RetrievedBlock of text-less records = %q, want empty", got)
	}
}
