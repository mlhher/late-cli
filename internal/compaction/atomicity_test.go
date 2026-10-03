package compaction

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// oversizedJSONParagraph builds one >2x-DefaultMaxSegChars JSON paragraph: a
// single pretty-printed object (no blank lines, so it is ONE paragraph) whose
// newline-pretty lines force splitOversized to cut it mid-structure — the
// exact shape the atomicity rule exists for.
func oversizedJSONParagraph() string {
	var b strings.Builder
	b.WriteString("{\n")
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "  \"key_%03d\": \"value %03d with some padding text to bulk the line past trivial lengths\",\n", i, i)
	}
	b.WriteString("  \"final\": true\n}")
	if len(b.String()) <= 2*DefaultMaxSegChars {
		panic("fixture too small; the test needs a >2x maxSegChars paragraph")
	}
	return b.String()
}

// TestSegmentSegments_CutPiecesShareGroup pins the grouping invariant the
// atomicity decision rides on: pieces splitOversized cut from one oversized
// paragraph share a Group, distinct paragraphs get distinct groups, and the
// pieces tile the paragraph's bytes.
func TestSegmentSegments_CutPiecesShareGroup(t *testing.T) {
	blob := oversizedJSONParagraph()
	segs := SegmentSegments(blob, 0)
	if len(segs) < 3 {
		t.Fatalf("SegmentSegments() = %d segments, want ≥3 pieces of the oversized paragraph", len(segs))
	}
	for i, seg := range segs {
		if seg.Group != segs[0].Group {
			t.Errorf("piece %d Group = %d, want %d (all pieces of one paragraph share the group)", i, seg.Group, segs[0].Group)
		}
		if seg.Group == 0 {
			t.Errorf("piece %d Group = 0, want a nonzero group id from SegmentSegments", i)
		}
	}
	// The pieces tile the paragraph byte for byte.
	var rebuilt strings.Builder
	for _, seg := range segs {
		rebuilt.WriteString(seg.Text)
	}
	if rebuilt.String() != blob {
		t.Error("the pieces do not reconstruct the original paragraph")
	}

	// Distinct paragraphs get distinct groups.
	multi := strings.Repeat("alpha paragraph\n\n", 2) + blob
	multiSegs := SegmentSegments(multi, 0)
	if multiSegs[0].Group == multiSegs[len(multiSegs)-1].Group {
		t.Error("separate paragraphs must not share a group")
	}
}

// TestAtomicElideDecisions pins the shared decision math: a group elides as
// a unit on its MINIMUM sibling score against the MINIMUM sibling floor, and
// ungrouped segments decide on their own score and floor.
func TestAtomicElideDecisions(t *testing.T) {
	segs := []Segment{
		{ID: "seg-1", Group: 1},
		{ID: "seg-2", Group: 1},
		{ID: "seg-3", Group: 1},
		{ID: "seg-4"}, // ungrouped: decides alone
	}
	// One low sibling (seg-2 at 0.1) below the flat floor 0.35 → the whole
	// paragraph elides, including its 0.9 siblings; the ungrouped 0.9 stays.
	elide := AtomicElideDecisions(segs,
		[]float64{0.9, 0.1, 0.9, 0.9},
		[]float64{0.35, 0.35, 0.35, 0.35})
	want := []bool{true, true, true, false}
	for i := range want {
		if elide[i] != want[i] {
			t.Errorf("elide[%d] = %v, want %v", i, elide[i], want[i])
		}
	}

	// All siblings above the floor → the whole paragraph is kept.
	elide = AtomicElideDecisions(segs,
		[]float64{0.9, 0.5, 0.9, 0.9},
		[]float64{0.35, 0.35, 0.35, 0.35})
	for i := range elide {
		if elide[i] {
			t.Errorf("elide[%d] = true, want the whole paragraph kept", i)
		}
	}

	// Mixed floors: the group decides on the MINIMUM sibling floor, so a
	// protected sibling (stacktrace floor 0.05) keeps the paragraph unless
	// the minimum sibling score falls below even that floor.
	protected := []Segment{{ID: "seg-1", Group: 1}, {ID: "seg-2", Group: 1}}
	elide = AtomicElideDecisions(protected,
		[]float64{0.2, 0.2}, // below the text floor 0.35, above 0.05
		[]float64{0.35, 0.05})
	if elide[0] || elide[1] {
		t.Error("a protected sibling's floor must keep the paragraph above the protected floor")
	}
	elide = AtomicElideDecisions(protected,
		[]float64{0.01, 0.9},
		[]float64{0.35, 0.05})
	if !elide[0] || !elide[1] {
		t.Error("a sibling below even the protected floor must elide the whole paragraph")
	}

	// Mismatched slices: a caller bug — no decisions, not a panic.
	if got := AtomicElideDecisions(segs, []float64{0.5}, []float64{0.35}); got != nil {
		t.Errorf("AtomicElideDecisions with mismatched slices = %v, want nil", got)
	}
}

// TestPipeline_CompactToolOutput_ParagraphAtomicity is the end-to-end pin:
// a >2x maxSegChars JSON paragraph cut into pieces elides as ONE unit when a
// single piece scores below the floor — the pointer references the full
// original and Reconstruct restores it byte for byte — and stays byte-exact
// when every piece is above the floor.
func TestPipeline_CompactToolOutput_ParagraphAtomicity(t *testing.T) {
	const threshold = 0.35
	blob := oversizedJSONParagraph()
	segs := SegmentSegments(blob, 0)
	if len(segs) < 3 {
		t.Fatalf("fixture sanity: %d segments, want ≥3", len(segs))
	}

	t.Run("one low piece elides the whole paragraph", func(t *testing.T) {
		// Only the middle piece scores below the floor; without atomicity
		// the elided middle would leave an unparseable JSON remnant behind.
		scores := map[string]float64{"seg-1": 0.9, "seg-2": 0.1, "seg-3": 0.9}
		for i := 4; i <= len(segs); i++ {
			scores[fmt.Sprintf("seg-%d", i)] = 0.9
		}
		d := newDecisionsServer(t, fixedScoresHandler(scores))
		store := NewStore()
		p := NewPipeline(ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}, "k", nil, PipelineOptions{})
		p.EnableRelocation(store, threshold)
		applyTestGate(p, threshold, 1) // tripwire off: the whole paragraph elides on purpose

		got, err := p.CompactToolOutput(context.Background(), "Bash", blob)
		if err != nil {
			t.Fatalf("CompactToolOutput() error = %v", err)
		}
		if len(got.Elided) != 1 {
			t.Fatalf("Elided = %d runs, want 1 (the whole paragraph)", len(got.Elided))
		}
		if got.Elided[0].Segments != len(segs) {
			t.Errorf("elided run covers %d segments, want all %d pieces", got.Elided[0].Segments, len(segs))
		}
		if got.Elided[0].Text != blob {
			t.Error("the elided run's text is not the FULL original paragraph")
		}
		if !strings.HasPrefix(got.CompactText, "[[elided id=r:") {
			t.Errorf("compacted text should be one pointer line, got:\n%s", truncateForTest(got.CompactText))
		}
		// The pointer references the full original through the store, and
		// Reconstruct is the byte-for-byte inverse.
		if stored, ok := store.Get(got.Elided[0].ID); !ok || stored != blob {
			t.Error("the store does not hold the full original under the pointer's id")
		}
		if restored := Reconstruct(got.CompactText, store); restored != blob {
			t.Error("Reconstruct(compacted) is not byte-for-byte")
		}
	})

	t.Run("all pieces above the floor keep the paragraph", func(t *testing.T) {
		scores := map[string]float64{}
		for i := 1; i <= len(segs); i++ {
			scores[fmt.Sprintf("seg-%d", i)] = 0.9
		}
		d := newDecisionsServer(t, fixedScoresHandler(scores))
		store := NewStore()
		p := NewPipeline(ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}, "k", nil, PipelineOptions{})
		p.EnableRelocation(store, threshold)
		applyTestGate(p, threshold, 1)

		got, err := p.CompactToolOutput(context.Background(), "Bash", blob)
		if err != nil {
			t.Fatalf("CompactToolOutput() error = %v", err)
		}
		if len(got.Elided) != 0 || got.CompactText != blob {
			t.Error("a paragraph whose pieces all stay above the floor must be kept byte-for-byte")
		}
		if store.Len() != 0 {
			t.Error("nothing should have been stored")
		}
	})

	t.Run("shadow decisions match the atomic elide", func(t *testing.T) {
		scores := map[string]float64{"seg-1": 0.9, "seg-2": 0.1, "seg-3": 0.9}
		for i := 4; i <= len(segs); i++ {
			scores[fmt.Sprintf("seg-%d", i)] = 0.9
		}
		d := newDecisionsServer(t, fixedScoresHandler(scores))
		shadowPath := t.TempDir() + "/shadow.jsonl"
		shadow, err := NewShadowLogAt(shadowPath)
		if err != nil {
			t.Fatal(err)
		}
		p := NewPipeline(ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}, "k", shadow, PipelineOptions{})
		p.EnableRelocation(NewStore(), threshold)
		applyTestGate(p, threshold, 1)

		if _, err := p.CompactToolOutput(context.Background(), "Bash", blob); err != nil {
			t.Fatalf("CompactToolOutput() error = %v", err)
		}
		entries, malformed, err := shadow.readEntries()
		if err != nil {
			t.Fatal(err)
		}
		if malformed != 0 || len(entries) != len(segs) {
			t.Fatalf("shadow log = %d entries (%d malformed), want %d clean decisions", len(entries), malformed, len(segs))
		}
		for _, e := range entries {
			// Every piece's recorded decision is the paragraph's shared
			// one, made on the minimum sibling score — so score vs
			// threshold replays it exactly.
			if e.Decision != DecisionElide {
				t.Errorf("decision for %s = %q, want %q (the paragraph elided as a unit)", e.SegmentID, e.Decision, DecisionElide)
			}
			if e.Score != 0.1 || e.Threshold != threshold {
				t.Errorf("entry %s records score %v vs threshold %v, want the group-min 0.1 vs %v", e.SegmentID, e.Score, e.Threshold, threshold)
			}
		}
	})
}

// truncateForTest caps a string for failure messages.
func truncateForTest(s string) string {
	if len(s) <= 200 {
		return s
	}
	return s[:200] + "…"
}

// TestAtomicDecisionScores_ProtectedSiblingPinsGroupKeep pins the
// protection-override rule: a sibling at the unelidable score ceiling (the
// 1.0 score protectedScore clamps activate_skill results to, or the
// fail-open keep score) pins its whole cut paragraph to keep — even when a
// normal sibling scores far below its own floor. The naive min-floor reduce
// would INVERT protection here: min(1.0-floor sibling, 0.35-floor sibling)
// = 0.35, and the protected piece would elide along with its low-scoring
// sibling. It also pins the recorded decision inputs, so the shadow
// log's score-vs-floor replay reproduces the keep a mutating run makes.
func TestAtomicDecisionScores_ProtectedSiblingPinsGroupKeep(t *testing.T) {
	segs := []Segment{
		{ID: "seg-1", Group: 1}, // protected sibling: score clamped to the ceiling
		{ID: "seg-2", Group: 1}, // normal sibling scoring below its floor
		{ID: "seg-3"},           // ungrouped control: decides alone
	}
	decide, decFloors := AtomicDecisionScores(segs,
		[]float64{unelidableScore, 0.1, 0.1},
		[]float64{0.35, 0.35, 0.35})
	if decide == nil {
		t.Fatal("AtomicDecisionScores() = nil, want decisions")
	}
	for i := 0; i < 2; i++ {
		if decide[i] != unelidableScore {
			t.Errorf("decide[%d] = %v, want the unelidable ceiling %v (the group is pinned to keep)", i, decide[i], unelidableScore)
		}
		if decFloors[i] != 0.35 {
			t.Errorf("decFloors[%d] = %v, want the group-minimum floor 0.35", i, decFloors[i])
		}
	}
	// The ungrouped control keeps its own inputs — and still elides.
	if decide[2] != 0.1 || decFloors[2] != 0.35 {
		t.Errorf("ungrouped control = (%v, %v), want (0.1, 0.35)", decide[2], decFloors[2])
	}

	elide := AtomicElideDecisions(segs,
		[]float64{unelidableScore, 0.1, 0.1},
		[]float64{0.35, 0.35, 0.35})
	if elide[0] || elide[1] {
		t.Error("a group holding an unelidable sibling must never elide")
	}
	if !elide[2] {
		t.Error("the ungrouped low scorer must still elide")
	}

	// Replay consistency: the pinned decision inputs re-decide as keep.
	if decide[0] < decFloors[0] {
		t.Error("the recorded decision inputs must replay as keep (score >= floor)")
	}

	// The fail-open keep score (keepScore — also the ceiling) pins the
	// group the same way: a piece the scorer could not answer keeps its
	// whole paragraph instead of eliding with a low sibling.
	elide = AtomicElideDecisions(segs,
		[]float64{keepScore, 0.1, 0.9},
		[]float64{0.35, 0.35, 0.35})
	if elide[0] || elide[1] {
		t.Error("a fail-open keep score must pin its group to keep")
	}
	if elide[2] {
		t.Error("the ungrouped 0.9 control must stay kept")
	}
}

// TestPipeline_CompactToolOutput_UnscoreablePieceKeepsParagraph is the
// end-to-end shape of the unelidable pin: one piece of a cut oversized
// paragraph comes back at the fail-open ceiling (the scorer refused it —
// here, an explicit 1.0, the value ScoreBatch reports for unscoreable
// items) while its siblings score low. The paragraph is ATOMIC and the
// ceiling sibling is unelidable, so nothing is elided — the pre-pin
// min-score reduce would have elided the whole paragraph, ceiling sibling
// included.
func TestPipeline_CompactToolOutput_UnscoreablePieceKeepsParagraph(t *testing.T) {
	const threshold = 0.35
	blob := oversizedJSONParagraph()
	segs := SegmentSegments(blob, 0)
	if len(segs) < 3 {
		t.Fatalf("fixture sanity: %d segments, want ≥3", len(segs))
	}

	scores := map[string]float64{"seg-1": keepScore} // the ceiling sibling
	for i := 2; i <= len(segs); i++ {
		scores[fmt.Sprintf("seg-%d", i)] = 0.1 // low siblings
	}
	d := newDecisionsServer(t, fixedScoresHandler(scores))
	store := NewStore()
	p := NewPipeline(ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}, "k", nil, PipelineOptions{})
	p.EnableRelocation(store, threshold)
	applyTestGate(p, threshold, 1)

	got, err := p.CompactToolOutput(context.Background(), "Bash", blob)
	if err != nil {
		t.Fatalf("CompactToolOutput() error = %v", err)
	}
	if len(got.Elided) != 0 || got.CompactText != blob {
		t.Error("a paragraph holding a ceiling-score sibling must be kept byte-for-byte")
	}
	if store.Len() != 0 {
		t.Error("nothing should have been stored")
	}
}
