package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixedScoresHandler serves the same score map for every request.
func fixedScoresHandler(scores map[string]float64) func(int, capturedRequest) (int, string) {
	return func(_ int, _ capturedRequest) (int, string) {
		return http.StatusOK, answersBody(scores)
	}
}

// shrinkPipelineRetryCurve shrinks the pipeline's decision-client retry
// curve for a fast test. The pipeline holds its scorer behind the Scorer
// interface (so the offline pipeline can swap in the scripted scorer);
// production code never touches the concrete retry fields, tests do, hence
// the type assertion — a no-op for a pipeline over any other scorer.
func shrinkPipelineRetryCurve(p *Pipeline) {
	if dc, ok := p.client.(*DecisionClient); ok {
		dc.baseBackoff, dc.maxBackoff = time.Millisecond, time.Millisecond
	}
}

func TestPipeline_ScoreToolOutputEndToEnd(t *testing.T) {
	scores := map[string]float64{"seg-1": 0.9, "seg-2": 0.1, "seg-3": 0.55}
	d := newDecisionsServer(t, fixedScoresHandler(scores))

	shadowPath := filepath.Join(t.TempDir(), "shadow.jsonl")
	shadow, err := NewShadowLogAt(shadowPath)
	if err != nil {
		t.Fatal(err)
	}

	backend := ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}
	p := NewPipeline(backend, "k", shadow, PipelineOptions{})
	now := time.Unix(1700000000, 0).UTC()
	p.now = func() time.Time { return now }

	output := "essential output\n\nfiller paragraph\n\nanother keeper"
	got, err := p.ScoreToolOutput(context.Background(), "Bash", output)
	if err != nil {
		t.Fatalf("ScoreToolOutput() error = %v", err)
	}

	// Segmentation flowed through.
	if len(got.Segments) != 3 {
		t.Fatalf("got %d segments, want 3", len(got.Segments))
	}
	assertSegmentsInvariant(t, output, got.Segments)

	// Scores round-tripped.
	for ref, want := range scores {
		if got.Scores[ref] != want {
			t.Errorf("Scores[%s] = %v, want %v", ref, got.Scores[ref], want)
		}
	}
	if len(got.Errors) != 0 {
		t.Errorf("Errors = %v, want none", got.Errors)
	}

	// The shadow log recorded one keep decision per segment.
	report, err := shadow.Replay(0.5)
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if report.Entries != 3 || report.UniqueSegments != 3 {
		t.Fatalf("Replay() = %+v, want 3 entries over 3 segments", report)
	}
	if report.ElidedEntries != 1 || report.TokensElided != got.Segments[1].Tokens {
		t.Errorf("Replay() = %+v, want only seg-2 (score 0.1) elided", report)
	}
	lines := readLines(t, shadowPath)
	if len(lines) != 3 {
		t.Fatalf("got %d shadow lines, want 3", len(lines))
	}
	for _, line := range lines {
		var e ShadowEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("shadow line invalid: %v", err)
		}
		if e.Decision != DecisionKeep {
			t.Errorf("shadow decision = %q, want %q (shadow-only stage)", e.Decision, DecisionKeep)
		}
		// No gate was applied and relocation is not armed, so no elision
		// threshold is in force: the entry records 0 (omitted in JSON) —
		// Stats/FalseNegativeRate never count such an entry as elided.
		if e.Threshold != 0 {
			t.Errorf("shadow threshold = %v, want 0 (no gate in force)", e.Threshold)
		}
		if e.TaskHash == "" || e.TaskHash != got.TaskHash {
			t.Errorf("shadow TaskHash = %q, want the pipeline's %q", e.TaskHash, got.TaskHash)
		}
		if !e.TS.Equal(now) {
			t.Errorf("shadow TS = %v, want the pipeline clock %v", e.TS, now)
		}
	}
}

// TestPipeline_FailOpenStillLogs: with the provider down, every segment
// fail-opens to 1.0 and the shadow log still records the decisions.
func TestPipeline_FailOpenStillLogs(t *testing.T) {
	d := newDecisionsServer(t, func(_ int, _ capturedRequest) (int, string) {
		return http.StatusServiceUnavailable, `{"error": {"message": "down"}}`
	})
	shadow, err := NewShadowLogAt(filepath.Join(t.TempDir(), "shadow.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	backend := ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}
	p := NewPipeline(backend, "k", shadow, PipelineOptions{})
	// Shrink the client's retry curve for a fast test.
	shrinkPipelineRetryCurve(p)

	output := strings.Repeat("a", 200) + "\n\n" + strings.Repeat("b", 200) + "\n\n" + strings.Repeat("c", 200)
	got, err := p.ScoreToolOutput(context.Background(), "Bash", output)
	if err == nil {
		t.Fatal("ScoreToolOutput() error = nil, want the outage recorded")
	}
	if len(got.Segments) != 3 {
		t.Fatalf("got %d segments, want 3", len(got.Segments))
	}
	for _, s := range got.Segments {
		if got.Scores[s.ID] != keepScore {
			t.Errorf("Scores[%s] = %v, want %v (fail-open)", s.ID, got.Scores[s.ID], keepScore)
		}
	}

	report, err := shadow.Replay(0.9)
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if report.Entries != 3 || report.ElidedEntries != 0 {
		t.Errorf("Replay() = %+v, want 3 kept entries (1.0 ≥ 0.9)", report)
	}
}

// TestPipeline_ShadowAppendFailureIsRecordedNotFatal: a broken log must not
// break scoring; the failures land in SegmentScores.Errors.
func TestPipeline_ShadowAppendFailureIsRecordedNotFatal(t *testing.T) {
	d := newDecisionsServer(t, echoHandler)
	shadow, err := NewShadowLogAt(filepath.Join(t.TempDir(), "logdir"))
	if err != nil {
		t.Fatal(err)
	}
	// Make the log path a directory so every append fails.
	if err := os.MkdirAll(shadow.Path(), 0o700); err != nil {
		t.Fatal(err)
	}

	backend := ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}
	p := NewPipeline(backend, "k", shadow, PipelineOptions{})
	got, err := p.ScoreToolOutput(context.Background(), "Bash", "one paragraph")
	if err != nil {
		t.Fatalf("ScoreToolOutput() error = %v, want scoring to succeed", err)
	}
	if len(got.Errors) == 0 {
		t.Error("Errors is empty, want the append failures recorded")
	}
	found := false
	for _, e := range got.Errors {
		if strings.Contains(e.Error(), "shadow log append") {
			found = true
		}
	}
	if !found {
		t.Errorf("Errors = %v, want a shadow-log append failure", got.Errors)
	}
}

func TestPipeline_EmptyOutputSkipsEverything(t *testing.T) {
	var requests int
	d := newDecisionsServer(t, func(_ int, req capturedRequest) (int, string) {
		requests++
		return echoHandler(1, req)
	})
	shadow, err := NewShadowLogAt(filepath.Join(t.TempDir(), "shadow.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	backend := ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}
	p := NewPipeline(backend, "k", shadow, PipelineOptions{})

	for _, output := range []string{"", "\n\n  \n\n"} {
		got, err := p.ScoreToolOutput(context.Background(), "Bash", output)
		if err != nil {
			t.Fatalf("ScoreToolOutput(%q) error = %v", output, err)
		}
		if len(got.Segments) != 0 || len(got.Scores) != 0 {
			t.Errorf("ScoreToolOutput(%q) = %+v, want empty", output, got)
		}
	}
	if requests != 0 {
		t.Errorf("got %d requests, want 0 for empty outputs", requests)
	}
	report, err := shadow.Replay(0.5)
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if report.Entries != 0 {
		t.Errorf("shadow log has %d entries, want 0", report.Entries)
	}
}

// TestPipeline_NilShadowScoresWithoutLogging covers the dry-run form.
func TestPipeline_NilShadowScoresWithoutLogging(t *testing.T) {
	d := newDecisionsServer(t, echoHandler)
	backend := ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}
	p := NewPipeline(backend, "k", nil, PipelineOptions{})
	got, err := p.ScoreToolOutput(context.Background(), "Bash", "alpha\n\nbeta")
	if err != nil {
		t.Fatalf("ScoreToolOutput() error = %v", err)
	}
	if len(got.Scores) != 2 {
		t.Errorf("got %d scores, want 2", len(got.Scores))
	}
	if len(got.Errors) != 0 {
		t.Errorf("Errors = %v, want none", got.Errors)
	}
}

// TestPipeline_CanceledContextFailsOpen: a canceled context must not wedge
// the caller — everything is kept, with the cancellation recorded.
func TestPipeline_CanceledContextFailsOpen(t *testing.T) {
	d := newDecisionsServer(t, echoHandler)
	backend := ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}
	p := NewPipeline(backend, "k", nil, PipelineOptions{})
	shrinkPipelineRetryCurve(p)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := p.ScoreToolOutput(ctx, "Bash", "alpha\n\nbeta")
	if err == nil {
		t.Fatal("ScoreToolOutput() error = nil, want the cancellation recorded")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want it to wrap context.Canceled", err)
	}
	for _, s := range got.Segments {
		if got.Scores[s.ID] != keepScore {
			t.Errorf("Scores[%s] = %v, want %v (fail-open)", s.ID, got.Scores[s.ID], keepScore)
		}
	}
}

// TestScoreTask_DerivedTaskShape pins the derived task used while the tool
// layer is not yet wired.
func TestScoreTask_DerivedTaskShape(t *testing.T) {
	task := scoreTask("Bash")
	if !strings.Contains(task, "Bash") || !strings.Contains(task, "ongoing task") {
		t.Errorf("scoreTask = %q, want it to name the tool and the ongoing task", task)
	}
}
