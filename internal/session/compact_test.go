package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"late/internal/client"
	"late/internal/common"
	"late/internal/compaction"
	"late/internal/tool"
)

// --- fixture helpers ---------------------------------------------------------
//
// All helpers are cmp-prefixed to stay clear of the history_sanitize_test.go
// fixture helpers in this package.

// cmpStamped marks fixture message n. On the source branch it stamped the
// message with a fixed RFC3339 timestamp (timestamps must survive compaction
// untouched); the receive-time Timestamp field itself belongs to the excluded
// timestamps feature, so here it is an identity marker kept so the fixture
// call sites keep their per-message indices.
func cmpStamped(msg client.ChatMessage, _ int) client.ChatMessage {
	return msg
}

func cmpSystem(text string) client.ChatMessage {
	return client.ChatMessage{Role: "system", Content: client.TextContent(text)}
}

func cmpUser(text string) client.ChatMessage {
	return client.ChatMessage{Role: "user", Content: client.TextContent(text)}
}

func cmpAssistant(text string) client.ChatMessage {
	return client.ChatMessage{Role: "assistant", Content: client.TextContent(text)}
}

func cmpTool(text string) client.ChatMessage {
	return client.ChatMessage{Role: "tool", Content: client.TextContent(text)}
}

func cmpToolResult(callID, text string) client.ChatMessage {
	return client.ChatMessage{Role: "tool", ToolCallID: callID, Content: client.TextContent(text)}
}

func cmpAssistantWithCalls(text string, calls []client.ToolCall) client.ChatMessage {
	return client.ChatMessage{Role: "assistant", Content: client.TextContent(text), ToolCalls: calls}
}

func cmpToolCall(id string) client.ToolCall {
	return client.ToolCall{Index: 0, ID: id, Type: "function", Function: client.FunctionCall{Name: "dump", Arguments: `{"path":"."}`}}
}

// cmpLongText builds n distinct ~550-byte paragraphs separated by blank
// lines, so compaction.SegmentSegments yields exactly n single-paragraph
// segments (none tiny, none over the 1200-byte cap) and n >= 3 exceeds
// minCompactChars.
func cmpLongText(tag string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "%s segment %02d: ", tag, i)
		b.WriteString(strings.Repeat(fmt.Sprintf("%s%02dtoken ", tag, i), 42))
	}
	return b.String()
}

// defaultFixture builds a 12-message history whose frozen prefix is
// max(1, 12/4) = 3 messages (the system prompt plus the two earliest
// exchanges) and whose eligible region mixes four compactable candidates
// (indices 4, 5, 7, 8 — assistant-with-tool-calls and tool results; a
// pure-prose assistant would NOT be a candidate), a long user message
// (index 9 — never compacted), and short filler.
func defaultFixture() []client.ChatMessage {
	return []client.ChatMessage{
		cmpStamped(cmpSystem("You are a helpful coding assistant."), 0),                                         // 0 frozen
		cmpStamped(cmpUser("Explore the repository and summarize it."), 1),                                      // 1 frozen
		cmpStamped(cmpAssistant("It is a Go CLI for LLM chat sessions."), 2),                                    // 2 frozen
		cmpStamped(cmpUser("Now inspect internal/session."), 3),                                                 // 3
		cmpStamped(cmpAssistantWithCalls(cmpLongText("alpha", 3), []client.ToolCall{cmpToolCall("call_4")}), 4), // 4 candidate
		cmpStamped(cmpTool(cmpLongText("beta", 3)), 5),                                                          // 5 candidate (tool result)
		cmpStamped(cmpUser("What about tool calls?"), 6),                                                        // 6
		cmpStamped(cmpAssistantWithCalls(cmpLongText("gamma", 3), []client.ToolCall{cmpToolCall("call_7")}), 7), // 7 candidate
		cmpStamped(cmpToolResult("call_7", cmpLongText("delta", 3)), 8),                                         // 8 candidate (tool result, call id)
		cmpStamped(cmpUser(cmpLongText("user", 3)), 9),                                                          // 9 long USER — never compacted
		cmpStamped(cmpAssistant("A short closing answer."), 10),                                                 // 10
		cmpStamped(cmpTool(`{"ok":true}`), 11),                                                                  // 11
	}
}

// fixtureSegments precomputes, for every compactable candidate in fixture,
// the segments CompactContext will see (segmentation is deterministic),
// keyed by history index.
func fixtureSegments(t *testing.T, fixture []client.ChatMessage) map[int][]compaction.Segment {
	t.Helper()
	out := make(map[int][]compaction.Segment)
	for i := range fixture {
		msg := &fixture[i]
		if !compactableContent(msg) {
			continue
		}
		segs := compaction.SegmentSegments(msg.Content.Text, minCompactChars)
		if len(segs) > 0 {
			out[i] = segs
		}
	}
	return out
}

// stubErrShape selects what a failing stubScorer call returns alongside its
// error — the three ScoreBatch shapes the walk must tell apart.
type stubErrShape int

const (
	// stubErrWholesale returns no usable scores with the error: a backend
	// that produced nothing scoreable (the walk-abort shape).
	stubErrWholesale stubErrShape = iota
	// stubErrFailOpen returns a complete score map (this stub's normal
	// answers for every requested id) alongside the error — the
	// compaction.DecisionClient.ScoreBatch fail-open contract.
	stubErrFailOpen
	// stubErrPartial returns a map missing some requested ids alongside the
	// error: only the first errPartialIds item ids (sorted) are answered.
	stubErrPartial
)

// stubScorer is the map-based HistoryScorer stub: it scores each item by
// exact segment-text lookup (fallback otherwise), can fail the Nth call in
// any of the three error shapes, and records every task it saw. No network.
type stubScorer struct {
	scores   map[string]float64
	fallback float64
	err      error
	// errOnCall is the 1-based ScoreBatch call that fails; 0 = never (or
	// every call fails when err is set).
	errOnCall int
	// errShape selects the failing call's return shape (default wholesale).
	errShape stubErrShape
	// errPartialIds caps the partial shape's answer map size; unused
	// otherwise.
	errPartialIds int
	calls         int
	tasks         []string
}

func (s *stubScorer) ScoreBatch(_ context.Context, task string, items map[string]compaction.Item) (map[string]float64, error) {
	s.calls++
	s.tasks = append(s.tasks, task)
	if s.err != nil && (s.errOnCall == 0 || s.calls == s.errOnCall) {
		switch s.errShape {
		case stubErrFailOpen:
			// Fail-open shape: a complete score map returned alongside the
			// error — every id usable.
			return s.answer(items), s.err
		case stubErrPartial:
			// Partial shape: keep only the first errPartialIds ids (sorted,
			// so the split is deterministic); the rest stay unanswered.
			out := s.answer(items)
			ids := make([]string, 0, len(out))
			for id := range out {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids[s.errPartialIds:] {
				delete(out, id)
			}
			return out, s.err
		default:
			// Wholesale shape: no usable scores.
			return nil, s.err
		}
	}
	return s.answer(items), nil
}

// answer builds the stub's normal score map for items: the exact-text score
// when present, the fallback otherwise.
func (s *stubScorer) answer(items map[string]compaction.Item) map[string]float64 {
	out := make(map[string]float64, len(items))
	for id, it := range items {
		if score, ok := s.scores[it.Text]; ok {
			out[id] = score
		} else {
			out[id] = s.fallback
		}
	}
	return out
}

// elideFirstScorer scores the FIRST segment of every precomputed candidate
// 0.1 (elide) and everything else 0.9 (keep), so each rewritten message
// loses exactly its opening segment.
func elideFirstScorer(segs map[int][]compaction.Segment) *stubScorer {
	scores := make(map[string]float64)
	for _, msgSegs := range segs {
		scores[msgSegs[0].Text] = 0.1
		for _, seg := range msgSegs[1:] {
			scores[seg.Text] = 0.9
		}
	}
	return &stubScorer{scores: scores, fallback: 0.9}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// newCompactSession wraps the fixture in an in-memory session (no history
// path: nothing is persisted) as a deep copy, so the caller's fixture stays
// pristine for comparisons.
func newCompactSession(history []client.ChatMessage) *Session {
	return New(nil, "", cloneHistory(history), "", false)
}

// assertZeroReport asserts a no-op report with the expected scan count.
func assertZeroReport(t *testing.T, report CompactionReport, wantScanned int) {
	t.Helper()
	if report.MessagesScanned != wantScanned {
		t.Errorf("MessagesScanned = %d, want %d", report.MessagesScanned, wantScanned)
	}
	if report.MessagesScored != 0 || report.MessagesCompacted != 0 || report.SegmentsElided != 0 || report.TokensSaved != 0 || report.StoreSize != 0 {
		t.Errorf("expected a no-op report, got %+v", report)
	}
	if report.TokensAfter != report.TokensBefore {
		t.Errorf("TokensAfter = %d, want TokensBefore %d", report.TokensAfter, report.TokensBefore)
	}
}

// --- tests -------------------------------------------------------------------

// (a) The frozen prefix is byte-identical after a mutating run — including
// the system prompt at index 0 — and the walk starts right after it.
func TestCompactContextFrozenPrefixUntouched(t *testing.T) {
	fixture := defaultFixture()
	s := newCompactSession(fixture)
	segs := fixtureSegments(t, fixture)

	report, err := s.CompactContext(context.Background(), elideFirstScorer(segs), NewCompactStore(), CompactionOptions{})
	if err != nil {
		t.Fatalf("CompactContext() error = %v", err)
	}

	// 12 messages at the default 25% → frozen = max(1, 3) = 3.
	if frozen := frozenPrefix(len(fixture), defaultFrozenPercent); frozen != 3 {
		t.Fatalf("frozenPrefix(12, 25) = %d, want 3", frozen)
	}
	if got, want := mustJSON(t, s.History[:3]), mustJSON(t, fixture[:3]); got != want {
		t.Fatalf("frozen prefix mutated:\n got %s\nwant %s", got, want)
	}
	if s.History[0].Content.Text != fixture[0].Content.Text {
		t.Error("the system prompt (message 0) must never be compacted")
	}
	// The walk starts at index 3: the first candidate (index 4) was rewritten.
	if !strings.Contains(s.History[4].Content.Text, "[[elided id=") {
		t.Error("expected the first eligible message (index 4) to be compacted")
	}
	if report.MessagesScanned != len(fixture)-3 {
		t.Errorf("MessagesScanned = %d, want %d", report.MessagesScanned, len(fixture)-3)
	}
}

// (b) User messages are never compacted — not even a long one whose segments
// would all score below the threshold.
func TestCompactContextNeverCompactsUserMessages(t *testing.T) {
	fixture := defaultFixture()
	s := newCompactSession(fixture)
	segs := fixtureSegments(t, fixture)
	scorer := elideFirstScorer(segs)

	report, err := s.CompactContext(context.Background(), scorer, NewCompactStore(), CompactionOptions{})
	if err != nil {
		t.Fatalf("CompactContext() error = %v", err)
	}

	if got, want := s.History[9].Content.Text, fixture[9].Content.Text; got != want {
		t.Fatalf("the long user message was compacted:\n got %q\nwant %q", truncateRunes(got, 120), truncateRunes(want, 120))
	}
	// It was never even scored: exactly the four assistant/tool candidates
	// reached the scorer.
	if scorer.calls != len(segs) {
		t.Errorf("scorer calls = %d, want %d (one per candidate)", scorer.calls, len(segs))
	}
	if report.MessagesCompacted != len(segs) {
		t.Errorf("MessagesCompacted = %d, want %d", report.MessagesCompacted, len(segs))
	}
}

// (c) A long tool-result message is rewritten to kept segments plus an
// [[elided …]] pointer, and the original round-trips through the store —
// including through the expand tool.
func TestCompactContextElidesToolResultWithStoreRoundTrip(t *testing.T) {
	fixture := defaultFixture()
	s := newCompactSession(fixture)
	segs := fixtureSegments(t, fixture)
	// The production store type (compaction.Store, as main() wires it): it
	// satisfies ElideStore, backs the expand tool, and works with
	// compaction.Reconstruct.
	store := compaction.NewStore()
	scorer := elideFirstScorer(segs)

	report, err := s.CompactContext(context.Background(), scorer, store, CompactionOptions{})
	if err != nil {
		t.Fatalf("CompactContext() error = %v", err)
	}

	// The elided opening segment of the tool result at index 5 is stored
	// under its content id (salt "" — history compaction is per-text), and
	// the pointer stands where the run stood.
	segs5 := segs[5]
	runText := segs5[0].Text
	id := compaction.ContentID(runText, "", "r")
	pointer := compaction.FormatPointer(compaction.Pointer{
		ID:      id,
		Lines:   &[2]int{segs5[0].LineStart, segs5[0].LineEnd},
		Tokens:  segs5[0].Tokens,
		Summary: compaction.Summarise(runText, compaction.SummaryMaxChars),
	})
	want := pointer + "\n" + segs5[1].Text + segs5[2].Text
	if got := s.History[5].Content.Text; got != want {
		t.Fatalf("compacted tool result mismatch:\n got %q\nwant %q", truncateRunes(got, 200), truncateRunes(want, 200))
	}
	if report.SegmentsElided != len(segs) {
		t.Errorf("SegmentsElided = %d, want %d", report.SegmentsElided, len(segs))
	}

	original, ok := store.Get(id)
	if !ok {
		t.Fatalf("store.Get(%s) miss: the elided original was not stored", id)
	}
	if original != segs5[0].Text {
		t.Fatalf("store round trip mismatch:\n got %q\nwant %q", truncateRunes(original, 120), truncateRunes(segs5[0].Text, 120))
	}

	// The expand tool reads the same store — content id or a whole pointer
	// line, both resolve.
	expanded, xerr := tool.ExpandTool{Store: store}.Execute(context.Background(), json.RawMessage(`{"id":"`+id+`"}`))
	if xerr != nil {
		t.Fatalf("expand(%s) error = %v", id, xerr)
	}
	if expanded != segs5[0].Text {
		t.Error("expand tool did not return the stored original")
	}
	expanded, xerr = tool.ExpandTool{Store: store}.Execute(context.Background(), json.RawMessage(`{"id":`+mustJSON(t, pointer)+`}`))
	if xerr != nil {
		t.Fatalf("expand(pointer line) error = %v", xerr)
	}
	if expanded != segs5[0].Text {
		t.Error("expand tool did not parse the pointer line down to its id")
	}

	// The byte-for-byte inverse: reconstructing the compacted message with
	// the store restores the original content.
	if restored := compaction.Reconstruct(s.History[5].Content.Text, store); restored != fixture[5].Content.Text {
		t.Errorf("Reconstruct(compacted message) is not byte-for-byte:\n got %q\nwant %q",
			truncateRunes(restored, 200), truncateRunes(fixture[5].Content.Text, 200))
	}

	// The derived task is the last user message's content, truncated to
	// maxTaskChars runes.
	wantTask := truncateRunes(fixture[9].Content.String(), maxTaskChars)
	for i, task := range scorer.tasks {
		if task != wantTask {
			t.Errorf("scorer task[%d] = %q, want the truncated last user message %q", i, truncateRunes(task, 80), truncateRunes(wantTask, 80))
		}
	}
}

// (c2) History compaction stores full records, not bare strings: kind
// elided_segment, origin "history" (Step 12 origin threading), the run's
// token count, the pointer summary, the contributing segment ids, and
// zeroed expand/hit counters — everything Step 13's outcomes attribute
// back through.
func TestCompactContextStoresRecordMetadata(t *testing.T) {
	fixture := defaultFixture()
	s := newCompactSession(fixture)
	segs := fixtureSegments(t, fixture)
	store := compaction.NewStore()

	if _, err := s.CompactContext(context.Background(), elideFirstScorer(segs), store, CompactionOptions{}); err != nil {
		t.Fatalf("CompactContext() error = %v", err)
	}

	// Message 5's elided opening segment is a single-segment run.
	segs5 := segs[5]
	runText := segs5[0].Text
	id := compaction.ContentID(runText, "", "r")
	rec, ok := store.GetRecord(id)
	if !ok {
		t.Fatalf("store must hold a record for %s", id)
	}
	if rec.Text != runText {
		t.Errorf("Text = %q, want the stored run %q", truncateRunes(rec.Text, 120), truncateRunes(runText, 120))
	}
	if rec.Kind != compaction.RecordKindElidedSegment {
		t.Errorf("Kind = %q, want %q", rec.Kind, compaction.RecordKindElidedSegment)
	}
	if rec.Origin != (compaction.Origin{Source: compaction.OriginSourceHistory, Ref: "", Turn: 0}) {
		t.Errorf("Origin = %+v, want {history  0}", rec.Origin)
	}
	if rec.CreatedTurn != 0 {
		t.Errorf("CreatedTurn = %d, want 0 (turn plumbing does not exist yet)", rec.CreatedTurn)
	}
	if rec.Tokens != segs5[0].Tokens {
		t.Errorf("Tokens = %d, want %d", rec.Tokens, segs5[0].Tokens)
	}
	if want := compaction.Summarise(runText, compaction.SummaryMaxChars); rec.Summary != want {
		t.Errorf("Summary = %q, want %q", rec.Summary, want)
	}
	if !reflect.DeepEqual(rec.SegmentIDs, []string{segs5[0].ID}) {
		t.Errorf("SegmentIDs = %v, want [%s]", rec.SegmentIDs, segs5[0].ID)
	}
	if rec.ExpandCount != 0 || rec.HitCount != 0 {
		t.Errorf("counters = (expand %d, hit %d), want zeros", rec.ExpandCount, rec.HitCount)
	}

	// The file-backed production store records the same shape: a put
	// through the ElideStore interface and a reload must preserve it. A
	// fresh session runs the walk (the first run rewrote s's history).
	path := filepath.Join(t.TempDir(), "compaction-store.jsonl")
	persisted, err := compaction.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	fixture2 := defaultFixture()
	s2 := newCompactSession(fixture2)
	segs2 := fixtureSegments(t, fixture2)
	if _, err := s2.CompactContext(context.Background(), elideFirstScorer(segs2), persisted, CompactionOptions{}); err != nil {
		t.Fatalf("CompactContext(file-backed store) error = %v", err)
	}
	reopened, err := compaction.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	rec2, ok := reopened.GetRecord(id)
	if !ok {
		t.Fatalf("reopened store must hold the history record for %s", id)
	}
	if !reflect.DeepEqual(rec2, rec) {
		t.Errorf("reopened record = %+v, want %+v", rec2, rec)
	}
}

// (d) An assistant message with tool calls: Content is compacted, ToolCalls
// (and role) stay structurally intact.
func TestCompactContextCompactsAssistantContentKeepsToolCalls(t *testing.T) {
	fixture := defaultFixture()
	s := newCompactSession(fixture)
	segs := fixtureSegments(t, fixture)

	if _, err := s.CompactContext(context.Background(), elideFirstScorer(segs), NewCompactStore(), CompactionOptions{}); err != nil {
		t.Fatalf("CompactContext() error = %v", err)
	}

	before, after := fixture[7], s.History[7]
	if !reflect.DeepEqual(before.ToolCalls, after.ToolCalls) {
		t.Fatalf("ToolCalls mutated:\n got %+v\nwant %+v", after.ToolCalls, before.ToolCalls)
	}
	if after.Role != before.Role {
		t.Errorf("Role changed: got %q, want %q", after.Role, before.Role)
	}
	if !strings.Contains(after.Content.Text, "[[elided id=") {
		t.Error("assistant Content was not compacted")
	}
	if len(after.Content.Text) >= len(before.Content.Text) {
		t.Error("assistant Content did not shrink")
	}
	// The tool result answering the call keeps its ToolCallID too.
	if s.History[8].ToolCallID != fixture[8].ToolCallID {
		t.Errorf("ToolCallID changed: got %q, want %q", s.History[8].ToolCallID, fixture[8].ToolCallID)
	}
}

// (e) The token math is exact: before/after are the summed
// EstimateMessageTokens of the history and saved is their difference.
func TestCompactContextTokenMath(t *testing.T) {
	fixture := defaultFixture()
	s := newCompactSession(fixture)
	segs := fixtureSegments(t, fixture)

	report, err := s.CompactContext(context.Background(), elideFirstScorer(segs), NewCompactStore(), CompactionOptions{})
	if err != nil {
		t.Fatalf("CompactContext() error = %v", err)
	}

	var wantBefore, wantAfter int
	for i := range fixture {
		wantBefore += common.EstimateMessageTokens(fixture[i])
		wantAfter += common.EstimateMessageTokens(s.History[i])
	}
	if report.TokensBefore != wantBefore {
		t.Errorf("TokensBefore = %d, want %d", report.TokensBefore, wantBefore)
	}
	if report.TokensAfter != wantAfter {
		t.Errorf("TokensAfter = %d, want %d", report.TokensAfter, wantAfter)
	}
	if report.TokensSaved != wantBefore-wantAfter {
		t.Errorf("TokensSaved = %d, want %d", report.TokensSaved, wantBefore-wantAfter)
	}
	if report.TokensSaved <= 0 {
		t.Errorf("TokensSaved = %d, want > 0", report.TokensSaved)
	}
	if report.StoreSize <= 0 {
		t.Errorf("StoreSize = %d, want > 0 (the relocated originals)", report.StoreSize)
	}
}

// (f) Shadow mode: the report carries the honest would-save numbers
// (identical to a mutating run's) and the history stays byte-identical.
func TestCompactContextShadowModeDoesNotMutate(t *testing.T) {
	fixtureJSON := mustJSON(t, defaultFixture())
	segs := fixtureSegments(t, defaultFixture())

	mutating := newCompactSession(defaultFixture())
	shadow := newCompactSession(defaultFixture())
	storeM, storeS := NewCompactStore(), NewCompactStore()

	gotMutating, err := mutating.CompactContext(context.Background(), elideFirstScorer(segs), storeM, CompactionOptions{})
	if err != nil {
		t.Fatalf("mutating run error = %v", err)
	}
	gotShadow, err := shadow.CompactContext(context.Background(), elideFirstScorer(segs), storeS, CompactionOptions{ShadowOnly: true})
	if err != nil {
		t.Fatalf("shadow run error = %v", err)
	}

	if got, want := mustJSON(t, shadow.History), fixtureJSON; got != want {
		t.Fatalf("the shadow run mutated history:\n got %s\nwant %s", truncateRunes(got, 300), truncateRunes(want, 300))
	}
	if !gotShadow.ShadowOnly {
		t.Error("the shadow report must set ShadowOnly")
	}
	if gotMutating.ShadowOnly {
		t.Error("the mutating report must not set ShadowOnly")
	}
	// Honest staging: every number matches the real run.
	if gotShadow.MessagesScanned != gotMutating.MessagesScanned ||
		gotShadow.MessagesScored != gotMutating.MessagesScored ||
		gotShadow.MessagesCompacted != gotMutating.MessagesCompacted ||
		gotShadow.SegmentsElided != gotMutating.SegmentsElided ||
		gotShadow.TokensBefore != gotMutating.TokensBefore ||
		gotShadow.TokensAfter != gotMutating.TokensAfter ||
		gotShadow.TokensSaved != gotMutating.TokensSaved ||
		gotShadow.StoreSize != gotMutating.StoreSize {
		t.Fatalf("shadow report %+v differs from mutating report %+v", gotShadow, gotMutating)
	}
	if gotShadow.MessagesCompacted == 0 || gotShadow.TokensSaved <= 0 {
		t.Fatalf("shadow report not populated: %+v", gotShadow)
	}
	// Shadow stores nothing; the mutating run stores under content ids.
	if storeS.Len() != 0 {
		t.Error("the shadow run must not store originals")
	}
	if storeM.Len() == 0 {
		t.Error("the mutating run must store originals")
	}
	// The mutating run really did rewrite history.
	if got := mustJSON(t, mutating.History); got == fixtureJSON {
		t.Error("the mutating run left history unchanged")
	}
}

// (g) Wholesale mid-walk failure: the scorer fails on the third candidate
// with no usable scores (the default stubErrWholesale shape); the first two
// rewritten messages stay rewritten, the rest are untouched, and the error
// reports how far the walk got.
func TestCompactContextFailOpenMidWalk(t *testing.T) {
	sentinel := errors.New("scorer unavailable")
	fixture := []client.ChatMessage{
		cmpStamped(cmpSystem("system prompt"), 0),                                                             // frozen
		cmpStamped(cmpUser("first question"), 1),                                                              // frozen
		cmpStamped(cmpAssistant("first answer"), 2),                                                           // frozen
		cmpStamped(cmpAssistantWithCalls(cmpLongText("c1", 3), []client.ToolCall{cmpToolCall("call_c1")}), 3), // candidate 1
		cmpStamped(cmpTool(cmpLongText("c2", 3)), 4),                                                          // candidate 2
		cmpStamped(cmpAssistantWithCalls(cmpLongText("c3", 3), []client.ToolCall{cmpToolCall("call_c3")}), 5), // candidate 3 — fails here
		cmpStamped(cmpTool(cmpLongText("c4", 3)), 6),                                                          // candidate 4
		cmpStamped(cmpAssistantWithCalls(cmpLongText("c5", 3), []client.ToolCall{cmpToolCall("call_c5")}), 7), // candidate 5
	}
	s := newCompactSession(fixture)
	segs := fixtureSegments(t, fixture)
	if len(segs) != 5 {
		t.Fatalf("fixture candidates = %d, want 5", len(segs))
	}
	scorer := elideFirstScorer(segs)
	scorer.err = sentinel
	scorer.errOnCall = 3

	report, err := s.CompactContext(context.Background(), scorer, NewCompactStore(), CompactionOptions{})
	if err == nil {
		t.Fatal("CompactContext() error = nil, want the scorer failure")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error does not wrap the scorer failure: %v", err)
	}
	if !strings.Contains(err.Error(), "after 2 messages") {
		t.Errorf("error %q does not report the completed-message count", err)
	}

	// Messages 1-2 of the walk (fixture 3-4) stay rewritten.
	for _, idx := range []int{3, 4} {
		if !strings.Contains(s.History[idx].Content.Text, "[[elided id=") {
			t.Errorf("fixture message %d should have been rewritten before the failure", idx)
		}
	}
	// Messages 3+ of the walk are untouched.
	for _, idx := range []int{5, 6, 7} {
		if got, want := s.History[idx].Content.Text, fixture[idx].Content.Text; got != want {
			t.Errorf("fixture message %d was mutated after the failure:\n got %q", idx, truncateRunes(got, 120))
		}
	}
	if report.MessagesCompacted != 2 {
		t.Errorf("MessagesCompacted = %d, want 2", report.MessagesCompacted)
	}
	if report.SegmentsElided != 2 {
		t.Errorf("SegmentsElided = %d, want 2", report.SegmentsElided)
	}
}

// (Step 7a) A scorer error WITH a complete score map is the pipeline's
// fail-open contract (compaction.DecisionClient.ScoreBatch answers every id,
// failed items as keep-scores): the walk completes with a normal-run report,
// every scored message is counted in MessagesScored, and the scorer's errors
// surface at the end wrapped in the returned error — instead of the old,
// wrong "context compaction stopped after 0 messages" abort.
func TestCompactContextFailOpenCompleteScoresFinishWalk(t *testing.T) {
	sentinel := errors.New("scorer degraded")
	fixture := defaultFixture()
	s := newCompactSession(fixture)
	segs := fixtureSegments(t, fixture)
	scorer := elideFirstScorer(segs)
	// Every call fails but answers every id with the stub's normal scores:
	// the run must be indistinguishable from a clean one except for the
	// surfaced error.
	scorer.err = sentinel
	scorer.errShape = stubErrFailOpen

	report, err := s.CompactContext(context.Background(), scorer, NewCompactStore(), CompactionOptions{})
	if err == nil {
		t.Fatal("CompactContext() error = nil, want the scorer's fail-open errors surfaced")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error does not wrap the scorer failure: %v", err)
	}
	if strings.Contains(err.Error(), "stopped after") {
		t.Errorf("a completed walk must not report a mid-walk stop: %v", err)
	}

	// The report reflects a normal run: every candidate rewritten, one
	// segment each elided.
	if report.MessagesCompacted != len(segs) {
		t.Errorf("MessagesCompacted = %d, want %d", report.MessagesCompacted, len(segs))
	}
	if report.SegmentsElided != len(segs) {
		t.Errorf("SegmentsElided = %d, want %d", report.SegmentsElided, len(segs))
	}
	if report.MessagesScored != len(segs) {
		t.Errorf("MessagesScored = %d, want %d (every candidate scored, fail-open ones included)", report.MessagesScored, len(segs))
	}
	if report.TokensSaved <= 0 {
		t.Errorf("TokensSaved = %d, want > 0", report.TokensSaved)
	}
	// The walk really completed: the last candidate was rewritten too.
	if !strings.Contains(s.History[8].Content.Text, "[[elided id=") {
		t.Error("expected the last candidate (fixture index 8) to be compacted — the walk must not stop")
	}
}

// (Step 7b) A scorer error with a PARTIAL score map (some requested ids
// missing) is a wholesale failure: the walk aborts with "context compaction
// stopped after N messages", N the messages successfully scored before the
// failure — a partial answer is never trusted for elisions.
func TestCompactContextPartialScoresAbortMidWalk(t *testing.T) {
	sentinel := errors.New("scorer degraded")
	fixture := []client.ChatMessage{
		cmpStamped(cmpSystem("system prompt"), 0),                                                             // frozen
		cmpStamped(cmpUser("first question"), 1),                                                              // frozen
		cmpStamped(cmpAssistant("first answer"), 2),                                                           // frozen
		cmpStamped(cmpAssistantWithCalls(cmpLongText("c1", 3), []client.ToolCall{cmpToolCall("call_c1")}), 3), // candidate 1 — scored cleanly
		cmpStamped(cmpTool(cmpLongText("c2", 3)), 4),                                                          // candidate 2 — partial scores here
		cmpStamped(cmpAssistantWithCalls(cmpLongText("c3", 3), []client.ToolCall{cmpToolCall("call_c3")}), 5), // candidate 3
		cmpStamped(cmpTool(cmpLongText("c4", 3)), 6),                                                          // candidate 4
	}
	s := newCompactSession(fixture)
	segs := fixtureSegments(t, fixture)
	if len(segs) != 4 {
		t.Fatalf("fixture candidates = %d, want 4", len(segs))
	}
	scorer := elideFirstScorer(segs)
	scorer.err = sentinel
	scorer.errOnCall = 2
	scorer.errShape = stubErrPartial
	scorer.errPartialIds = 1 // one of candidate 2's three segments answered

	report, err := s.CompactContext(context.Background(), scorer, NewCompactStore(), CompactionOptions{})
	if err == nil {
		t.Fatal("CompactContext() error = nil, want the wholesale failure")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error does not wrap the scorer failure: %v", err)
	}
	if !strings.Contains(err.Error(), "stopped after 1 messages") {
		t.Errorf("error %q does not report the one message scored before the failure", err)
	}
	if report.MessagesScored != 1 {
		t.Errorf("MessagesScored = %d, want 1", report.MessagesScored)
	}
	// Candidate 1 was scored and rewritten before the failure.
	if !strings.Contains(s.History[3].Content.Text, "[[elided id=") {
		t.Error("fixture message 3 should have been rewritten before the failure")
	}
	// Candidates 2+ are untouched — the walk stopped at the partial answer.
	for _, idx := range []int{4, 5, 6} {
		if got, want := s.History[idx].Content.Text, fixture[idx].Content.Text; got != want {
			t.Errorf("fixture message %d was mutated after the partial failure:\n got %q", idx, truncateRunes(got, 120))
		}
	}
	if report.MessagesCompacted != 1 || report.SegmentsElided != 1 {
		t.Errorf("expected exactly the pre-failure rewrite, got %+v", report)
	}
}

// (Step 7c) A clean scorer: nil error, and MessagesScored counts every
// message whose segments were scored.
func TestCompactContextCleanScorerCountsScoredMessages(t *testing.T) {
	fixture := defaultFixture()
	s := newCompactSession(fixture)
	segs := fixtureSegments(t, fixture)
	scorer := elideFirstScorer(segs)

	report, err := s.CompactContext(context.Background(), scorer, NewCompactStore(), CompactionOptions{})
	if err != nil {
		t.Fatalf("CompactContext() error = %v", err)
	}
	if report.MessagesScored != len(segs) {
		t.Errorf("MessagesScored = %d, want %d (one per scored candidate)", report.MessagesScored, len(segs))
	}
	if report.MessagesScored != scorer.calls {
		t.Errorf("MessagesScored = %d, scorer calls = %d; want them equal", report.MessagesScored, scorer.calls)
	}
}

// (Step 7d) The user's regression pin: when the very first candidate's
// ScoreBatch fails wholesale (a backend rejecting the scoring request — no
// usable scores at all), the error says "stopped after 0 messages" and
// nothing is rewritten. The fail-open-complete variant of the same symptom no
// longer aborts — see TestCompactContextFailOpenCompleteScoresFinishWalk.
func TestCompactContextWholesaleFailureOnFirstCandidateStopsAtZero(t *testing.T) {
	sentinel := errors.New("decisions API error (422): model too small")
	fixture := defaultFixture()
	s := newCompactSession(fixture)
	segs := fixtureSegments(t, fixture)
	scorer := elideFirstScorer(segs)
	scorer.err = sentinel
	scorer.errOnCall = 1 // wholesale: no usable scores (default stubErrWholesale)

	report, err := s.CompactContext(context.Background(), scorer, NewCompactStore(), CompactionOptions{})
	if err == nil {
		t.Fatal("CompactContext() error = nil, want the scorer failure")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error does not wrap the scorer failure: %v", err)
	}
	if !strings.Contains(err.Error(), "context compaction stopped after 0 messages") {
		t.Errorf("error %q does not pin the zero-progress abort", err)
	}
	if report.MessagesScored != 0 {
		t.Errorf("MessagesScored = %d, want 0", report.MessagesScored)
	}
	if report.MessagesCompacted != 0 || report.SegmentsElided != 0 {
		t.Errorf("expected a no-rewrite report, got %+v", report)
	}
	// Nothing was rewritten: the first candidate (fixture index 4) is intact.
	if got, want := s.History[4].Content.Text, fixture[4].Content.Text; got != want {
		t.Errorf("fixture message 4 was rewritten before the abort:\n got %q", truncateRunes(got, 120))
	}
}

// (h) Timestamps survive compaction untouched — on compacted and frozen
// messages alike. (The receive-time Timestamp field belongs to the excluded
// timestamps feature, so that assertion lives with it; compaction's
// rewrite path preserves everything except the compacted content.)

// (i) Short histories are no-ops: nothing eligible, nothing rewritten, a
// zero report.
func TestCompactContextShortHistoryNoOp(t *testing.T) {
	t.Run("single system message", func(t *testing.T) {
		fixture := []client.ChatMessage{cmpStamped(cmpSystem("only the prompt"), 0)}
		s := newCompactSession(fixture)
		report, err := s.CompactContext(context.Background(), elideFirstScorer(nil), NewCompactStore(), CompactionOptions{})
		if err != nil {
			t.Fatalf("CompactContext() error = %v", err)
		}
		assertZeroReport(t, report, 0) // the only message is frozen
		if got, want := mustJSON(t, s.History), mustJSON(t, fixture); got != want {
			t.Fatalf("history mutated:\n got %s\nwant %s", got, want)
		}
	})

	t.Run("system plus long user message", func(t *testing.T) {
		fixture := []client.ChatMessage{
			cmpStamped(cmpSystem("system prompt"), 0),
			cmpStamped(cmpUser(cmpLongText("u", 3)), 1), // long, but a user message
		}
		s := newCompactSession(fixture)
		report, err := s.CompactContext(context.Background(), elideFirstScorer(nil), NewCompactStore(), CompactionOptions{})
		if err != nil {
			t.Fatalf("CompactContext() error = %v", err)
		}
		assertZeroReport(t, report, 1) // the user message is scanned but never compacted
		if got, want := mustJSON(t, s.History), mustJSON(t, fixture); got != want {
			t.Fatalf("history mutated:\n got %s\nwant %s", got, want)
		}
	})

	t.Run("system, user and short assistant", func(t *testing.T) {
		fixture := []client.ChatMessage{
			cmpStamped(cmpSystem("system prompt"), 0),
			cmpStamped(cmpUser("question"), 1),
			cmpStamped(cmpAssistant("a short answer"), 2), // under minCompactChars
		}
		s := newCompactSession(fixture)
		report, err := s.CompactContext(context.Background(), elideFirstScorer(nil), NewCompactStore(), CompactionOptions{})
		if err != nil {
			t.Fatalf("CompactContext() error = %v", err)
		}
		assertZeroReport(t, report, 2)
		if got, want := mustJSON(t, s.History), mustJSON(t, fixture); got != want {
			t.Fatalf("history mutated:\n got %s\nwant %s", got, want)
		}
	})

	t.Run("everything frozen via FrozenPercent 100", func(t *testing.T) {
		fixture := defaultFixture()
		s := newCompactSession(fixture)
		report, err := s.CompactContext(context.Background(), elideFirstScorer(fixtureSegments(t, fixture)), NewCompactStore(), CompactionOptions{FrozenPercent: 100})
		if err != nil {
			t.Fatalf("CompactContext() error = %v", err)
		}
		assertZeroReport(t, report, 0) // every message sits inside the frozen prefix
		if got, want := mustJSON(t, s.History), mustJSON(t, fixture); got != want {
			t.Fatalf("history mutated:\n got %s\nwant %s", got, want)
		}
	})
}

// (j) All-high scores: the walk runs (every candidate scored) but nothing is
// elided — a populated scan report with zero rewrites and unchanged history.
func TestCompactContextAllHighScoresNoOp(t *testing.T) {
	fixture := defaultFixture()
	s := newCompactSession(fixture)
	segs := fixtureSegments(t, fixture)
	scorer := &stubScorer{fallback: 0.9} // nothing elidable

	report, err := s.CompactContext(context.Background(), scorer, NewCompactStore(), CompactionOptions{})
	if err != nil {
		t.Fatalf("CompactContext() error = %v", err)
	}
	if got, want := mustJSON(t, s.History), mustJSON(t, fixture); got != want {
		t.Fatalf("history mutated under all-high scores:\n got %s", truncateRunes(got, 300))
	}
	if report.MessagesScanned != len(fixture)-3 {
		t.Errorf("MessagesScanned = %d, want %d", report.MessagesScanned, len(fixture)-3)
	}
	if report.MessagesCompacted != 0 || report.SegmentsElided != 0 || report.TokensSaved != 0 || report.StoreSize != 0 {
		t.Errorf("expected a no-op report, got %+v", report)
	}
	if report.MessagesScored != len(segs) {
		t.Errorf("MessagesScored = %d, want %d (every candidate scored)", report.MessagesScored, len(segs))
	}
	if scorer.calls != len(segs) {
		t.Errorf("scorer calls = %d, want %d", scorer.calls, len(segs))
	}
}

// The threshold is exclusive and defaults to the relocation default: a
// segment scoring exactly compaction.DefaultRelocationThreshold is kept, one
// strictly below is elided.
func TestCompactContextThresholdIsExclusive(t *testing.T) {
	build := func() (*Session, []client.ChatMessage, map[int][]compaction.Segment) {
		fixture := []client.ChatMessage{
			cmpStamped(cmpSystem("system prompt"), 0),
			cmpStamped(cmpUser("question"), 1),
			cmpStamped(cmpAssistantWithCalls(cmpLongText("x", 4), []client.ToolCall{cmpToolCall("call_x")}), 2), // the only candidate (n=4 clears minCompactChars)
		}
		return newCompactSession(fixture), fixture, fixtureSegments(t, fixture)
	}

	t.Run("exactly the default threshold is kept", func(t *testing.T) {
		s, fixture, segs := build()
		scorer := elideFirstScorer(segs)
		scorer.scores[segs[2][0].Text] = compaction.DefaultRelocationThreshold
		report, err := s.CompactContext(context.Background(), scorer, NewCompactStore(), CompactionOptions{})
		if err != nil {
			t.Fatalf("CompactContext() error = %v", err)
		}
		if report.MessagesCompacted != 0 || report.SegmentsElided != 0 {
			t.Errorf("expected a keep at the exact threshold, got %+v", report)
		}
		if s.History[2].Content.Text != fixture[2].Content.Text {
			t.Error("history mutated at the exact threshold")
		}
	})

	t.Run("just below the default threshold is elided", func(t *testing.T) {
		s, _, segs := build()
		scorer := elideFirstScorer(segs)
		scorer.scores[segs[2][0].Text] = compaction.DefaultRelocationThreshold - 0.01
		report, err := s.CompactContext(context.Background(), scorer, NewCompactStore(), CompactionOptions{})
		if err != nil {
			t.Fatalf("CompactContext() error = %v", err)
		}
		if report.MessagesCompacted != 1 || report.SegmentsElided != 1 {
			t.Fatalf("expected one elision just below the threshold, got %+v", report)
		}
		if !strings.Contains(s.History[2].Content.Text, "[[elided id=r:") {
			t.Error("expected the segment just below the threshold to be elided under a content id")
		}
	})
}

// (P10) Out-of-range CompactionOptions clamp to the safe production defaults:
// a threshold outside (0, 1] falls back to compaction.DefaultRelocationThreshold
// (0.35 — pinned via the keep-at-exactly-0.35 behavior, which an unclamped 0
// would break by eliding everything and a 5 would break by keeping everything),
// and a frozen percent outside (0, 100] falls back to defaultFrozenPercent (25).
func TestCompactContextOptionsClampToDefaults(t *testing.T) {
	fixture := []client.ChatMessage{
		cmpStamped(cmpSystem("system prompt"), 0),
		cmpStamped(cmpUser("question"), 1),
		cmpStamped(cmpAssistantWithCalls(cmpLongText("x", 4), []client.ToolCall{cmpToolCall("call_x")}), 2),
	}

	t.Run("threshold 0 clamps to the default 0.35", func(t *testing.T) {
		s := newCompactSession(fixture)
		segs := fixtureSegments(t, fixture)
		scorer := elideFirstScorer(segs)
		scorer.scores[segs[2][0].Text] = compaction.DefaultRelocationThreshold // exactly the default: kept
		if _, err := s.CompactContext(context.Background(), scorer, NewCompactStore(), CompactionOptions{Threshold: 0}); err != nil {
			t.Fatalf("CompactContext() error = %v", err)
		}
		if s.History[2].Content.Text != fixture[2].Content.Text {
			t.Error("threshold 0 must clamp to 0.35, which keeps a segment at exactly 0.35")
		}
	})

	t.Run("threshold 5 clamps to the default 0.35", func(t *testing.T) {
		s := newCompactSession(fixture)
		segs := fixtureSegments(t, fixture)
		scorer := elideFirstScorer(segs)
		scorer.scores[segs[2][0].Text] = compaction.DefaultRelocationThreshold - 0.01 // just below: elided
		if _, err := s.CompactContext(context.Background(), scorer, NewCompactStore(), CompactionOptions{Threshold: 5}); err != nil {
			t.Fatalf("CompactContext() error = %v", err)
		}
		if !strings.Contains(s.History[2].Content.Text, "[[elided id=r:") {
			t.Error("threshold 5 must clamp to 0.35, which elides a segment just below 0.35")
		}
	})

	t.Run("frozen percent 0 and 150 clamp to the default 25", func(t *testing.T) {
		for _, bad := range []int{0, 150} {
			s := newCompactSession(fixture)
			if _, err := s.CompactContext(context.Background(), &stubScorer{fallback: 0.9}, NewCompactStore(), CompactionOptions{FrozenPercent: bad}); err != nil {
				t.Fatalf("FrozenPercent %d: CompactContext() error = %v", bad, err)
			}
			// 3 messages at the clamped default 25% → frozen = max(1, 0) = 1,
			// so the assistant candidate at index 2 was scanned.
			if s.History[2].Content.Text != fixture[2].Content.Text {
				t.Errorf("FrozenPercent %d: history mutated (clamp broken)", bad)
			}
		}
	})
}

// A nil scorer is a wiring bug: CompactContext refuses to run and leaves the
// history untouched. A nil store leaves relocation disarmed: nothing is
// elided (a pointer whose original cannot be stored must never enter
// history), mirroring the pipeline with relocation off.
func TestCompactContextFailSafes(t *testing.T) {
	fixture := defaultFixture()

	t.Run("nil scorer", func(t *testing.T) {
		s := newCompactSession(fixture)
		report, err := s.CompactContext(context.Background(), nil, NewCompactStore(), CompactionOptions{})
		if err == nil {
			t.Fatal("nil scorer: want an error")
		}
		if report != (CompactionReport{}) {
			t.Errorf("nil scorer: want a zero report, got %+v", report)
		}
		if got, want := mustJSON(t, s.History), mustJSON(t, fixture); got != want {
			t.Error("nil scorer: history must stay untouched")
		}
	})

	t.Run("nil store disarms relocation", func(t *testing.T) {
		s := newCompactSession(fixture)
		report, err := s.CompactContext(context.Background(), elideFirstScorer(fixtureSegments(t, fixture)), nil, CompactionOptions{})
		if err != nil {
			t.Fatalf("nil store: error = %v, want nil", err)
		}
		if got, want := mustJSON(t, s.History), mustJSON(t, fixture); got != want {
			t.Error("nil store: history must stay untouched")
		}
		if report.MessagesScanned != len(fixture)-3 {
			t.Errorf("nil store: MessagesScanned = %d, want %d", report.MessagesScanned, len(fixture)-3)
		}
		if report.MessagesCompacted != 0 || report.SegmentsElided != 0 {
			t.Errorf("nil store: expected no elisions, got %+v", report)
		}
	})
}

// (Step 11) History round trip: a tool-result message carrying quotes,
// backslashes, and newlines compacts into in-place pointers under content
// ids, and compaction.Reconstruct restores the original content byte for
// byte. Legacy ids keep expanding: the counter still mints elide-N ids and
// the store resolves them.
func TestCompactContextReconstructRoundTrip(t *testing.T) {
	para := func(marker string, n int) string { return marker + strings.Repeat(" "+marker+"-filler", n) }
	content := strings.Join([]string{
		para(`keeper one "quoted" \ with backslashes`, 20),
		"secret one starts \"with quotes\" and \\ a backslash\nsecret one line two\n" + para("s-one-tail", 12),
		para("keeper two", 20),
		"secret two \\ odd first line\n" + para("s-two-tail", 12),
		para("keeper three", 20),
	}, "\n\n")
	fixture := []client.ChatMessage{
		cmpStamped(cmpSystem("system prompt"), 0),
		cmpStamped(cmpUser("question"), 1),
		cmpStamped(cmpAssistant("short"), 2),
		cmpStamped(cmpTool(content), 3), // the only compactable candidate
	}
	segs := compaction.SegmentSegments(content, minCompactChars)
	if len(segs) != 5 {
		t.Fatalf("SegmentSegments() = %d segments, want 5", len(segs))
	}

	// Score the exact segment texts (the stub keys by text): the two secret
	// paragraphs elide, everything else stays.
	scorer := &stubScorer{
		scores: map[string]float64{
			segs[1].Text: 0.05,
			segs[3].Text: 0.05,
		},
		fallback: 0.9,
	}
	store := compaction.NewStore()
	s := newCompactSession(fixture)

	report, err := s.CompactContext(context.Background(), scorer, store, CompactionOptions{})
	if err != nil {
		t.Fatalf("CompactContext() error = %v", err)
	}
	if report.MessagesCompacted != 1 || report.SegmentsElided != 2 {
		t.Fatalf("report = %+v, want one compacted message with 2 elided segments", report)
	}
	if report.StoreSize != len(segs[1].Text)+len(segs[3].Text) {
		t.Errorf("StoreSize = %d, want the two run originals' size", report.StoreSize)
	}

	compacted := s.History[3].Content.Text
	if strings.Contains(compacted, "secret one line two") || strings.Contains(compacted, "s-two-tail") {
		t.Errorf("compacted message leaked an elided run's body:\n%s", compacted)
	}
	for _, keeper := range []string{segs[0].Text, segs[2].Text, segs[4].Text} {
		if !strings.Contains(compacted, keeper) {
			t.Errorf("compacted message lost a kept segment:\n%s", compacted)
		}
	}

	// Two pointers, each naming its run's content id (salt "").
	pointers := compaction.FindPointers(compacted)
	if len(pointers) != 2 {
		t.Fatalf("FindPointers(compacted) = %d pointers, want 2", len(pointers))
	}
	for i, p := range pointers {
		if want := compaction.ContentID(segs[2*i+1].Text, "", "r"); p.ID != want {
			t.Errorf("pointer[%d].ID = %q, want the content id %q", i, p.ID, want)
		}
		if p.Lines == nil || *p.Lines != [2]int{segs[2*i+1].LineStart, segs[2*i+1].LineEnd} {
			t.Errorf("pointer[%d] lines = %v, want the segment's line span", i, p.Lines)
		}
	}

	// THE guarantee: reconstruct is the byte-for-byte inverse of the walk.
	if restored := compaction.Reconstruct(compacted, store); restored != content {
		t.Errorf("Reconstruct(compacted) is not byte-for-byte:\n got %q\nwant %q", restored, content)
	}

	// Legacy ids keep working: the counter still mints elide-N and Put/Get
	// round-trips them (backward compatibility for old pointers).
	if got := store.NextID(); got != "elide-1" {
		t.Errorf("NextID() = %q, want the legacy elide-1", got)
	}
	store.Put("elide-1", "legacy original")
	if text, ok := store.Get("elide-1"); !ok || text != "legacy original" {
		t.Errorf("Get(elide-1) = (%q, %v), want the legacy original", text, ok)
	}
}

// CompactStore mints sequential elide ids and round-trips originals; ids
// without a stored original miss (the expand tool's unknown-id path).
func TestCompactStoreRoundTrip(t *testing.T) {
	store := NewCompactStore()
	if got := store.NextID(); got != "elide-1" {
		t.Errorf("NextID() = %q, want elide-1", got)
	}
	if got := store.NextID(); got != "elide-2" {
		t.Errorf("NextID() = %q, want elide-2", got)
	}
	store.Put("elide-1", "original one")
	got, ok := store.Get("elide-1")
	if !ok || got != "original one" {
		t.Fatalf("Get(elide-1) = %q, %v; want the stored original", got, ok)
	}
	if _, ok := store.Get("elide-2"); ok {
		t.Error("Get(elide-2) = hit; want a miss for an id with no stored original")
	}
	if _, ok := store.Get("elide-999"); ok {
		t.Error("Get(elide-999) = hit; want a miss")
	}
}

// --- Step 14: frozen-prefix high-water mark ----------------------------------

// (Step 14a) Two consecutive runs on a growing history: the second run
// rewrites NOTHING below the first run's high-water mark — the frozen prefix
// is append-only, the prompt-cache anchor never moves — and only the new
// messages are candidates.
func TestCompactContextHighWaterFreezesAcrossRuns(t *testing.T) {
	fixture := defaultFixture()
	s := newCompactSession(fixture)
	segs := fixtureSegments(t, fixture)

	first, err := s.CompactContext(context.Background(), elideFirstScorer(segs), NewCompactStore(), CompactionOptions{})
	if err != nil {
		t.Fatalf("first CompactContext() error = %v", err)
	}
	if got, want := s.CompactionHighWater(), len(fixture); got != want {
		t.Fatalf("high-water after the first run = %d, want %d", got, want)
	}
	afterFirst := mustJSON(t, s.History)
	if first.MessagesCompacted != len(segs) {
		t.Fatalf("first run MessagesCompacted = %d, want %d", first.MessagesCompacted, len(segs))
	}

	// Grow the history: a fresh question answered with two compactable
	// messages. Everything below the mark must stay exactly as run 1 left it.
	growth := []client.ChatMessage{
		cmpStamped(cmpUser("Second question"), 12),
		cmpStamped(cmpAssistantWithCalls(cmpLongText("eps", 3), []client.ToolCall{cmpToolCall("call_13")}), 13), // new candidate
		cmpStamped(cmpToolResult("call_13", cmpLongText("zeta", 3)), 14),                                        // new candidate
		cmpStamped(cmpAssistant("A short new answer."), 15),
	}
	s.History = append(s.History, growth...)
	full := append(append([]client.ChatMessage{}, fixture...), growth...)
	scorer2 := elideFirstScorer(fixtureSegments(t, full))

	second, err := s.CompactContext(context.Background(), scorer2, NewCompactStore(), CompactionOptions{})
	if err != nil {
		t.Fatalf("second CompactContext() error = %v", err)
	}

	// The walk starts at the mark (12), not at the count-based prefix
	// (16/4 = 4): only the four new messages were examined.
	if second.MessagesScanned != len(growth) {
		t.Errorf("second run MessagesScanned = %d, want %d (the walk starts at the mark)", second.MessagesScanned, len(growth))
	}
	// Nothing below the mark was rewritten — the first run's bytes stand.
	if got, want := mustJSON(t, s.History[:len(fixture)]), afterFirst; got != want {
		t.Errorf("the second run rewrote the frozen prefix:\n got %s\nwant %s", truncateRunes(got, 300), truncateRunes(want, 300))
	}
	// Only the two new candidates reached the scorer.
	if scorer2.calls != 2 {
		t.Errorf("second run scorer calls = %d, want 2 (the new candidates only)", scorer2.calls)
	}
	if second.MessagesScored != 2 || second.MessagesCompacted != 2 || second.SegmentsElided != 2 {
		t.Errorf("second run report = %+v, want the two new candidates scored and rewritten", second)
	}
	for _, idx := range []int{13, 14} {
		if !strings.Contains(s.History[idx].Content.Text, "[[elided id=") {
			t.Errorf("the new candidate at index %d was not compacted", idx)
		}
	}
	// The mark advanced to the length the second walk covered.
	if got, want := s.CompactionHighWater(), len(full); got != want {
		t.Errorf("high-water after the second run = %d, want %d", got, want)
	}
}

// (Step 14a) The mark survives save/reload: a completed run persists it
// through the session meta sidecar, a session rebuilt the way the resume
// path does (history from disk, mark from the sidecar) starts its next walk
// at the persisted mark, and its own advance persists again.
func TestCompactContextHighWaterPersistsAcrossSaveReload(t *testing.T) {
	tmpDir := t.TempDir()
	oldSessionDir := SessionDir
	SessionDir = func() (string, error) { return tmpDir, nil }
	defer func() { SessionDir = oldSessionDir }()

	historyPath := filepath.Join(tmpDir, "session-hw.json")
	fixture := defaultFixture()
	s := New(nil, historyPath, cloneHistory(fixture), "", false)

	if _, err := s.CompactContext(context.Background(), elideFirstScorer(fixtureSegments(t, fixture)), NewCompactStore(), CompactionOptions{}); err != nil {
		t.Fatalf("first CompactContext() error = %v", err)
	}
	if got, want := s.CompactionHighWater(), len(fixture); got != want {
		t.Fatalf("in-memory high-water = %d, want %d", got, want)
	}
	// History persistence stays the caller's job (the production runner
	// saves right after the run) — save so the reload below sees the
	// compacted history the mark describes.
	if err := SaveHistory(historyPath, s.History); err != nil {
		t.Fatalf("SaveHistory() error = %v", err)
	}
	meta, err := LoadSessionMeta("session-hw")
	if err != nil || meta == nil {
		t.Fatalf("LoadSessionMeta(session-hw) = (%v, %v)", meta, err)
	}
	if meta.CompactionHighWater != len(fixture) {
		t.Fatalf("persisted CompactionHighWater = %d, want %d", meta.CompactionHighWater, len(fixture))
	}

	// Reload the way cmd/late resumes: history from disk, mark from the
	// sidecar — then grow and compact again.
	reloaded, err := LoadHistory(historyPath)
	if err != nil {
		t.Fatalf("LoadHistory() error = %v", err)
	}
	resumed := New(nil, historyPath, reloaded, "", false)
	resumed.SetCompactionHighWater(meta.CompactionHighWater)

	growth := []client.ChatMessage{
		cmpStamped(cmpUser("Second question"), 12),
		cmpStamped(cmpAssistantWithCalls(cmpLongText("eps", 3), []client.ToolCall{cmpToolCall("call_13b")}), 13),
	}
	for _, msg := range growth {
		if err := resumed.AddMessage(msg); err != nil {
			t.Fatalf("AddMessage() error = %v", err)
		}
	}
	full := append(append([]client.ChatMessage{}, fixture...), growth...)
	scorer2 := elideFirstScorer(fixtureSegments(t, full))

	second, err := resumed.CompactContext(context.Background(), scorer2, NewCompactStore(), CompactionOptions{})
	if err != nil {
		t.Fatalf("second CompactContext() error = %v", err)
	}
	if second.MessagesScanned != len(growth) {
		t.Errorf("second run MessagesScanned = %d, want %d (the resumed mark froze the old history)", second.MessagesScanned, len(growth))
	}
	if scorer2.calls != 1 {
		t.Errorf("second run scorer calls = %d, want 1 (the new candidate only)", scorer2.calls)
	}
	if got, want := resumed.CompactionHighWater(), len(full); got != want {
		t.Errorf("high-water after the resumed run = %d, want %d", got, want)
	}
	meta, err = LoadSessionMeta("session-hw")
	if err != nil || meta == nil {
		t.Fatalf("LoadSessionMeta(session-hw) after the resumed run = (%v, %v)", meta, err)
	}
	if meta.CompactionHighWater != len(full) {
		t.Errorf("persisted CompactionHighWater after the resumed run = %d, want %d", meta.CompactionHighWater, len(full))
	}
}

// (Step 14b) A stale mark over a shrunken history: the persisted mark
// outlives the history it froze (the sidecar was written when the history
// held 12 messages; the history file now holds 6). The only way to make
// progress would be to rewrite below-mark messages, so the run fails loudly
// with ErrFrozenPrefix and changes nothing — the reference's
// FrozenPrefixError analog.
func TestCompactContextFrozenPrefixViolationFailsLoud(t *testing.T) {
	fixture := defaultFixture()
	s := newCompactSession(fixture)
	s.SetCompactionHighWater(len(fixture))
	s.History = s.History[:6]
	snapshot := mustJSON(t, s.History)

	report, err := s.CompactContext(context.Background(), elideFirstScorer(fixtureSegments(t, fixture)), NewCompactStore(), CompactionOptions{})
	if !errors.Is(err, ErrFrozenPrefix) {
		t.Fatalf("CompactContext() error = %v, want ErrFrozenPrefix", err)
	}
	if !strings.Contains(err.Error(), "high-water mark 12") || !strings.Contains(err.Error(), "6-message") {
		t.Errorf("error %q does not name the stale mark and the shrunken history", err)
	}
	if got := mustJSON(t, s.History); got != snapshot {
		t.Errorf("the violating run mutated history:\n got %s", truncateRunes(got, 300))
	}
	if got := s.CompactionHighWater(); got != len(fixture) {
		t.Errorf("the violating run moved the mark to %d, want %d", got, len(fixture))
	}
	if report.MessagesScanned != 0 || report.MessagesScored != 0 || report.MessagesCompacted != 0 || report.SegmentsElided != 0 {
		t.Errorf("the violating run reported work: %+v", report)
	}
}

// (Step 14c) A pointer-bearing message is never re-scored: loaded with a
// zero mark (e.g. a sidecar from before the high-water mark existed), a tool
// result that already carries a final [[elided …]] pointer sits in the work
// area — and must be skipped entirely (never re-segmented, never re-scored,
// never rewritten), while the fresh candidate below it compacts as usual.
func TestCompactContextNeverRescoresPointerBearingMessages(t *testing.T) {
	pointer := compaction.FormatPointer(compaction.Pointer{
		ID:      compaction.ContentID("the earlier run's elided original", "", "r"),
		Lines:   &[2]int{1, 4},
		Tokens:  12,
		Summary: "earlier run's elided output",
	})
	pointerBearing := cmpLongText("kept", 3) + "\n" + pointer + "\n" + cmpLongText("tail", 3)
	if n := len(compaction.FindPointers(pointerBearing)); n != 1 {
		t.Fatalf("fixture sanity: FindPointers found %d pointers, want 1", n)
	}
	fixture := []client.ChatMessage{
		cmpStamped(cmpSystem("system prompt"), 0),
		cmpStamped(cmpUser("question"), 1),
		cmpStamped(cmpTool(pointerBearing), 2), // pointer-bearing candidate
		cmpStamped(cmpAssistantWithCalls(cmpLongText("fresh", 3), []client.ToolCall{cmpToolCall("call_f")}), 3), // control candidate
		cmpStamped(cmpUser("follow-up"), 4),
		cmpStamped(cmpAssistant("short"), 5),
	}
	s := newCompactSession(fixture)
	store := NewCompactStore()
	scorer := &stubScorer{fallback: 0.1} // everything scored would be elided

	report, err := s.CompactContext(context.Background(), scorer, store, CompactionOptions{})
	if err != nil {
		t.Fatalf("CompactContext() error = %v", err)
	}

	// Only the control candidate was scored — the pointer-bearing message,
	// large and post-prefix as it is, never reached the scorer.
	if scorer.calls != 1 {
		t.Errorf("scorer calls = %d, want 1 (the pointer-bearing message must never be re-scored)", scorer.calls)
	}
	if got, want := s.History[2].Content.Text, pointerBearing; got != want {
		t.Errorf("the pointer-bearing message was rewritten:\n got %q\nwant %q", truncateRunes(got, 200), truncateRunes(want, 200))
	}
	if !strings.Contains(s.History[3].Content.Text, "[[elided id=") {
		t.Error("the control candidate was not compacted")
	}
	if report.MessagesScored != 1 || report.MessagesCompacted != 1 {
		t.Errorf("report = %+v, want only the control candidate scored and rewritten", report)
	}
	// The store holds only the control's elided run: the fixture pointer's
	// original was never re-stored.
	if store.Len() != 1 {
		t.Errorf("store holds %d records, want 1 (the control's run)", store.Len())
	}
	// A completing mutating run still advances the mark.
	if got, want := s.CompactionHighWater(), len(fixture); got != want {
		t.Errorf("high-water = %d, want %d", got, want)
	}
}

// (Step 14d) Shadow runs report only: nothing was rewritten, so the mark
// stays where it was and the next mutating run still walks the whole work
// area.
func TestCompactContextShadowDoesNotAdvanceHighWater(t *testing.T) {
	fixture := defaultFixture()
	s := newCompactSession(fixture)
	segs := fixtureSegments(t, fixture)

	report, err := s.CompactContext(context.Background(), elideFirstScorer(segs), NewCompactStore(), CompactionOptions{ShadowOnly: true})
	if err != nil {
		t.Fatalf("shadow CompactContext() error = %v", err)
	}
	if report.TokensSaved <= 0 || report.MessagesCompacted == 0 {
		t.Fatalf("shadow report not populated: %+v", report)
	}
	if got := s.CompactionHighWater(); got != 0 {
		t.Fatalf("shadow run advanced the high-water mark to %d, want 0", got)
	}
	if got, want := mustJSON(t, s.History), mustJSON(t, fixture); got != want {
		t.Fatal("shadow run mutated history")
	}

	second, err := s.CompactContext(context.Background(), elideFirstScorer(segs), NewCompactStore(), CompactionOptions{})
	if err != nil {
		t.Fatalf("mutating CompactContext() error = %v", err)
	}
	if second.MessagesScanned != len(fixture)-3 {
		t.Errorf("mutating run MessagesScanned = %d, want %d (the shadow run froze nothing)", second.MessagesScanned, len(fixture)-3)
	}
	if got, want := s.CompactionHighWater(), len(fixture); got != want {
		t.Errorf("mutating run high-water = %d, want %d", got, want)
	}
}

// (Step 14d) A mid-walk abort (wholesale scorer failure) does not advance
// the mark: the walk never reached the end of history.
func TestCompactContextMidWalkAbortKeepsHighWater(t *testing.T) {
	fixture := []client.ChatMessage{
		cmpStamped(cmpSystem("system prompt"), 0),
		cmpStamped(cmpUser("first question"), 1),
		cmpStamped(cmpAssistant("first answer"), 2),
		cmpStamped(cmpAssistantWithCalls(cmpLongText("c1", 3), []client.ToolCall{cmpToolCall("call_m1")}), 3),
		cmpStamped(cmpTool(cmpLongText("c2", 3)), 4),
		cmpStamped(cmpAssistantWithCalls(cmpLongText("c3", 3), []client.ToolCall{cmpToolCall("call_m3")}), 5),
	}
	s := newCompactSession(fixture)
	scorer := elideFirstScorer(fixtureSegments(t, fixture))
	scorer.err = errors.New("scorer down")
	scorer.errOnCall = 3 // wholesale failure on the third candidate

	if _, err := s.CompactContext(context.Background(), scorer, NewCompactStore(), CompactionOptions{}); err == nil {
		t.Fatal("CompactContext() error = nil, want the mid-walk failure")
	}
	if got := s.CompactionHighWater(); got != 0 {
		t.Errorf("high-water after the abort = %d, want 0", got)
	}
}

// (Step 14e) The reset paths adjust the mark: /new zeroes it (a fresh
// conversation has no frozen prefix) and PopLastUserMessage clamps it to the
// truncated length (the frozen prefix never outlives the history it froze) —
// persisted by the pop's own metadata write.
func TestCompactionHighWaterResetPaths(t *testing.T) {
	t.Run("/new resets the mark to zero", func(t *testing.T) {
		tmpDir := t.TempDir()
		oldSessionDir := SessionDir
		SessionDir = func() (string, error) { return tmpDir, nil }
		defer func() { SessionDir = oldSessionDir }()

		s := New(nil, filepath.Join(tmpDir, "session-hw-new.json"), defaultFixture(), "", false)
		s.SetCompactionHighWater(12)
		if err := s.StartNewConversation(); err != nil {
			t.Fatalf("StartNewConversation() error = %v", err)
		}
		if got := s.CompactionHighWater(); got != 0 {
			t.Errorf("high-water after /new = %d, want 0", got)
		}
		// The fresh conversation's sidecar records the reset mark.
		if got := s.GenerateSessionMeta().CompactionHighWater; got != 0 {
			t.Errorf("fresh session meta CompactionHighWater = %d, want 0", got)
		}
	})

	t.Run("PopLastUserMessage clamps and persists the mark", func(t *testing.T) {
		tmpDir := t.TempDir()
		oldSessionDir := SessionDir
		SessionDir = func() (string, error) { return tmpDir, nil }
		defer func() { SessionDir = oldSessionDir }()

		historyPath := filepath.Join(tmpDir, "session-hw-pop.json")
		fixture := []client.ChatMessage{
			cmpStamped(cmpSystem("system prompt"), 0),
			cmpStamped(cmpAssistant("answer"), 1),
			cmpStamped(cmpUser("last question"), 2),
		}
		if err := SaveHistory(historyPath, fixture); err != nil {
			t.Fatalf("SaveHistory() error = %v", err)
		}
		s := New(nil, historyPath, cloneHistory(fixture), "", false)
		s.SetCompactionHighWater(len(fixture))

		popped, err := s.PopLastUserMessage()
		if err != nil || !popped {
			t.Fatalf("PopLastUserMessage() = (%v, %v), want (true, nil)", popped, err)
		}
		if got, want := s.CompactionHighWater(), len(fixture)-1; got != want {
			t.Errorf("high-water after pop = %d, want %d", got, want)
		}
		meta, err := LoadSessionMeta("session-hw-pop")
		if err != nil || meta == nil {
			t.Fatalf("LoadSessionMeta(session-hw-pop) = (%v, %v)", meta, err)
		}
		if meta.CompactionHighWater != len(fixture)-1 {
			t.Errorf("persisted CompactionHighWater after pop = %d, want %d", meta.CompactionHighWater, len(fixture)-1)
		}
	})
}

// authScorer stands in for a decision backend that rejected the API key: it
// returns the typed compaction auth error wrapped exactly the way
// compaction.DecisionClient.ScoreBatch wraps one (an errors.Join of
// *ItemScoreError values around a *compaction.Error) alongside a complete
// fail-open score map — the shape the poisoned client synthesizes.
type authScorer struct {
	calls int
}

func (a *authScorer) ScoreBatch(_ context.Context, _ string, items map[string]compaction.Item) (map[string]float64, error) {
	a.calls++
	scores := make(map[string]float64, len(items))
	errs := make([]error, 0, len(items))
	for id := range items {
		scores[id] = keepScoreFallback
		errs = append(errs, &compaction.ItemScoreError{
			ItemID: id,
			Err: &compaction.Error{
				Kind:   compaction.KindAuth,
				Status: 401,
				Op:     "score-batch",
				Err:    errors.New("bad or missing API key"),
			},
		})
	}
	return scores, errors.Join(errs...)
}

// TestCompactContextAuthErrorStopsWalk: a typed auth failure (the reference's
// JevAuthError — 401/403, a bad or missing API key) ends the walk on the
// first message: no later message can score either, so the walk must not
// fire one doomed scorer call per message. The error keeps the typed class
// for the caller, and the report says how far the walk got.
func TestCompactContextAuthErrorStopsWalk(t *testing.T) {
	fixture := defaultFixture()
	s := newCompactSession(fixture)
	scorer := &authScorer{}

	report, err := s.CompactContext(context.Background(), scorer, NewCompactStore(), CompactionOptions{})
	if err == nil {
		t.Fatal("CompactContext() error = nil, want the typed auth error")
	}

	// The walk stopped on the first candidate (index 4, after the 3-message
	// frozen prefix): exactly one scorer call, nothing scored, nothing
	// rewritten, nothing advanced.
	if scorer.calls != 1 {
		t.Errorf("scorer calls = %d, want 1 (auth stops the walk immediately)", scorer.calls)
	}
	if report.MessagesScored != 0 {
		t.Errorf("MessagesScored = %d, want 0", report.MessagesScored)
	}
	if report.MessagesCompacted != 0 || report.SegmentsElided != 0 {
		t.Errorf("an aborted walk must rewrite nothing, got %+v", report)
	}
	if !strings.Contains(err.Error(), "stopped after 0 messages") {
		t.Errorf("error %v should say the walk stopped after 0 messages", err)
	}
	var ae *compaction.Error
	if !errors.As(err, &ae) || ae.Kind != compaction.KindAuth {
		t.Errorf("error = %v, want a *compaction.Error of KindAuth through the join", err)
	}
	if s.CompactionHighWater() != 0 {
		t.Errorf("CompactionHighWater = %d, want 0 (a mid-walk abort must not advance the mark)", s.CompactionHighWater())
	}
}

// --- Priority fixes: prose preservation, skill preservation, atomicity -------

// (P3) A pure-prose assistant message — no tool calls — is the
// conversation's narrative and must stay byte-identical through a mutating
// run, even when the scorer scores everything 0.0. Only the tool result
// (the recoverable work output) is compacted.
func TestCompactContextPreservesProseAssistant(t *testing.T) {
	prose := "## Plan\n\n" + cmpLongText("prose", 6)
	fixture := []client.ChatMessage{
		cmpStamped(cmpSystem("system prompt"), 0),
		cmpStamped(cmpUser("make a plan"), 1),
		cmpStamped(cmpAssistant(prose), 2),             // pure prose — never compacted
		cmpStamped(cmpTool(cmpLongText("beta", 3)), 3), // control candidate
		cmpStamped(cmpUser("go"), 4),
	}
	s := newCompactSession(fixture)
	scorer := &stubScorer{fallback: 0.1} // everything scored would be elided

	report, err := s.CompactContext(context.Background(), scorer, NewCompactStore(), CompactionOptions{})
	if err != nil {
		t.Fatalf("CompactContext() error = %v", err)
	}

	if got := s.History[2].Content.Text; got != prose {
		t.Fatalf("the pure-prose assistant message was compacted:\n got %q\nwant %q",
			truncateRunes(got, 200), truncateRunes(prose, 200))
	}
	// It was never even scored: only the tool result reached the scorer.
	if scorer.calls != 1 {
		t.Errorf("scorer calls = %d, want 1 (prose assistants are not candidates)", scorer.calls)
	}
	if report.MessagesScored != 1 {
		t.Errorf("MessagesScored = %d, want 1", report.MessagesScored)
	}
	// The control tool result was compacted as usual.
	if !strings.Contains(s.History[3].Content.Text, "[[elided id=") {
		t.Error("the tool-result control was not compacted")
	}
}

// (P4) A tool result originating from the activate_skill tool is the skill's
// instructions — what the agent was told to follow — and must never be
// elided: the walk skips it before segmentation, so it is byte-identical and
// never scored, while a control result from another tool compacts normally.
func TestCompactContextPreservesActivateSkillResults(t *testing.T) {
	skillResult := "Skill instructions:\n" + cmpLongText("skill", 6)
	control := cmpLongText("bash", 6)
	fixture := []client.ChatMessage{
		cmpStamped(cmpSystem("system prompt"), 0),
		cmpStamped(cmpUser("use the skill"), 1),
		cmpStamped(cmpAssistantWithCalls("", []client.ToolCall{
			{Index: 0, ID: "call_s", Type: "function", Function: client.FunctionCall{Name: "activate_skill", Arguments: `{"name":"demo"}`}},
			{Index: 1, ID: "call_b", Type: "function", Function: client.FunctionCall{Name: "Bash", Arguments: `{"cmd":"ls"}`}},
		}), 2),
		cmpStamped(cmpToolResult("call_s", skillResult), 3), // skill result — never compacted
		cmpStamped(cmpToolResult("call_b", control), 4),     // control — compacted
	}
	s := newCompactSession(fixture)
	scorer := &stubScorer{fallback: 0.0} // everything scored would be elided

	report, err := s.CompactContext(context.Background(), scorer, NewCompactStore(), CompactionOptions{})
	if err != nil {
		t.Fatalf("CompactContext() error = %v", err)
	}

	if got := s.History[3].Content.Text; got != skillResult {
		t.Fatalf("the activate_skill result was compacted:\n got %q\nwant %q",
			truncateRunes(got, 200), truncateRunes(skillResult, 200))
	}
	// Only the control reached the scorer.
	if scorer.calls != 1 {
		t.Errorf("scorer calls = %d, want 1 (protected tool results are never scored)", scorer.calls)
	}
	if report.MessagesScored != 1 {
		t.Errorf("MessagesScored = %d, want 1", report.MessagesScored)
	}
	if !strings.Contains(s.History[4].Content.Text, "[[elided id=") {
		t.Error("the control tool result was not compacted")
	}
}

// (P2) History-surface paragraph atomicity: a >2x maxSegChars JSON paragraph
// cut into pieces by splitOversized elides as ONE unit when a single piece
// scores below the floor — the pointer references the full original and
// Reconstruct restores it byte for byte — and stays byte-identical when all
// pieces score above the floor.
func TestCompactContextParagraphAtomicity(t *testing.T) {
	build := func() (*Session, []client.ChatMessage) {
		// One pretty-printed JSON object, no blank lines: one oversized
		// paragraph splitOversized cuts mid-structure.
		var b strings.Builder
		b.WriteString("{\n")
		for i := 0; i < 60; i++ {
			fmt.Fprintf(&b, "  \"key_%03d\": \"value %03d with some padding text to bulk the line past trivial lengths\",\n", i, i)
		}
		b.WriteString("  \"final\": true\n}")
		blob := b.String()
		if len(blob) <= 2*minCompactChars {
			t.Fatalf("fixture too small: %d bytes, want > %d", len(blob), 2*minCompactChars)
		}
		fixture := []client.ChatMessage{
			cmpStamped(cmpSystem("system prompt"), 0),
			cmpStamped(cmpUser("parse this"), 1),
			cmpStamped(cmpToolResult("call_x", blob), 2),
		}
		return newCompactSession(fixture), fixture
	}
	segment := func(t *testing.T, blob string) []compaction.Segment {
		t.Helper()
		segs := compaction.SegmentSegments(blob, minCompactChars)
		if len(segs) < 3 {
			t.Fatalf("SegmentSegments() = %d pieces, want ≥3", len(segs))
		}
		for _, seg := range segs {
			if seg.Group != segs[0].Group {
				t.Fatal("fixture sanity: cut pieces do not share a group")
			}
		}
		return segs
	}

	t.Run("one low piece elides the whole paragraph", func(t *testing.T) {
		s, fixture := build()
		blob := fixture[2].Content.Text
		segs := segment(t, blob)

		// Only the middle piece scores below the floor; without atomicity
		// eliding it would leave an unparseable JSON remnant.
		scores := map[string]float64{}
		for _, seg := range segs {
			scores[seg.Text] = 0.9
		}
		scores[segs[1].Text] = 0.1
		scorer := &stubScorer{scores: scores, fallback: 0.9}
		store := compaction.NewStore()

		report, err := s.CompactContext(context.Background(), scorer, store, CompactionOptions{})
		if err != nil {
			t.Fatalf("CompactContext() error = %v", err)
		}

		compacted := s.History[2].Content.Text
		pointers := compaction.FindPointers(compacted)
		if len(pointers) != 1 {
			t.Fatalf("FindPointers(compacted) = %d pointers, want 1 (the whole paragraph)", len(pointers))
		}
		if report.SegmentsElided != len(segs) {
			t.Errorf("SegmentsElided = %d, want all %d pieces", report.SegmentsElided, len(segs))
		}
		rec, ok := store.GetRecord(pointers[0].ID)
		if !ok {
			t.Fatalf("store holds no record for %s", pointers[0].ID)
		}
		if rec.Text != blob {
			t.Error("the pointer's record is not the FULL original paragraph")
		}
		if restored := compaction.Reconstruct(compacted, store); restored != blob {
			t.Error("Reconstruct(compacted) is not byte-for-byte")
		}
	})

	t.Run("all pieces above the floor keep the message", func(t *testing.T) {
		s, fixture := build()
		blob := fixture[2].Content.Text
		segment(t, blob)

		scorer := &stubScorer{fallback: 0.9}
		report, err := s.CompactContext(context.Background(), scorer, compaction.NewStore(), CompactionOptions{})
		if err != nil {
			t.Fatalf("CompactContext() error = %v", err)
		}
		if s.History[2].Content.Text != blob {
			t.Error("a message whose pieces all stay above the floor must be kept byte-for-byte")
		}
		if report.MessagesCompacted != 0 || report.SegmentsElided != 0 {
			t.Errorf("expected a no-op report, got %+v", report)
		}
	})
}

// (P9) The full transient-outage story, pinned end to end with the REAL
// decision client: the scorer endpoint 500s forever → the client spends its
// full 4-attempt retry budget, the walk FAILS OPEN (completes with everything
// kept), the scorer's error surfaces, and the session stays usable — the
// client is not poisoned, so a later scoring call still reaches the server.
func TestCompactContextTransientOutageCompletesWithEverythingKept(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error": {"message": "overloaded"}}`)
	}))
	t.Cleanup(srv.Close)

	scorer := compaction.NewDecisionClient(
		compaction.ResolvedBackend{Backend: compaction.Backend{Name: "test", URL: srv.URL, Model: "jev-latest"}, APIKey: "k"},
		"k")

	fixture := []client.ChatMessage{
		cmpStamped(cmpSystem("system prompt"), 0),
		cmpStamped(cmpUser("run the thing"), 1),
		cmpStamped(cmpToolResult("call_out", cmpLongText("outage", 3)), 2), // one candidate
		cmpStamped(cmpUser("next"), 3),
	}
	s := newCompactSession(fixture)

	report, err := s.CompactContext(context.Background(), scorer, NewCompactStore(), CompactionOptions{})
	if err == nil {
		t.Fatal("CompactContext() error = nil, want the scorer's outage errors surfaced")
	}

	// The full retry budget was spent, then fail-open kept everything.
	if got := atomic.LoadInt32(&hits); got != 4 {
		t.Errorf("server hits = %d, want 4 (full attempt budget)", got)
	}
	if report.MessagesScored != 1 {
		t.Errorf("MessagesScored = %d, want 1 (the fail-open scores completed the walk)", report.MessagesScored)
	}
	if report.MessagesCompacted != 0 || report.SegmentsElided != 0 {
		t.Errorf("an outage must keep everything, got %+v", report)
	}
	if got, want := s.History[2].Content.Text, fixture[2].Content.Text; got != want {
		t.Error("the outage mutated the tool result")
	}
	// The walk completed: the mark advanced, so the session stays coherent.
	if got := s.CompactionHighWater(); got != len(fixture) {
		t.Errorf("high-water = %d, want %d (the walk completed)", got, len(fixture))
	}
	// The session is usable: an outage is transient, not a poison — the
	// client still reaches the server on the next scoring call.
	if scorer.Unavailable() {
		t.Error("the client was poisoned by a 500 outage; only auth may poison")
	}
	before := atomic.LoadInt32(&hits)
	if _, err := scorer.ScoreBatch(context.Background(), "still alive", map[string]compaction.Item{"seg-1": {Text: "x", Tokens: 1}}); err == nil {
		t.Error("the server is still down; want an error")
	}
	if got := atomic.LoadInt32(&hits) - before; got != 4 {
		t.Errorf("post-outage call hit the server %d times, want 4 (the session still scores)", got)
	}
}
