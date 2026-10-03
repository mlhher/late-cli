package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"late/internal/client"
	"late/internal/compaction"
	"late/internal/session"
	"late/internal/tool"
)

// largeDumpTool is a fake tool whose result is configurable: big enough to
// cross MinCompactToolResultChars, multi-paragraph so it segments, with one
// SECRET marker per paragraph placed past the first 60 characters (a
// pointer's preview can therefore never leak it).
type largeDumpTool struct {
	name   string
	output string
}

func (t largeDumpTool) Name() string {
	if t.name == "" {
		return "large_dump"
	}
	return t.name
}
func (t largeDumpTool) Description() string { return "Emits a large multi-paragraph output." }
func (t largeDumpTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t largeDumpTool) RequiresConfirmation(json.RawMessage) bool { return false }
func (t largeDumpTool) CallString(json.RawMessage) string         { return "Dumping..." }
func (t largeDumpTool) Execute(context.Context, json.RawMessage) (string, error) {
	return t.output, nil
}

// largeDumpOutput builds `paras` paragraphs of ~600 chars each (over the
// 80-char tiny-paragraph floor, under the 1200-byte segment cap, so each
// paragraph is exactly one segment).
func largeDumpOutput(paras int) string {
	var b strings.Builder
	for i := 1; i <= paras; i++ {
		b.WriteString(strings.Repeat(fmt.Sprintf("p%d ", i), 200))
		b.WriteString(fmt.Sprintf("PARA-%d-SECRET", i))
		b.WriteString("\n\n")
	}
	return strings.TrimSuffix(b.String(), "\n\n")
}

// stubDecisionsServer is a minimal System One decisions endpoint: every
// request is answered with a score per asked ref, decided by scoreFor. The
// returned counter counts served requests.
func stubDecisionsServer(t *testing.T, scoreFor func(ref string) float64) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			t.Errorf("read decisions request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var req struct {
			Questions map[string]json.RawMessage `json:"questions"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode decisions request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		answers := make(map[string]any, len(req.Questions))
		for ref := range req.Questions {
			answers[ref] = map[string]any{"type": "noul", "noul": scoreFor(ref)}
		}
		resp, err := json.Marshal(map[string]any{"answers": answers})
		if err != nil {
			t.Errorf("marshal decisions response: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// evenBelowOddAbove scores even-numbered segments below the 0.35 threshold
// and odd-numbered ones above it, so relocation has both kinds to work with.
func evenBelowOddAbove(ref string) float64 {
	var n int
	if _, err := fmt.Sscanf(ref, "seg-%d", &n); err == nil && n%2 == 0 {
		return 0.05
	}
	return 0.9
}

// newCompactionSession builds a session with the dump and expand tools
// registered, mirroring main()'s enabled-mode wiring.
func newCompactionSession(t *testing.T, store *compaction.Store) *session.Session {
	t.Helper()
	c := client.NewClient(client.Config{BaseURL: "http://localhost:0"})
	sess := session.New(c, filepath.Join(t.TempDir(), "history.json"), nil, "", true)
	sess.Registry.Register(largeDumpTool{output: largeDumpOutput(8)})
	sess.Registry.Register(largeDumpTool{name: "small_dump", output: "tiny result"})
	sess.Registry.Register(tool.ExpandTool{Store: store})
	return sess
}

// callTool runs one tool call through ExecuteToolCalls and returns the tool
// result message that entered history.
func callTool(t *testing.T, sess *session.Session, id, name, args string) string {
	t.Helper()
	err := ExecuteToolCalls(context.Background(), sess, []client.ToolCall{
		{ID: id, Function: client.FunctionCall{Name: name, Arguments: args}},
	}, nil)
	if err != nil {
		t.Fatalf("ExecuteToolCalls(%s) error = %v", name, err)
	}
	last := sess.History[len(sess.History)-1]
	if last.Role != "tool" || last.ToolCallID != id {
		t.Fatalf("history tail = role %q id %q, want the tool result for %s", last.Role, last.ToolCallID, id)
	}
	return last.Content.String()
}

func hits(t *testing.T, counter *int32) int {
	t.Helper()
	return int(atomic.LoadInt32(counter))
}

// TestExecuteToolCalls_CompactionEnabledRelocatesAndExpandRetrieves is the
// end-to-end enabled-mode check: an oversized tool result enters history
// compacted (pointers + kept segments + trailer), the elided original never
// leaks into history, and the expand tool retrieves it from the store.
func TestExecuteToolCalls_CompactionEnabledRelocatesAndExpandRetrieves(t *testing.T) {
	srv, counter := stubDecisionsServer(t, evenBelowOddAbove)
	store := compaction.NewStore()
	pipeline := compaction.NewPipeline(
		compaction.ResolvedBackend{Backend: compaction.Backend{Name: "test", URL: srv.URL, Model: "jev-latest"}, APIKey: "k"},
		"k", nil, compaction.PipelineOptions{})
	pipeline.EnableRelocation(store, 0.35)
	SetToolResultCompactor(pipeline)
	t.Cleanup(func() { SetToolResultCompactor(nil) })

	sess := newCompactionSession(t, store)
	output := largeDumpOutput(8)
	if len(output) <= MinCompactToolResultChars {
		t.Fatalf("test output is only %d chars, must exceed the %d-char threshold", len(output), MinCompactToolResultChars)
	}

	dumped := callTool(t, sess, "call_1", "large_dump", "{}")
	// Four non-adjacent paragraphs score below the threshold: four runs, four
	// content-addressed pointers (r:<8hex>) with line ranges.
	if got := strings.Count(dumped, "[[elided id=r:"); got != 4 {
		t.Errorf("history tool result carries %d content-id pointers, want 4:\n%s", got, dumped)
	}
	if !strings.Contains(dumped, " lines=") {
		t.Errorf("history tool result pointers missing the line ranges:\n%s", dumped)
	}
	if !strings.Contains(dumped, "PARA-1-SECRET") || !strings.Contains(dumped, "PARA-3-SECRET") {
		t.Errorf("history tool result lost a high-scoring segment:\n%s", dumped)
	}
	if strings.Contains(dumped, "PARA-2-SECRET") || strings.Contains(dumped, "PARA-4-SECRET") {
		t.Errorf("history tool result leaked an elided original:\n%s", dumped)
	}
	if !strings.Contains(dumped, "4 segments elided — use the expand tool with the elided ids to retrieve originals") {
		t.Errorf("history tool result missing the expand trailer:\n%s", dumped)
	}

	// The elided original sits in the store under its content id — derived
	// exactly as the pipeline does: ContentID(runText, salt=toolName, "r").
	segs := compaction.SegmentSegments(output, 0)
	seg2ID := compaction.ContentID(segs[1].Text, "large_dump", "r")
	if _, ok := store.Get(seg2ID); !ok {
		t.Errorf("store must hold seg-2 under its content id %s", seg2ID)
	}

	// The expand tool retrieves the elided original from the shared store —
	// and its own result must not be re-compacted (no new scoring requests).
	before := hits(t, counter)
	expanded := callTool(t, sess, "call_2", "expand", `{"id":"`+seg2ID+`"}`)
	if hits(t, counter) != before {
		t.Errorf("expand result was re-compacted: %d new scoring requests", hits(t, counter)-before)
	}
	// seg-2 is paragraph 2 plus its trailing blank-line separator.
	want := segs[1].Text
	if expanded != want {
		t.Errorf("expand(%s) = %q, want the stored original %q", seg2ID, truncateForTest(expanded), truncateForTest(want))
	}

	// The byte-for-byte inverse: reconstructing the history result (trailer
	// stripped) restores the tool's raw output. evenBelowOddAbove elides the
	// even segments: seg-2, seg-4, seg-6, seg-8 — none adjacent, so four
	// single-segment runs in segment order.
	ids := []string{
		compaction.ContentID(segs[1].Text, "large_dump", "r"),
		compaction.ContentID(segs[3].Text, "large_dump", "r"),
		compaction.ContentID(segs[5].Text, "large_dump", "r"),
		compaction.ContentID(segs[7].Text, "large_dump", "r"),
	}
	trailer := "\n\n4 segments elided — use the expand tool with the elided ids to retrieve originals (" + strings.Join(ids, ", ") + ")."
	compacted := strings.TrimSuffix(dumped, trailer)
	if compacted == dumped {
		t.Fatalf("history result missing the expand trailer with the elided ids:\n%s", dumped)
	}
	if restored := compaction.Reconstruct(compacted, store); restored != output {
		t.Errorf("Reconstruct(history result) is not byte-for-byte:\n got %q\nwant %q", truncateForTest(restored), truncateForTest(output))
	}

	// Unknown ids produce the documented error result — legacy id shapes too.
	unknown := callTool(t, sess, "call_3", "expand", `{"id":"elide-999"}`)
	if !strings.Contains(unknown, "unknown elided id") {
		t.Errorf("expand(unknown id) = %q, want the unknown-id error result", unknown)
	}
}

// TestExecuteToolCalls_CompactionFileBackedStoreSurvivesReopen is the Step
// 12 end-to-end: enabled mode with the store backed by a real JSONL file.
// The elided original is retrievable in-session, carries its full record
// metadata (tool origin, kind, segment ids, summary, tokens), and — the gap
// this step closes — is still retrievable from a freshly reopened store,
// so [[elided …]] pointers in a persisted session no longer dangle after a
// restart.
func TestExecuteToolCalls_CompactionFileBackedStoreSurvivesReopen(t *testing.T) {
	srv, _ := stubDecisionsServer(t, evenBelowOddAbove)
	storePath := filepath.Join(t.TempDir(), "compaction-store.jsonl")
	store, err := compaction.OpenStore(storePath)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	pipeline := compaction.NewPipeline(
		compaction.ResolvedBackend{Backend: compaction.Backend{Name: "test", URL: srv.URL, Model: "jev-latest"}, APIKey: "k"},
		"k", nil, compaction.PipelineOptions{})
	pipeline.EnableRelocation(store, 0.35)
	SetToolResultCompactor(pipeline)
	t.Cleanup(func() { SetToolResultCompactor(nil) })

	sess := newCompactionSession(t, store)
	output := largeDumpOutput(8)
	callTool(t, sess, "call_1", "large_dump", "{}")

	segs := compaction.SegmentSegments(output, 0)
	seg2ID := compaction.ContentID(segs[1].Text, "large_dump", "r")

	// The record carries the pipeline's metadata, not just the text.
	rec, ok := store.GetRecord(seg2ID)
	if !ok {
		t.Fatalf("store must hold a record for %s", seg2ID)
	}
	if rec.Kind != compaction.RecordKindElidedSegment {
		t.Errorf("Kind = %q, want %q", rec.Kind, compaction.RecordKindElidedSegment)
	}
	if rec.Origin != (compaction.Origin{Source: "tool:large_dump", Ref: "", Turn: 0}) {
		t.Errorf("Origin = %+v, want {tool:large_dump  0}", rec.Origin)
	}
	if len(rec.SegmentIDs) != 1 || rec.SegmentIDs[0] != "seg-2" {
		t.Errorf("SegmentIDs = %v, want [seg-2]", rec.SegmentIDs)
	}
	if rec.Tokens != segs[1].Tokens {
		t.Errorf("Tokens = %d, want %d", rec.Tokens, segs[1].Tokens)
	}
	if want := compaction.Summarise(segs[1].Text, compaction.SummaryMaxChars); rec.Summary != want {
		t.Errorf("Summary = %q, want the pointer summary %q", rec.Summary, want)
	}

	// Touch the expand counter the way the expand path will (Step 13).
	if !store.Touch(seg2ID, true, false) {
		t.Fatal("Touch on a stored record must report found")
	}

	// REOPEN — the restart/resume case every in-memory build failed: the
	// persisted record must resolve for both the raw read side and the
	// expand tool wired against the reopened store.
	reopened, err := compaction.OpenStore(storePath)
	if err != nil {
		t.Fatalf("reopen OpenStore() error = %v", err)
	}
	if text, ok := reopened.Get(seg2ID); !ok || text != segs[1].Text {
		t.Errorf("reopened Get(%s) = (%q, %v), want the stored original", seg2ID, truncateForTest(text), ok)
	}
	rec2, ok := reopened.GetRecord(seg2ID)
	if !ok {
		t.Fatalf("reopened store must hold the record for %s", seg2ID)
	}
	if rec2.ExpandCount != 1 {
		t.Errorf("reopened ExpandCount = %d, want 1 (the touch survives reload)", rec2.ExpandCount)
	}
	if rec2.Origin != rec.Origin || rec2.Kind != rec.Kind || rec2.Tokens != rec.Tokens || rec2.Summary != rec.Summary ||
		len(rec2.SegmentIDs) != 1 || rec2.SegmentIDs[0] != "seg-2" {
		t.Errorf("reopened record = %+v, want the original metadata %+v", rec2, rec)
	}

	sess2 := newCompactionSession(t, reopened)
	expanded := callTool(t, sess2, "call_3", "expand", `{"id":"`+seg2ID+`"}`)
	if expanded != segs[1].Text {
		t.Errorf("expand after reopen = %q, want the stored original %q", truncateForTest(expanded), truncateForTest(segs[1].Text))
	}
}

// TestExecuteToolCalls_CompactionShadowMode: shadow mode scores and logs but
// the history result is byte-identical to the tool's output.
func TestExecuteToolCalls_CompactionShadowMode(t *testing.T) {
	srv, counter := stubDecisionsServer(t, evenBelowOddAbove)
	shadowPath := filepath.Join(t.TempDir(), "shadow.jsonl")
	shadow, err := compaction.NewShadowLogAt(shadowPath)
	if err != nil {
		t.Fatal(err)
	}
	pipeline := compaction.NewPipeline(
		compaction.ResolvedBackend{Backend: compaction.Backend{Name: "test", URL: srv.URL, Model: "jev-latest"}, APIKey: "k"},
		"k", shadow, compaction.PipelineOptions{})
	SetToolResultCompactor(pipeline)
	t.Cleanup(func() { SetToolResultCompactor(nil) })

	sess := newCompactionSession(t, compaction.NewStore())
	output := largeDumpOutput(8)
	got := callTool(t, sess, "call_1", "large_dump", "{}")
	if got != output {
		t.Error("shadow mode must leave the history tool result unchanged")
	}

	// The shadow log recorded one decision per segment.
	data, err := os.ReadFile(shadowPath)
	if err != nil {
		t.Fatalf("shadow log missing: %v", err)
	}
	lines := strings.Count(strings.TrimSpace(string(data)), "\n") + 1
	if lines != 8 {
		t.Errorf("shadow log has %d lines, want one per segment (8)", lines)
	}
	if hits(t, counter) == 0 {
		t.Error("shadow mode must still score through the decisions backend")
	}
}

// TestExecuteToolCalls_CompactionOff: no compactor installed → the result
// passes through untouched and the decisions backend is never contacted.
func TestExecuteToolCalls_CompactionOff(t *testing.T) {
	_, counter := stubDecisionsServer(t, evenBelowOddAbove) // server live, but must never be hit
	SetToolResultCompactor(nil)
	t.Cleanup(func() { SetToolResultCompactor(nil) })

	sess := newCompactionSession(t, compaction.NewStore())
	output := largeDumpOutput(8)
	got := callTool(t, sess, "call_1", "large_dump", "{}")
	if got != output {
		t.Error("compaction off must leave the history tool result unchanged")
	}
	if n := hits(t, counter); n != 0 {
		t.Errorf("compaction off must not score, got %d decisions requests", n)
	}
}

// TestExecuteToolCalls_CompactionFailOpenAndSmallResults: a broken backend
// must return the original result (never break a tool call over
// compaction), and results under the size threshold must not be scored at
// all. The 400 status is non-retryable, so the broken-backend case is fast.
func TestExecuteToolCalls_CompactionFailOpenAndSmallResults(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error": {"message": "down"}}`)
	}))
	t.Cleanup(srv.Close)

	store := compaction.NewStore()
	pipeline := compaction.NewPipeline(
		compaction.ResolvedBackend{Backend: compaction.Backend{Name: "test", URL: srv.URL, Model: "jev-latest"}, APIKey: "k"},
		"k", nil, compaction.PipelineOptions{})
	pipeline.EnableRelocation(store, 0.35)
	SetToolResultCompactor(pipeline)
	t.Cleanup(func() { SetToolResultCompactor(nil) })

	sess := newCompactionSession(t, store)
	output := largeDumpOutput(8)
	got := callTool(t, sess, "call_1", "large_dump", "{}")
	if got != output {
		t.Errorf("fail-open must keep the original result, got:\n%s", truncateForTest(got))
	}
	if store.Len() != 0 {
		t.Error("fail-open must not store elided originals")
	}

	// A small result is never offered to the compactor.
	callTool(t, sess, "call_2", "small_dump", "{}")
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("scoring requests = %d, want exactly the single (failed) oversized-result attempt", n)
	}
}

// TestMaybeCompactToolResult_Guards pins the executor-side guards without a
// backend: nil compactor, the size threshold, and the expand-tool exemption.
func TestMaybeCompactToolResult_Guards(t *testing.T) {
	SetToolResultCompactor(nil)
	t.Cleanup(func() { SetToolResultCompactor(nil) })

	big := strings.Repeat("x", MinCompactToolResultChars+1)
	if got := maybeCompactToolResult(context.Background(), "large_dump", big); got != big {
		t.Error("nil compactor must pass results through untouched")
	}

	calls := 0
	SetToolResultCompactor(compactorFunc(func(ctx context.Context, toolName, result string) string {
		calls++
		return "compacted"
	}))

	if got := maybeCompactToolResult(context.Background(), "large_dump", strings.Repeat("x", MinCompactToolResultChars)); got != strings.Repeat("x", MinCompactToolResultChars) {
		t.Error("results at the size threshold must not be compacted")
	}
	if got := maybeCompactToolResult(context.Background(), tool.ExpandToolName, big); got != big {
		t.Error("expand results must be exempt from re-compaction")
	}
	if got := maybeCompactToolResult(context.Background(), "large_dump", big); got != "compacted" {
		t.Errorf("oversized results must reach the compactor, got %q", truncateForTest(got))
	}
	if calls != 1 {
		t.Errorf("compactor invoked %d times, want 1", calls)
	}
}

// compactorFunc adapts a function to ToolResultCompactor (tests only).
type compactorFunc func(ctx context.Context, toolName, result string) string

func (f compactorFunc) CompactToolResult(ctx context.Context, toolName, result string) string {
	return f(ctx, toolName, result)
}

func truncateForTest(s string) string {
	if len(s) <= 120 {
		return s
	}
	return s[:117] + "..."
}
