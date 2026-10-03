package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"late/internal/compaction"
	appconfig "late/internal/config"
	"late/internal/session"
)

// The offline compaction tests never construct a server: compaction-backend
// "offline" is the no-key, no-network demo path, so every assertion below
// runs against the ScriptedScorer with nothing to connect to — an attempted
// request would fail and fail the test.

// TestOfflineBackendNeedsNoEnvKeys is the wiring smoke test (Step 18): in an
// environment where NO System One backend resolves (empty env lookup, empty
// key dir), the config offline selection still builds a working pipeline and
// scores — no key, no env, no network.
func TestOfflineBackendNeedsNoEnvKeys(t *testing.T) {
	// Negative control: the env-based resolution must fail in this machine
	// state, so the offline path below is demonstrably not leaning on it.
	if _, err := compaction.ResolveBackendIn(t.TempDir(), "", func(string) string { return "" }); err == nil {
		t.Fatal("no backend should resolve with an empty environment and no key files")
	}

	// The config selects offline; the resolver honors it with no warning.
	backend, warning := appconfig.ResolveCompactionBackend(&appconfig.Config{CompactionBackend: appconfig.CompactionBackendOffline})
	if backend != appconfig.CompactionBackendOffline {
		t.Fatalf("ResolveCompactionBackend() = %q (warning %q), want %q", backend, warning, appconfig.CompactionBackendOffline)
	}
	if warning != "" {
		t.Fatalf("ResolveCompactionBackend() warning = %q, want none", warning)
	}

	// The wiring that follows never resolves a backend: it builds the
	// offline pipeline directly (mirroring main()'s offline branch) and
	// scores through it.
	pipeline := compaction.NewOfflinePipeline(compaction.PipelineOptions{})
	output := strings.Repeat("a", 200) + "\n\n" + strings.Repeat("b", 200) + "\n\n" + strings.Repeat("c", 200)
	got, err := pipeline.ScoreToolOutput(context.Background(), "Bash", output)
	if err != nil {
		t.Fatalf("ScoreToolOutput() error = %v, want nil (offline scoring never touches the network)", err)
	}
	if len(got.Scores) != len(got.Segments) || len(got.Segments) != 3 {
		t.Fatalf("got %d segments / %d scores, want 3/3", len(got.Segments), len(got.Scores))
	}
	if len(got.Errors) != 0 {
		t.Errorf("Errors = %v, want none", got.Errors)
	}
}

// TestCheckCompactionOfflinePassPath pins the -check-compaction offline
// branch at unit level: the three stages (questions, gate, expand) run
// against the offline scripted scorer and pass, and the flag's exit-code
// wrapper returns 0.
func TestCheckCompactionOfflinePassPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	results, ok := compaction.RunPreflightOffline(ctx)
	if !ok {
		t.Fatalf("offline preflight failed:\n%s", compaction.FormatCheckReport(results, ok))
	}
	for _, r := range results {
		if !r.OK {
			t.Errorf("stage %q failed: %s", r.Stage, r.Detail)
		}
	}
	stages := map[string]bool{}
	for _, r := range results {
		stages[r.Stage] = true
	}
	for _, want := range []string{compaction.CheckStageQuestions, compaction.CheckStageGate, compaction.CheckStageExpand} {
		if !stages[want] {
			t.Errorf("the three stages must all run offline; missing %q", want)
		}
	}

	// The flag wrapper: exit code 0 (it prints the report to stdout).
	if code := runCompactionCheck(true); code != 0 {
		t.Errorf("runCompactionCheck(offline) = %d, want 0", code)
	}
}

// TestOfflineSessionCompactContextEndToEnd is the history-side half of the
// offline end-to-end pin: the offline pipeline's scorer drives
// session.CompactContext (the /jev-compact-context flow) enabled-mode, the
// rewritten history carries [[elided id=r:…]] content-id pointers, and
// Reconstruct over the store restores the original message byte for byte.
// No network anywhere.
func TestOfflineSessionCompactContextEndToEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	sess := compactionTestSession(t, path)
	original := sess.History[1].Content.Text

	pipeline := compaction.NewOfflinePipeline(compaction.PipelineOptions{})
	store := compaction.NewStore()

	report, err := sess.CompactContext(context.Background(), pipeline.HistoryScorer(), store, session.CompactionOptions{
		Threshold: 1.0, // below 1.0 always elides: scripted scores are < 1
	})
	if err != nil {
		t.Fatalf("CompactContext() error = %v, want a clean offline walk", err)
	}
	if report.ShadowOnly {
		t.Error("a mutating offline run must not report ShadowOnly")
	}
	if report.MessagesCompacted != 1 || report.SegmentsElided < 1 {
		t.Fatalf("report = %+v, want the assistant message compacted with ≥1 segment elided", report)
	}

	// The user message is never compacted; the assistant message carries a
	// content-id pointer.
	if got := sess.History[0].Content.Text; got != "Please analyze this build log." {
		t.Errorf("user message mutated:\n%s", got)
	}
	compacted := sess.History[1].Content.Text
	if !strings.Contains(compacted, "[[elided id=r:") {
		t.Fatalf("compacted history carries no pointer:\n%s", compacted)
	}

	// Byte-for-byte inverse through the same store.
	if expanded := compaction.Reconstruct(compacted, store); expanded != original {
		t.Errorf("Reconstruct did not restore the original byte for byte (%d vs %d bytes)", len(expanded), len(original))
	}

	// The expand tool's read side (what a real enabled-mode session wires)
	// resolves the pointer from the same store.
	if text, ok := store.Get(pointerID(t, compacted)); !ok || text != original {
		t.Errorf("store.Get(pointer) = (%q, %v), want the original message", text, ok)
	}
}

// pointerID extracts the first [[elided id=…]] pointer id from text via the
// compaction package's own parser.
func pointerID(t *testing.T, text string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if p, ok := compaction.ParsePointer(line); ok {
			return p.ID
		}
	}
	t.Fatalf("no pointer in:\n%s", text)
	return ""
}
