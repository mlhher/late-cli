package compaction

import (
	"context"
	"strings"
	"testing"
)

// TestPipeline_ActivateSkillResultPreserved pins the activated-skills
// protection: a tool result produced by activate_skill is the skill's
// instructions — what the agent was told to follow — and must NEVER be
// elided, whatever the scorer answers. The gate clamps the result's scores
// up to the 1.0 protected-origin floor, so even a scorer that scores
// everything 0.0 keeps the result verbatim; the same output from an
// unprotected tool elides as usual.
func TestPipeline_ActivateSkillResultPreserved(t *testing.T) {
	const threshold = 0.35
	output, keeper1, filler, keeper2 := relocationOutput()

	// Stub scorer scoring 0.0: without protection everything would elide.
	d := newDecisionsServer(t, fixedScoresHandler(map[string]float64{"seg-1": 0.0, "seg-2": 0.0, "seg-3": 0.0}))

	t.Run("activate_skill result is kept verbatim at score 0.0", func(t *testing.T) {
		store := NewStore()
		p := NewPipeline(ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}, "k", nil, PipelineOptions{})
		p.EnableRelocation(store, threshold)
		applyTestGate(p, threshold, 1)

		got, err := p.CompactToolOutput(context.Background(), SkillToolName, output)
		if err != nil {
			t.Fatalf("CompactToolOutput() error = %v", err)
		}
		if got.CompactText != output {
			t.Errorf("the activate_skill result was mutated:\n got %q\nwant %q", truncateForTest(got.CompactText), truncateForTest(output))
		}
		if len(got.Elided) != 0 {
			t.Errorf("the activate_skill result was elided into %d runs", len(got.Elided))
		}
		if store.Len() != 0 {
			t.Error("nothing from a protected tool result should be stored")
		}
		if !strings.Contains(got.CompactText, keeper1) || !strings.Contains(got.CompactText, filler) || !strings.Contains(got.CompactText, keeper2) {
			t.Error("the protected result lost content")
		}
	})

	t.Run("the same output from an unprotected tool elides", func(t *testing.T) {
		store := NewStore()
		p := NewPipeline(ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}, "k", nil, PipelineOptions{})
		p.EnableRelocation(store, threshold)
		applyTestGate(p, threshold, 1)

		got, err := p.CompactToolOutput(context.Background(), "Bash", output)
		if err != nil {
			t.Fatalf("CompactToolOutput() error = %v", err)
		}
		if len(got.Elided) != 1 {
			t.Fatalf("control run: Elided = %d, want 1 (all three segments score 0.0)", len(got.Elided))
		}
		if got.CompactText == output {
			t.Error("control run: expected elision")
		}
	})

	t.Run("ProtectedTool and origin floor agree", func(t *testing.T) {
		if !ProtectedTool(SkillToolName) {
			t.Error("ProtectedTool(activate_skill) = false, want true")
		}
		if ProtectedTool("Bash") || ProtectedTool("expand") {
			t.Error("ProtectedTool = true for an unprotected tool")
		}
		if f, ok := originScoreFloor(OriginSourceSkillTool); !ok || f != 1.0 {
			t.Errorf("originScoreFloor(%q) = (%v, %v), want (1.0, true)", OriginSourceSkillTool, f, ok)
		}
		if got := protectedScore(OriginSourceSkillTool, 0.0); got != 1.0 {
			t.Errorf("protectedScore clamped %v, want 1.0", got)
		}
		if got := protectedScore(OriginSourceToolPrefix+"Bash", 0.0); got != 0.0 {
			t.Errorf("unprotected origin score changed: %v", got)
		}
	})
}
