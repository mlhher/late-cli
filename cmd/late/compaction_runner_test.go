package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"late/internal/client"
	"late/internal/compaction"
	"late/internal/session"
)

// fakeHistoryScorer scores every segment with one fixed score, standing in
// for the compaction pipeline's decision client.
type fakeHistoryScorer struct {
	score float64
}

func (f fakeHistoryScorer) ScoreBatch(_ context.Context, _ string, items map[string]compaction.Item) (map[string]float64, error) {
	out := make(map[string]float64, len(items))
	for id := range items {
		out[id] = f.score
	}
	return out, nil
}

func compactionTestSession(t *testing.T, path string) *session.Session {
	t.Helper()
	// A completing mutating run persists the compaction high-water mark
	// through the session meta sidecar — sandbox SessionDir so these tests
	// never write into the real user sessions directory (t.TempDir paths
	// keep everything inside the test sandbox).
	sessionDir := t.TempDir()
	originalSessionDir := session.SessionDir
	session.SessionDir = func() (string, error) { return sessionDir, nil }
	t.Cleanup(func() { session.SessionDir = originalSessionDir })
	return session.New(nil, path, []client.ChatMessage{
		{Role: "user", Content: client.TextContent("Please analyze this build log.")},
		// The compaction candidate: assistant content annotating a tool
		// call. (A pure-prose assistant message — no tool calls — is never
		// compacted; the walk preserves it byte-identically.)
		{
			Role:      "assistant",
			Content:   client.TextContent(strings.Repeat("verbose analysis ", 200)),
			ToolCalls: []client.ToolCall{{Index: 0, ID: "call_1", Type: "function", Function: client.FunctionCall{Name: "Bash", Arguments: `{"cmd":"make build"}`}}},
		},
	}, "system prompt", false)
}

// TestHistoryCompactionRunnerPersistsMutatingRun: an enabled-mode run
// compacts the session history in place and persists it to the session's
// history path, mirroring the orchestrator's own SaveHistory call sites.
func TestHistoryCompactionRunnerPersistsMutatingRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	sess := compactionTestSession(t, path)
	store := compaction.NewStore()

	runner := historyCompactionRunner(sess, fakeHistoryScorer{score: 0}, store, false, compaction.DefaultRelocationThreshold, nil)
	report, err := runner(context.Background())
	if err != nil {
		t.Fatalf("runner() error = %v", err)
	}
	if report.ShadowOnly {
		t.Fatal("mutating run must not report ShadowOnly")
	}
	if report.SegmentsElided == 0 || report.TokensSaved <= 0 {
		t.Fatalf("expected a real elision, report = %+v", report)
	}
	if !strings.Contains(sess.History[1].Content.Text, "[[elided") {
		t.Fatal("compaction must rewrite the assistant message in place")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("SaveHistory did not write %s: %v", path, err)
	}
	var saved []client.ChatMessage
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("saved history is not valid JSON: %v", err)
	}
	if len(saved) != 2 {
		t.Fatalf("saved history has %d messages, want 2", len(saved))
	}
	if !strings.Contains(saved[1].Content.String(), "[[elided") {
		t.Fatal("the persisted history must contain the compacted message")
	}
}

// TestHistoryCompactionRunnerShadowRunSkipsPersistence: a shadow run computes
// the honest would-save report without mutating history or writing the
// session file.
func TestHistoryCompactionRunnerShadowRunSkipsPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	sess := compactionTestSession(t, path)
	store := compaction.NewStore()
	original := sess.History[1].Content.Text

	runner := historyCompactionRunner(sess, fakeHistoryScorer{score: 0}, store, true, compaction.DefaultRelocationThreshold, nil)
	report, err := runner(context.Background())
	if err != nil {
		t.Fatalf("runner() error = %v", err)
	}
	if !report.ShadowOnly {
		t.Fatal("shadow run must report ShadowOnly")
	}
	if report.SegmentsElided == 0 || report.TokensSaved <= 0 {
		t.Fatalf("shadow report must quantify the would-save, report = %+v", report)
	}
	if sess.History[1].Content.Text != original {
		t.Fatal("shadow run must not mutate history")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("shadow run must not write the history file, stat error = %v", err)
	}
	if _, ok := store.Get("elide-1"); ok {
		t.Fatal("shadow run must not store originals")
	}
}

// failingHistoryScorer returns no usable scores at all, standing in for a
// wholesale scorer failure (the mid-walk abort path).
type failingHistoryScorer struct{}

func (failingHistoryScorer) ScoreBatch(_ context.Context, _ string, _ map[string]compaction.Item) (map[string]float64, error) {
	return nil, errors.New("scorer down")
}

// readShadowLines reads the JSONL shadow log and decodes each line.
func readShadowLines(t *testing.T, path string) []compaction.ShadowEntry {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read shadow log %s: %v", path, err)
	}
	var entries []compaction.ShadowEntry
	for i, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e compaction.ShadowEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("shadow log line %d is not valid JSON: %v (%q)", i, err, line)
		}
		entries = append(entries, e)
	}
	return entries
}

// TestHistoryCompactionRunnerAppendsRunSummary: every mutating run appends
// exactly one "history-run" summary line carrying the report's totals, keyed
// by the same task hash the per-segment decisions use.
func TestHistoryCompactionRunnerAppendsRunSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	sess := compactionTestSession(t, path)
	store := compaction.NewStore()
	shadowLog, err := compaction.NewShadowLogAt(filepath.Join(t.TempDir(), "shadow.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	runner := historyCompactionRunner(sess, fakeHistoryScorer{score: 0}, store, false, compaction.DefaultRelocationThreshold, shadowLog)
	report, err := runner(context.Background())
	if err != nil {
		t.Fatalf("runner() error = %v", err)
	}

	entries := readShadowLines(t, shadowLog.Path())
	if len(entries) != 1 {
		t.Fatalf("got %d shadow log lines, want 1 run summary", len(entries))
	}
	e := entries[0]
	if e.Type != compaction.EntryTypeHistoryRun {
		t.Errorf("Type = %q, want %q", e.Type, compaction.EntryTypeHistoryRun)
	}
	if e.SegmentID != "" || e.Decision != "" {
		t.Errorf("run summary must carry no decision fields, got segment_id=%q decision=%q", e.SegmentID, e.Decision)
	}
	if e.TS.IsZero() {
		t.Error("run summary TS was not defaulted to now")
	}
	if want := compaction.HashTask("Please analyze this build log."); e.TaskHash != want {
		t.Errorf("TaskHash = %q, want the session task's digest %q", e.TaskHash, want)
	}
	if e.TaskHash != report.TaskHash {
		t.Errorf("TaskHash = %q, want the report's %q", e.TaskHash, report.TaskHash)
	}
	if e.Run == nil {
		t.Fatal("run summary Run is nil")
	}
	want := compaction.RunSummary{
		Scanned:      report.MessagesScanned,
		Scored:       report.MessagesScored,
		Elided:       report.SegmentsElided,
		TokensBefore: report.TokensBefore,
		TokensAfter:  report.TokensAfter,
		TokensSaved:  report.TokensSaved,
	}
	if *e.Run != want {
		t.Errorf("Run = %+v, want %+v", *e.Run, want)
	}
	if e.Run.Shadow {
		t.Error("a mutating run must not report Shadow in its summary")
	}
	if e.Run.Err != "" {
		t.Errorf("a clean run must log no error, got %q", e.Run.Err)
	}
}

// TestHistoryCompactionRunnerLogsFailedRunSummary: a run that fails still
// appends its summary (with the error string), and a shadow run's summary
// says so — the failure must not cost the audit trail.
func TestHistoryCompactionRunnerLogsFailedRunSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	sess := compactionTestSession(t, path)
	store := compaction.NewStore()
	shadowLog, err := compaction.NewShadowLogAt(filepath.Join(t.TempDir(), "shadow.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	runner := historyCompactionRunner(sess, failingHistoryScorer{}, store, true, compaction.DefaultRelocationThreshold, shadowLog)
	report, err := runner(context.Background())
	if err == nil {
		t.Fatal("runner() error = nil, want the scorer failure")
	}

	entries := readShadowLines(t, shadowLog.Path())
	if len(entries) != 1 {
		t.Fatalf("got %d shadow log lines, want 1 run summary even for the failed run", len(entries))
	}
	e := entries[0]
	if e.Type != compaction.EntryTypeHistoryRun || e.Run == nil {
		t.Fatalf("entry = %+v, want a history-run summary", e)
	}
	if !e.Run.Shadow {
		t.Error("a shadow run must report Shadow in its summary")
	}
	if e.Run.Err != err.Error() {
		t.Errorf("Run.Err = %q, want the runner error %q", e.Run.Err, err.Error())
	}
	if e.Run.Scored != report.MessagesScored {
		t.Errorf("Run.Scored = %d, want the report's %d", e.Run.Scored, report.MessagesScored)
	}
}
