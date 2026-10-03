package compaction

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// relocationOutput builds a three-paragraph tool output whose segments stay
// separate (each well over defaultMinSegChars, well under
// DefaultMaxSegChars). Every paragraph carries its marker after the first
// 100 characters, so a pointer's 120-char summary can show the marker's
// first characters but never the whole paragraph body (its distinctive
// tail).
func relocationOutput() (output, keeper1, filler, keeper2 string) {
	keeper1 = strings.Repeat("k", 100) + "KEEPER-ONE-MARK" + strings.Repeat("1", 40)
	filler = strings.Repeat("f", 100) + "FILLER-SECRET-MARK" + strings.Repeat("2", 40)
	keeper2 = strings.Repeat("j", 100) + "KEEPER-TWO-MARK" + strings.Repeat("3", 40)
	return keeper1 + "\n\n" + filler + "\n\n" + keeper2, keeper1, filler, keeper2
}

// boundaryScores scores seg-1 exactly at the threshold, seg-2 below it, and
// seg-3 above it.
func boundaryScores(threshold float64) map[string]float64 {
	return map[string]float64{
		"seg-1": threshold, // exactly at: stays (strictly-below comparison)
		"seg-2": 0.1,       // below: elided
		"seg-3": 0.9,       // above: stays
	}
}

// applyTestGate applies a gate config sized for the small relocation
// fixtures: they are a few hundred bytes (a few dozen tokens), far below the
// reference DefaultGateConfig's 400-token min-gate, so the token gate is
// disabled and the keep threshold pinned explicitly. maxElide caps the
// elide-fraction tripwire (1 disables it, since elided tokens can never
// exceed the total), so fixtures that elide everything still exercise the
// pointer path they were written for.
func applyTestGate(p *Pipeline, keepThreshold, maxElide float64) {
	p.ApplyGateConfig(GateConfig{
		KeepThreshold:    keepThreshold,
		MinGateTokens:    0,
		MaxElideFraction: maxElide,
	})
}

// TestPipeline_EnableRelocationThresholdBoundary pins the relocation
// contract: the segment scoring exactly at the threshold stays, the one
// below it is elided into the store under a content id, the high scorer
// stays, and the compact text keeps the kept segments in place with the
// pointer standing where the run stood — so Reconstruct is byte-for-byte.
func TestPipeline_EnableRelocationThresholdBoundary(t *testing.T) {
	const threshold = 0.35
	d := newDecisionsServer(t, fixedScoresHandler(boundaryScores(threshold)))
	store := NewStore()

	p := NewPipeline(ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}, "k", nil, PipelineOptions{})
	p.EnableRelocation(store, threshold)
	applyTestGate(p, threshold, 0.7)

	output, keeper1, filler, keeper2 := relocationOutput()
	got, err := p.CompactToolOutput(context.Background(), "Bash", output)
	if err != nil {
		t.Fatalf("CompactToolOutput() error = %v", err)
	}

	// The stored original is the full segment span (a segment absorbs its
	// trailing blank-line separator, so spans tile the input).
	segs := SegmentSegments(output, 0)
	if len(segs) != 3 {
		t.Fatalf("SegmentSegments() = %d segments, want 3", len(segs))
	}
	fillerSpan := segs[1].Text

	if len(got.Elided) != 1 {
		t.Fatalf("Elided = %d segments, want exactly the below-threshold one", len(got.Elided))
	}
	e := got.Elided[0]
	// Content-addressed id: ContentID of the run text, salted with the tool
	// name, "r"-prefixed — 8 lowercase hex chars.
	if want := ContentID(fillerSpan, "Bash", "r"); e.ID != want {
		t.Errorf("elided id = %q, want the content id %q", e.ID, want)
	}
	if !regexp.MustCompile(`^r:[0-9a-f]{8}$`).MatchString(e.ID) {
		t.Errorf("elided id = %q, want the r:<8hex> shape", e.ID)
	}
	if e.Text != fillerSpan {
		t.Errorf("elided text = %q, want the filler segment span %q", e.Text, fillerSpan)
	}
	if !strings.Contains(e.Text, filler) {
		t.Errorf("elided text %q lost the filler paragraph", e.Text)
	}
	// Lines: the filler occupies original line 3, plus its trailing blank
	// separator on line 4.
	if e.Lines != [2]int{3, 4} {
		t.Errorf("elided lines = %v, want [3 4]", e.Lines)
	}
	if e.Segments != 1 {
		t.Errorf("elided segments = %d, want 1", e.Segments)
	}
	if want := Summarise(fillerSpan, SummaryMaxChars); e.Summary != want {
		t.Errorf("elided summary = %q, want %q", e.Summary, want)
	}
	if e.Summary == "" || strings.ContainsAny(e.Summary, "\n\r\t") {
		t.Errorf("elided summary = %q, want a flat one-liner", e.Summary)
	}
	// The summary is a 120-char cut plus the ellipsis rune.
	if len([]rune(e.Summary)) > SummaryMaxChars+1 {
		t.Errorf("elided summary = %d runes, want ≤%d (cut + ellipsis)",
			len([]rune(e.Summary)), SummaryMaxChars+1)
	}

	// The store round-trips the original, keyed by the content id.
	if text, ok := store.Get(e.ID); !ok || text != fillerSpan {
		t.Errorf("store.Get(%s) = (%q, %v), want the filler original", e.ID, text, ok)
	}
	if store.Len() != 1 {
		t.Errorf("store.Len() = %d, want exactly the one elided record", store.Len())
	}

	// Compact text: the kept segments in place, the pointer standing where
	// the run stood (after keeper-1's span, before keeper-2's).
	if !strings.HasPrefix(got.CompactText, keeper1+"\n\n") {
		t.Errorf("CompactText must start with the first kept segment, got:\n%s", got.CompactText)
	}
	if !strings.Contains(got.CompactText, keeper1) || !strings.Contains(got.CompactText, keeper2) {
		t.Errorf("CompactText lost a high-scoring segment:\n%s", got.CompactText)
	}
	// The full filler span (its 2-filled tail reaches past the 120-char
	// summary) must not appear; the summary legitimately shows the marker's
	// first characters.
	if strings.Contains(got.CompactText, filler) || strings.Contains(got.CompactText, strings.Repeat("2", 40)) {
		t.Errorf("CompactText leaked the elided segment's original:\n%s", got.CompactText)
	}
	pointerRe := regexp.MustCompile(`^\[\[elided id=r:[0-9a-f]{8} lines=3-4 tokens=[0-9]+ ".+"\]\]$`)
	pointerLine := "<missing>"
	pointerIdx := -1
	for i, line := range strings.Split(got.CompactText, "\n") {
		if strings.Contains(line, "[[elided ") {
			pointerLine = line
			pointerIdx = i
			break
		}
	}
	if !pointerRe.MatchString(pointerLine) {
		t.Errorf("CompactText pointer line %q not in the pinned reference format", pointerLine)
	}
	if lastLine := got.CompactText[strings.LastIndexByte(got.CompactText, '\n')+1:]; lastLine != keeper2 {
		t.Errorf("CompactText must end with the last kept segment, got %q", lastLine)
	}
	if pointerIdx != 2 { // keeper1's line, the blank separator, then the pointer
		t.Errorf("pointer line index = %d, want 2 (in place of the elided run)", pointerIdx)
	}

	// The byte-for-byte inverse: Reconstruct restores the original output.
	if restored := Reconstruct(got.CompactText, store); restored != output {
		t.Errorf("Reconstruct(compacted) mismatch:\n got %q\nwant %q", restored, output)
	}
}

// TestPipeline_CompactOutputAtAndAboveThresholdKeepsOriginalBytes: nothing
// below the threshold means the result passes through byte-for-byte (never
// reconstructed, which would drop leading blank bytes).
func TestPipeline_CompactOutputNothingElidedKeepsOriginalBytes(t *testing.T) {
	// Every segment scores 0.9, so nothing is elided.
	high := newDecisionsServer(t, func(_ int, req capturedRequest) (int, string) {
		scores := make(map[string]float64, len(req.Req.Questions))
		for ref := range req.Req.Questions {
			scores[ref] = 0.9
		}
		return http.StatusOK, answersBody(scores)
	})
	store := NewStore()
	p := NewPipeline(ResolvedBackend{Backend: Backend{Name: "test", URL: high.srv.URL, Model: "jev-latest"}, APIKey: "k"}, "k", nil, PipelineOptions{})
	p.EnableRelocation(store, 0.35)
	applyTestGate(p, 0.35, 0.7)

	output := strings.Repeat("keep me\n\n", 40)
	got, err := p.CompactToolOutput(context.Background(), "Bash", output)
	if err != nil {
		t.Fatalf("CompactToolOutput() error = %v", err)
	}
	if got.CompactText != output {
		t.Error("CompactText must equal the original byte-for-byte when nothing is elided")
	}
	if len(got.Elided) != 0 {
		t.Errorf("Elided = %v, want empty", got.Elided)
	}
	if _, ok := store.Get("elide-1"); ok {
		t.Error("store must stay empty when nothing is elided")
	}
}

// TestPipeline_CompactOutputAllElided: every segment below the threshold
// leaves only pointer lines behind — and the two consecutive elided
// segments form ONE run with a single record and pointer (the reference
// flush_run pattern). The gate's tripwire is disabled for this fixture:
// eliding everything is 100% of tokens, past the 0.7 default — the
// tripwire's own behavior is covered in gate_test.go.
func TestPipeline_CompactOutputAllElided(t *testing.T) {
	d := newDecisionsServer(t, fixedScoresHandler(map[string]float64{"seg-1": 0.1, "seg-2": 0.2}))
	store := NewStore()
	p := NewPipeline(ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}, "k", nil, PipelineOptions{})
	p.EnableRelocation(store, 0.35)
	applyTestGate(p, 0.35, 1)

	output := strings.Repeat("x", 120) + "\n\n" + strings.Repeat("y", 120)
	got, err := p.CompactToolOutput(context.Background(), "Bash", output)
	if err != nil {
		t.Fatalf("CompactToolOutput() error = %v", err)
	}
	if got.Tripwire != "" {
		t.Errorf("Tripwire = %q, want empty (tripwire disabled for this fixture)", got.Tripwire)
	}
	if len(got.Elided) != 1 {
		t.Fatalf("Elided = %d runs, want 1 (both segments are consecutive)", len(got.Elided))
	}
	run := got.Elided[0]
	if run.Segments != 2 {
		t.Errorf("run.Segments = %d, want the two grouped segments", run.Segments)
	}
	if run.Text != output {
		t.Errorf("run text = %q, want both segment spans concatenated", run.Text)
	}
	if run.Lines != [2]int{1, 3} { // x-run, blank separator, y-run
		t.Errorf("run lines = %v, want [1 3]", run.Lines)
	}
	if run.Tokens != SegmentSegments(output, 0)[0].Tokens+SegmentSegments(output, 0)[1].Tokens {
		t.Errorf("run tokens = %d, want the summed segment tokens", run.Tokens)
	}
	// The first line (exactly 120 x's) IS the summary — but the second
	// segment's y-run must be gone.
	if strings.Contains(got.CompactText, strings.Repeat("y", 120)) {
		t.Error("CompactText kept an elided segment's text")
	}
	// One pointer, one record.
	if strings.Count(got.CompactText, "[[elided ") != 1 {
		t.Errorf("CompactText must carry exactly one pointer:\n%s", got.CompactText)
	}
	if store.Len() != 1 {
		t.Errorf("store.Len() = %d, want the single run record", store.Len())
	}
	if text, ok := store.Get(run.ID); !ok || text != output {
		t.Errorf("store.Get(%s) = (%q, %v), want the whole run", run.ID, text, ok)
	}
	// The pointer replaced the whole output: reconstruct restores it.
	if restored := Reconstruct(got.CompactText, store); restored != output {
		t.Errorf("Reconstruct(compacted) = %q, want the original output", restored)
	}
}

// TestPipeline_CompactOutputFailOpenKeepsOriginal: any scoring error means
// the original result comes back unchanged (never break a tool call over
// compaction). A 400 is non-retryable so this fails fast; the 503 case
// shrinks the retry curve.
func TestPipeline_CompactOutputFailOpenKeepsOriginal(t *testing.T) {
	output, _, _, _ := relocationOutput()
	for _, tc := range []struct {
		name    string
		status  int
		shrink  bool
		wantErr bool
	}{
		{name: "permanent 400", status: http.StatusBadRequest},
		{name: "transient 503", status: http.StatusServiceUnavailable, shrink: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDecisionsServer(t, func(_ int, _ capturedRequest) (int, string) {
				return tc.status, `{"error": {"message": "down"}}`
			})
			store := NewStore()
			p := NewPipeline(ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}, "k", nil, PipelineOptions{})
			if tc.shrink {
				shrinkPipelineRetryCurve(p)
			}
			p.EnableRelocation(store, 0.35)
			applyTestGate(p, 0.35, 0.7)

			got, err := p.CompactToolOutput(context.Background(), "Bash", output)
			if tc.wantErr && err == nil {
				t.Fatal("CompactToolOutput() error = nil, want the outage recorded")
			}
			if got.CompactText != output {
				t.Errorf("fail-open must return the original text unchanged, got:\n%s", got.CompactText)
			}
			if len(got.Elided) != 0 {
				t.Errorf("Elided = %v, want empty on fail-open", got.Elided)
			}
			if store.Len() != 0 {
				t.Error("store must stay empty on fail-open")
			}
		})
	}
}

// TestPipeline_CompactWithoutRelocationIsShadow: an un-armed pipeline scores
// and logs but never mutates the result or the store — the shadow contract.
func TestPipeline_CompactWithoutRelocationIsShadow(t *testing.T) {
	d := newDecisionsServer(t, fixedScoresHandler(map[string]float64{"seg-1": 0.01, "seg-2": 0.02}))
	store := NewStore()
	p := NewPipeline(ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}, "k", nil, PipelineOptions{})
	applyTestGate(p, 0.35, 0.7)

	output := strings.Repeat("a", 120) + "\n\n" + strings.Repeat("b", 120)
	got, err := p.CompactToolOutput(context.Background(), "Bash", output)
	if err != nil {
		t.Fatalf("CompactToolOutput() error = %v", err)
	}
	if got.CompactText != output || len(got.Elided) != 0 {
		t.Errorf("shadow-only pipeline must not mutate the result, got %+v", got)
	}
	// Arming with a nil store disarms relocation again.
	p.EnableRelocation(nil, 0.35)
	got, err = p.CompactToolOutput(context.Background(), "Bash", output)
	if err != nil || got.CompactText != output {
		t.Errorf("disarmed pipeline must not mutate the result, got %+v, err %v", got, err)
	}
	if store.Len() != 0 {
		t.Error("store must stay empty without relocation")
	}
}

// TestPipeline_RelocationShadowLogDecisions: the shadow log records "elide"
// for below-threshold segments only when relocation is armed; shadow mode
// keeps recording "keep" for everything.
func TestPipeline_RelocationShadowLogDecisions(t *testing.T) {
	newArmed := func(armed bool) (*Pipeline, *ShadowLog) {
		d := newDecisionsServer(t, fixedScoresHandler(boundaryScores(0.35)))
		shadow, err := NewShadowLogAt(filepath.Join(t.TempDir(), "shadow.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		p := NewPipeline(ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}, "k", shadow, PipelineOptions{})
		applyTestGate(p, 0.35, 0.7)
		if armed {
			p.EnableRelocation(NewStore(), 0.35)
		}
		return p, shadow
	}

	output, _, _, _ := relocationOutput()

	t.Run("armed records elide", func(t *testing.T) {
		p, shadow := newArmed(true)
		if _, err := p.CompactToolOutput(context.Background(), "Bash", output); err != nil {
			t.Fatalf("CompactToolOutput() error = %v", err)
		}
		report, err := shadow.Replay(0.35)
		if err != nil {
			t.Fatalf("Replay() error = %v", err)
		}
		if report.Entries != 3 || report.ElidedEntries != 1 {
			t.Fatalf("Replay() = %+v, want 3 entries with 1 elided", report)
		}
		// Step 13: every decision entry carries the gate floor it was made
		// against (all fixtures are text segments, so the floor is the keep
		// threshold), letting replay re-decide from score vs threshold.
		lines := readLines(t, shadow.Path())
		if len(lines) != 3 {
			t.Fatalf("got %d shadow lines, want 3", len(lines))
		}
		for _, line := range lines {
			var e ShadowEntry
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Fatalf("shadow line invalid: %v", err)
			}
			if e.Threshold != 0.35 {
				t.Errorf("decision for %s threshold = %v, want the gate floor 0.35", e.SegmentID, e.Threshold)
			}
		}
	})

	t.Run("shadow records keep", func(t *testing.T) {
		p, shadow := newArmed(false)
		if _, err := p.CompactToolOutput(context.Background(), "Bash", output); err != nil {
			t.Fatalf("CompactToolOutput() error = %v", err)
		}
		report, err := shadow.Replay(0.35)
		if err != nil {
			t.Fatalf("Replay() error = %v", err)
		}
		if report.Entries != 3 || report.ElidedEntries != 1 {
			t.Fatalf("Replay() = %+v, want 3 entries with 1 below threshold", report)
		}
	})
}

// TestPipeline_ContentIDsDeterministicAcrossCalls: pointer ids are
// content-addressed, so the same run text mints the same id (and reuses the
// same store record) across calls, while different texts mint different
// ids. The legacy counter stays untouched for old pointers.
func TestPipeline_ContentIDsDeterministicAcrossCalls(t *testing.T) {
	d := newDecisionsServer(t, echoHandler) // seg-N scores 0.01*N: all below 0.35
	store := NewStore()
	p := NewPipeline(ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}, "k", nil, PipelineOptions{})
	p.EnableRelocation(store, 0.35)
	applyTestGate(p, 0.35, 1)

	ctx := context.Background()
	output := strings.Repeat("a", 120) + "\n\n" + strings.Repeat("b", 120)
	first, err := p.CompactToolOutput(ctx, "Bash", output)
	if err != nil {
		t.Fatalf("first CompactToolOutput() error = %v", err)
	}
	again, err := p.CompactToolOutput(ctx, "Bash", output)
	if err != nil {
		t.Fatalf("second CompactToolOutput() error = %v", err)
	}
	other, err := p.CompactToolOutput(ctx, "Bash", strings.Repeat("c", 120)+"\n\n"+strings.Repeat("d", 120))
	if err != nil {
		t.Fatalf("third CompactToolOutput() error = %v", err)
	}

	// Two consecutive segments per output: one run each.
	if len(first.Elided) != 1 || len(again.Elided) != 1 || len(other.Elided) != 1 {
		t.Fatalf("Elided runs = %d/%d/%d, want one run per output",
			len(first.Elided), len(again.Elided), len(other.Elided))
	}

	// Same run text → same id, always: re-compaction reuses the record.
	if first.Elided[0].ID != again.Elided[0].ID {
		t.Errorf("same run text minted different ids: %q vs %q", first.Elided[0].ID, again.Elided[0].ID)
	}
	if first.Elided[0].ID != ContentID(output, "Bash", "r") {
		t.Errorf("run id = %q, want ContentID(runText, salt=toolName)", first.Elided[0].ID)
	}
	// Different text → different id.
	if first.Elided[0].ID == other.Elided[0].ID {
		t.Errorf("different run texts shared the id %q", first.Elided[0].ID)
	}
	// All runs are retrievable, and idempotent Put kept exactly two records.
	for _, e := range []ElidedSegment{first.Elided[0], other.Elided[0]} {
		if _, ok := store.Get(e.ID); !ok {
			t.Errorf("store missing %s", e.ID)
		}
	}
	if store.Len() != 2 {
		t.Errorf("store.Len() = %d, want 2 (identical content shares one record)", store.Len())
	}
}

// TestPipeline_CompactToolResultTrailer pins the executor-facing string
// form: compact text plus the expand-tool trailer, original on fail-open.
func TestPipeline_CompactToolResultTrailer(t *testing.T) {
	output, keeper1, filler, keeper2 := relocationOutput()
	d := newDecisionsServer(t, fixedScoresHandler(boundaryScores(0.35)))
	store := NewStore()
	p := NewPipeline(ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}, "k", nil, PipelineOptions{})
	p.EnableRelocation(store, 0.35)
	applyTestGate(p, 0.35, 0.7)

	got := p.CompactToolResult(context.Background(), "Bash", output)
	// The trailer names the run's content id.
	segs := SegmentSegments(output, 0)
	runID := ContentID(segs[1].Text, "Bash", "r")
	for _, want := range []string{
		keeper1, keeper2,
		"1 segments elided — use the expand tool with the elided ids to retrieve originals (" + runID + ").",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("CompactToolResult() missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, filler) || strings.Contains(got, strings.Repeat("2", 40)) {
		t.Errorf("CompactToolResult() leaked the elided original:\n%s", got)
	}

	// Nothing elided → byte-identical, no trailer.
	high := newDecisionsServer(t, fixedScoresHandler(map[string]float64{"seg-1": 0.9, "seg-2": 0.9, "seg-3": 0.9}))
	p2 := NewPipeline(ResolvedBackend{Backend: Backend{Name: "test", URL: high.srv.URL, Model: "jev-latest"}, APIKey: "k"}, "k", nil, PipelineOptions{})
	p2.EnableRelocation(store, 0.35)
	applyTestGate(p2, 0.35, 0.7)
	if got := p2.CompactToolResult(context.Background(), "Bash", output); got != output {
		t.Errorf("CompactToolResult() with nothing elided must return the original, got:\n%s", got)
	}
}

// TestPipeline_EnableRelocationClampsThreshold: out-of-range thresholds fall
// back to DefaultRelocationThreshold instead of eliding everything or
// nothing by accident.
func TestPipeline_EnableRelocationClampsThreshold(t *testing.T) {
	d := newDecisionsServer(t, fixedScoresHandler(map[string]float64{"seg-1": 0.3}))
	output := strings.Repeat("a", 120)

	for _, bad := range []float64{0, -1, 1.5} {
		store := NewStore()
		p := NewPipeline(ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}, "k", nil, PipelineOptions{})
		p.EnableRelocation(store, bad)
		// The invalid keep threshold clamps to "unset" (the armed threshold
		// stays in force) and the tripwire is disabled so the single
		// all-eliding fixture elides normally.
		applyTestGate(p, bad, 1)
		got, err := p.CompactToolOutput(context.Background(), "Bash", output)
		if err != nil {
			t.Fatalf("threshold %v: CompactToolOutput() error = %v", bad, err)
		}
		// Every out-of-range threshold clamps to DefaultRelocationThreshold,
		// under which the 0.3-scoring segment is elided (a raw threshold of
		// 0 would keep everything; 1.5 would be nonsense).
		if len(got.Elided) != 1 {
			t.Errorf("threshold %v: Elided = %d, want the default-threshold elision", bad, len(got.Elided))
		}
	}
}

// TestStore_GetRoundTripAndMisses covers the store's read side and Put's
// idempotency: an existing id keeps its first record, so re-storing
// identical content never duplicates or overwrites (content ids hash the
// text, so two records under one id would be indistinguishable anyway).
func TestStore_GetRoundTripAndMisses(t *testing.T) {
	var nilStore *Store
	if _, ok := nilStore.Get("elide-1"); ok {
		t.Error("nil store must report a miss")
	}
	if nilStore.Len() != 0 {
		t.Error("nil store must be empty")
	}

	s := NewStore()
	if _, ok := s.Get("elide-1"); ok {
		t.Error("empty store must report a miss")
	}
	s.Put("elide-1", "original text")
	if text, ok := s.Get("elide-1"); !ok || text != "original text" {
		t.Errorf("Get(elide-1) = (%q, %v), want the stored original", text, ok)
	}
	s.Put("elide-1", "replaced")
	if text, _ := s.Get("elide-1"); text != "original text" {
		t.Errorf("Get(elide-1) = %q, want the first record kept (idempotent Put)", text)
	}
	if s.Len() != 1 {
		t.Errorf("Len() = %d, want 1 after an idempotent re-put", s.Len())
	}
	// Legacy counter ids still mint (backward compatibility of old stores).
	if got := s.NextID(); got != "elide-1" {
		t.Errorf("NextID() = %q, want elide-1", got)
	}
	if got := s.NextID(); got != "elide-2" {
		t.Errorf("NextID() = %q, want elide-2", got)
	}
	s.Put("elide-2", "legacy original")
	if text, ok := s.Get("elide-2"); !ok || text != "legacy original" {
		t.Errorf("Get(elide-2) = (%q, %v), want the legacy original", text, ok)
	}
}

// TestPipeline_CompactReconstructRoundTrip is the Step 11 round-trip pin:
// an output whose segments carry quotes, backslashes, and newlines goes
// through compact (enabled mode, stub scorer) and comes back through
// Reconstruct BYTE FOR BYTE — the pointer lines (with their escaped
// summaries) are exact inverses of the runs they replaced.
func TestPipeline_CompactReconstructRoundTrip(t *testing.T) {
	// Five paragraphs: keep, elide, keep, elide, keep. The elided ones start
	// with quotes and backslashes (exercising summary escaping) and span
	// multiple lines (exercising stored newlines); every paragraph clears
	// the 80-byte tiny-paragraph floor and stays under the segment cap.
	pad := func(n int) string { return strings.Repeat("pad ", n) }
	para1 := `intro "quoted" and \backslash\ padding ` + pad(13)
	para2 := `secret one starts "with quotes" and \ a backslash` + "\n" +
		"secret one line two\nsecret one line three\n" + pad(6)
	para3 := `middle keeper "with quotes" too ` + pad(12)
	para4 := `secret two \ ends its first line oddly` + "\n" +
		"secret two line two\n" + pad(8)
	para5 := `final keeper "quoted end" ` + pad(14)
	output := para1 + "\n\n" + para2 + "\n\n" + para3 + "\n\n" + para4 + "\n\n" + para5

	segs := SegmentSegments(output, 0)
	if len(segs) != 5 {
		t.Fatalf("SegmentSegments() = %d segments, want 5", len(segs))
	}

	d := newDecisionsServer(t, fixedScoresHandler(map[string]float64{
		"seg-1": 0.9, "seg-2": 0.05, "seg-3": 0.9, "seg-4": 0.05, "seg-5": 0.9,
	}))
	store := NewStore()
	p := NewPipeline(ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}, "k", nil, PipelineOptions{})
	p.EnableRelocation(store, 0.35)
	applyTestGate(p, 0.35, 1)

	got, err := p.CompactToolOutput(context.Background(), "Bash", output)
	if err != nil {
		t.Fatalf("CompactToolOutput() error = %v", err)
	}
	if len(got.Elided) != 2 {
		t.Fatalf("Elided = %d runs, want exactly the two secret paragraphs", len(got.Elided))
	}

	// Each run's record holds its multi-line original verbatim.
	for i, e := range got.Elided {
		want := segs[2*i+1].Text
		if e.Text != want {
			t.Errorf("run %d text = %q, want the segment span %q", i, e.Text, want)
		}
		stored, ok := store.Get(e.ID)
		if !ok || stored != want {
			t.Errorf("store.Get(%s) = (%q, %v), want the verbatim multi-line original", e.ID, stored, ok)
		}
	}
	if got.Elided[0].ID == got.Elided[1].ID {
		t.Errorf("distinct runs shared the id %q", got.Elided[0].ID)
	}
	if store.Len() != 2 {
		t.Errorf("store.Len() = %d, want 2", store.Len())
	}

	// The compact text names both pointers with escaped summaries and hides
	// the secrets' bodies; the kept paragraphs (quotes and all) survive.
	if strings.Contains(got.CompactText, "secret one line two") ||
		strings.Contains(got.CompactText, "secret two line two") {
		t.Errorf("CompactText leaked an elided run's body:\n%s", got.CompactText)
	}
	for _, keeper := range []string{para1, para3, para5} {
		if !strings.Contains(got.CompactText, keeper) {
			t.Errorf("CompactText lost a kept paragraph:\n%s", got.CompactText)
		}
	}
	pointers := FindPointers(got.CompactText)
	if len(pointers) != 2 {
		t.Fatalf("FindPointers(compacted) = %d, want the 2 pointers back", len(pointers))
	}
	for i, p := range pointers {
		if p.ID != got.Elided[i].ID {
			t.Errorf("pointer[%d].ID = %q, want the run's id %q", i, p.ID, got.Elided[i].ID)
		}
		if p.Lines == nil || *p.Lines != got.Elided[i].Lines {
			t.Errorf("pointer[%d] lines = %v, want the run's %v", i, p.Lines, got.Elided[i].Lines)
		}
	}

	// THE guarantee: reconstruct is the byte-for-byte inverse.
	if restored := Reconstruct(got.CompactText, store); restored != output {
		t.Errorf("Reconstruct(compacted) is not byte-for-byte:\n got %q\nwant %q", restored, output)
	}
}

// TestStore_ConcurrentAccess exercises the mutex under -race: one pipeline
// is shared by all agents, whose tool calls run concurrently.
func TestStore_ConcurrentAccess(t *testing.T) {
	s := NewStore()
	var wg sync.WaitGroup
	for i := 1; i <= 64; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := fmt.Sprintf("elide-%d", n)
			s.Put(id, strings.Repeat("x", n))
			if text, ok := s.Get(id); !ok || len(text) != n {
				t.Errorf("Get(%s) = (%d chars, %v), want %d chars", id, len(text), ok, n)
			}
		}(i)
	}
	wg.Wait()
	if _, ok := s.Get("elide-65"); ok {
		t.Error("Get(missing id) must report a miss")
	}
}
