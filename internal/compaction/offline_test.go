package compaction

import (
	"context"
	"strings"
	"testing"
)

// The offline scripted scorer's tests construct NO server of any kind: the
// whole point of the offline path is that scoring never touches the network.
// Any HTTP attempt would have nothing to connect to and would surface as a
// scoring error, so the nil-error assertions below double as the no-network
// proof.

// TestScorerInterfaceSatisfied pins the interface contract the pipeline
// consumes: the production decision client and the offline scripted scorer
// are both Scorer, so either can sit behind the same Pipeline.
func TestScorerInterfaceSatisfied(t *testing.T) {
	var _ Scorer = (*DecisionClient)(nil)
	var _ Scorer = ScriptedScorer{}
}

// TestScriptedScorerDeterministicAndBounded: the same task and text always
// produce the same score (the property that makes offline demos reproducible
// and shadow logs replayable), every score lands in [0, 1) (1.0 — the
// fail-open keep score — is unreachable, so a scripted score can never be
// mistaken for one), and differing content or task changes the score.
func TestScriptedScorerDeterministicAndBounded(t *testing.T) {
	scorer := ScriptedScorer{}
	items := map[string]Item{
		"seg-1": {Text: "the build log shows three failing tests"},
		"seg-2": {Text: "a benchmark table timing the scoring endpoint"},
		"seg-3": {Text: "unrelated stack trace, already fixed"},
	}
	task := "Preserve what the Bash tool output contributed toward the ongoing task."

	first, err := scorer.ScoreBatch(context.Background(), task, items)
	if err != nil {
		t.Fatalf("ScoreBatch() error = %v, want nil (the scripted scorer never fails)", err)
	}
	if len(first) != len(items) {
		t.Fatalf("got %d scores, want one per item (%d)", len(first), len(items))
	}
	for _, text := range []string{
		"the build log shows three failing tests",
		"a benchmark table timing the scoring endpoint",
		"unrelated stack trace, already fixed",
		"",
		strings.Repeat("determinism probe ", 500),
	} {
		one, err := scorer.ScoreBatch(context.Background(), task, map[string]Item{"x": {Text: text}})
		if err != nil {
			t.Fatalf("ScoreBatch(%q) error = %v", text, err)
		}
		again, err := scorer.ScoreBatch(context.Background(), task, map[string]Item{"x": {Text: text}})
		if err != nil {
			t.Fatalf("ScoreBatch(%q) second call error = %v", text, err)
		}
		if one["x"] != again["x"] {
			t.Errorf("score for %q changed across identical calls: %v vs %v", text, one["x"], again["x"])
		}
		if one["x"] < 0 || one["x"] >= 1 {
			t.Errorf("score for %q = %v, want [0,1)", text, one["x"])
		}
	}

	// Different content scores differently (the content hash must actually
	// depend on the content).
	other, err := scorer.ScoreBatch(context.Background(), task, map[string]Item{"seg-1": {Text: "something entirely different"}})
	if err != nil {
		t.Fatal(err)
	}
	if other["seg-1"] == first["seg-1"] {
		t.Errorf("different texts collided on score %v", first["seg-1"])
	}
	// ...and the same text under a different task too.
	otherTask, err := scorer.ScoreBatch(context.Background(), "another task", map[string]Item{"seg-1": items["seg-1"]})
	if err != nil {
		t.Fatal(err)
	}
	if otherTask["seg-1"] == first["seg-1"] {
		t.Errorf("the task did not affect the score (both %v)", first["seg-1"])
	}
}

// TestScriptedScorerEmptyTaskNormalization: an empty task scores like the
// client's defaultTask normalization — the score depends on content even
// when the caller has no task text, matching DecisionClient's behavior.
func TestScriptedScorerEmptyTaskNormalization(t *testing.T) {
	scorer := ScriptedScorer{}
	item := map[string]Item{"x": {Text: "stable text"}}
	empty, err := scorer.ScoreBatch(context.Background(), "", item)
	if err != nil {
		t.Fatal(err)
	}
	viaDefault, err := scorer.ScoreBatch(context.Background(), defaultTask, item)
	if err != nil {
		t.Fatal(err)
	}
	if empty["x"] != viaDefault["x"] {
		t.Errorf("empty task scored %v, want the defaultTask score %v", empty["x"], viaDefault["x"])
	}
	// An empty-item batch is not an error, like the protocol client.
	if got, err := scorer.ScoreBatch(context.Background(), "t", nil); err != nil || len(got) != 0 {
		t.Errorf("ScoreBatch(nil) = (%v, %v), want (empty, nil)", got, err)
	}
}

// TestOfflinePipelineEndToEndByteForByte is the Step 18 core pin, offline
// (no server exists in this test): NewOfflinePipeline over the scripted
// scorer, relocation armed (enabled mode), compacts a tool output into
// [[elided id=r:…]] pointers backed by store records, and Reconstruct
// expands them back byte for byte. A second, identical compaction produces
// the identical compacted text and pointer ids — the determinism the demo
// path advertises.
func TestOfflinePipelineEndToEndByteForByte(t *testing.T) {
	original := strings.Join([]string{
		"essential first paragraph: the migration changes the row format and the reader must know that.",
		strings.Repeat("filler number one ", 60),
		strings.Repeat("filler number two ", 60),
		"essential last paragraph: the benchmark table below is the only evidence of the regression.",
	}, "\n\n")

	run := func() (CompactResult, *Store) {
		p := NewOfflinePipeline(PipelineOptions{})
		store := NewStore()
		// Neutral gate: no min-gate floor (the fixture is small) and a
		// disabled tripwire, so the scripted decisions stand.
		p.ApplyGateConfig(GateConfig{
			MinGateTokens:    0,
			MaxElideFraction: 1.0,
			ProtectedKinds:   map[SegmentKind]float64{},
		})
		p.EnableRelocation(store, 1.0) // below 1.0 always elides: scripted scores are < 1
		res, err := p.CompactToolOutput(context.Background(), "Bash", original)
		if err != nil {
			t.Fatalf("CompactToolOutput() error = %v, want nil (offline scoring never fails)", err)
		}
		return res, store
	}

	res, store := run()
	if len(res.Elided) == 0 {
		t.Fatal("nothing was elided: the offline pipeline relocated nothing")
	}
	if !strings.Contains(res.CompactText, "[[elided id=r:") {
		t.Fatalf("compacted text carries no content-id pointer:\n%s", res.CompactText)
	}
	if res.Tripwire != "" || res.Disabled != "" {
		t.Errorf("tripwire = %q, disabled = %q, want both empty", res.Tripwire, res.Disabled)
	}
	for _, e := range res.Elided {
		rec, ok := store.GetRecord(e.ID)
		if !ok {
			t.Fatalf("store is missing record %q for the elided run", e.ID)
		}
		if rec.Text != e.Text {
			t.Errorf("record %q text differs from the elided run text", e.ID)
		}
	}
	if expanded := Reconstruct(res.CompactText, store); expanded != original {
		t.Errorf("Reconstruct did not restore the original byte for byte (%d vs %d bytes)", len(expanded), len(original))
	}

	// Determinism: an identical second run (fresh store) yields the identical
	// compacted text — same scores, same decisions, same content ids.
	res2, _ := run()
	if res2.CompactText != res.CompactText {
		t.Errorf("second offline compaction differs:\n--- first ---\n%s\n--- second ---\n%s", res.CompactText, res2.CompactText)
	}
	for i := range res.Elided {
		if res2.Elided[i].ID != res.Elided[i].ID {
			t.Errorf("pointer id %d differs across identical runs: %q vs %q", i, res.Elided[i].ID, res2.Elided[i].ID)
		}
	}
}

// TestOfflinePipelineShadowLog: the offline pipeline logs to the shadow log
// exactly like the online one (opts.Shadow) — one keep decision per segment
// in shadow mode — so replay tooling works on offline demo runs too.
func TestOfflinePipelineShadowLog(t *testing.T) {
	shadow, err := NewShadowLogAt(t.TempDir() + "/shadow.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	p := NewOfflinePipeline(PipelineOptions{Shadow: shadow})
	output := strings.Repeat("a", 200) + "\n\n" + strings.Repeat("b", 200) + "\n\n" + strings.Repeat("c", 200)
	got, err := p.ScoreToolOutput(context.Background(), "Bash", output)
	if err != nil {
		t.Fatalf("ScoreToolOutput() error = %v", err)
	}
	if len(got.Segments) != 3 || len(got.Scores) != 3 {
		t.Fatalf("got %d segments / %d scores, want 3/3", len(got.Segments), len(got.Scores))
	}
	report, err := shadow.Replay(0.5)
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if report.Entries != 3 {
		t.Errorf("shadow log recorded %d entries, want 3", report.Entries)
	}
}

// TestOfflinePipelineHistoryScorer: the offline pipeline exposes its
// ScriptedScorer through HistoryScorer, so session.CompactContext runs
// offline through the same seam the online pipeline uses.
func TestOfflinePipelineHistoryScorer(t *testing.T) {
	p := NewOfflinePipeline(PipelineOptions{})
	if p.HistoryScorer() == nil {
		t.Fatal("HistoryScorer() = nil, want the scripted scorer")
	}
	if _, ok := p.HistoryScorer().(ScriptedScorer); !ok {
		t.Errorf("HistoryScorer() = %T, want ScriptedScorer", p.HistoryScorer())
	}
}

// TestRunPreflightOfflinePasses: the -check-compaction offline path runs all
// three real stages locally (no backend resolved, no key, no network) and
// passes by construction. The report names the scripted backend on stage 0.
func TestRunPreflightOfflinePasses(t *testing.T) {
	results, ok := RunPreflightOffline(context.Background())
	if !ok {
		t.Fatalf("RunPreflightOffline() ok = false, want all stages to pass:\n%s", FormatCheckReport(results, ok))
	}
	if len(results) != 4 {
		t.Fatalf("got %d results, want 4 (backend, questions, gate, expand)", len(results))
	}
	for i, want := range []string{CheckStageBackend, CheckStageQuestions, CheckStageGate, CheckStageExpand} {
		if results[i].Stage != want {
			t.Errorf("results[%d].Stage = %q, want %q", i, results[i].Stage, want)
		}
		if !results[i].OK {
			t.Errorf("results[%d] (%s) = FAIL: %s", i, results[i].Stage, results[i].Detail)
		}
	}
	// Stage 0 says there is no endpoint and no key — the scripted scorer.
	for _, want := range []string{`"offline"`, "none", "scripted", "no network"} {
		if !strings.Contains(results[0].Detail, want) {
			t.Errorf("backend detail = %q, want it to contain %q", results[0].Detail, want)
		}
	}
	// The gate really relocated from the synthetic output, and expand named
	// the round trip.
	if !strings.Contains(results[2].Detail, "relocated") {
		t.Errorf("gate detail = %q, want the relocation summary", results[2].Detail)
	}
	if !strings.Contains(results[3].Detail, "byte for byte") {
		t.Errorf("expand detail = %q, want the round-trip summary", results[3].Detail)
	}

	out := FormatCheckReport(results, true)
	if !strings.Contains(out, "result: PASS (4/4 stages ok)") {
		t.Errorf("report missing the PASS verdict:\n%s", out)
	}
}

// TestOfflineBackendName pins the one string the config resolver, main, and
// the docs all spell the same way.
func TestOfflineBackendName(t *testing.T) {
	if OfflineBackendName != "offline" {
		t.Errorf("OfflineBackendName = %q, want \"offline\"", OfflineBackendName)
	}
}
