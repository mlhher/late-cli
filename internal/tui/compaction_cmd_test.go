package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"late/internal/client"
	"late/internal/common"
	"late/internal/compaction"
	"late/internal/config"
	"late/internal/session"
)

// The compaction pipeline's store is the production session.ElideStore: the
// session's CompactContext shares the pipeline's elide-id space with the
// expand tool through it (cmd/late/main.go passes it as session.ElideStore).
var _ session.ElideStore = (*compaction.Store)(nil)

// pressEnter delivers a terminal Enter keypress to m and returns the
// resulting model. Private copy: the helper lives in an excluded
// feature's test file on the source branch.
func pressEnter(t *testing.T, m Model) Model {
	t.Helper()
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", updated)
	}
	return next
}

// unknownCtxOrchestrator reports an unknown context size (ContextSize -1)
// so the auto-compaction trigger path can be exercised without a real
// window size. Private copy: the helper lives in an excluded feature's
// test file on the source branch.
type unknownCtxOrchestrator struct {
	mockOrchestrator
}

func (m *unknownCtxOrchestrator) MaxTokens() int { return -1 }

// The pipeline's decision client is the production HistoryScorer behind
// Model.Compactor (wired via compaction.Pipeline.HistoryScorer).
var _ session.HistoryScorer = (*compaction.DecisionClient)(nil)

// fakeHistoryScorer scores every segment with one fixed score, standing in
// for the compaction pipeline's decision client in the end-to-end test.
type fakeHistoryScorer struct {
	score float64
	calls int
}

func (f *fakeHistoryScorer) ScoreBatch(_ context.Context, _ string, items map[string]compaction.Item) (map[string]float64, error) {
	f.calls++
	out := make(map[string]float64, len(items))
	for id := range items {
		out[id] = f.score
	}
	return out, nil
}

// okRunner is the no-op CompactionRunner stub: it reports a fixed mutating
// run without touching anything.
func okRunner(report session.CompactionReport) CompactionRunner {
	return func(context.Context) (session.CompactionReport, error) {
		return report, nil
	}
}

func TestJevCompactStoreSharesElideIDSpace(t *testing.T) {
	store := compaction.NewStore()
	var elideStore session.ElideStore = store

	id1 := elideStore.NextID()
	id2 := elideStore.NextID()
	if id1 != "elide-1" || id2 != "elide-2" {
		t.Fatalf("NextID() = %q, %q; want elide-1, elide-2", id1, id2)
	}
	elideStore.Put(id1, "original text")
	if got, ok := store.Get(id1); !ok || got != "original text" {
		t.Fatalf("store.Get(%s) = (%q, %v), want the stored original", id1, got, ok)
	}
}

func TestJevCompactContextListedInAvailableCommands(t *testing.T) {
	found := false
	for _, cmd := range AvailableCommands {
		if cmd.Name == "/jev-compact-context" {
			found = true
			if cmd.Description == "" {
				t.Fatal("/jev-compact-context must carry a description")
			}
		}
	}
	if !found {
		t.Fatal("/jev-compact-context missing from AvailableCommands")
	}
}

func TestJevCompactContextCommandUnavailable(t *testing.T) {
	m := NewModel(&mockOrchestrator{}, nil, &config.Config{})
	m.SetSize(80, 24)
	m.Input.SetValue("/jev-compact-context")
	m = pressEnter(t, m)

	s := m.GetAgentState(m.Focused.ID())
	if s.StatusText != compactionUnavailableStatus {
		t.Fatalf("StatusText = %q, want %q", s.StatusText, compactionUnavailableStatus)
	}
	if m.CompactionRunning {
		t.Fatal("unavailable compaction must not start a run")
	}
}

func TestJevCompactContextCommandDispatchesRun(t *testing.T) {
	m := NewModel(&mockOrchestrator{}, nil, &config.Config{})
	m.SetSize(80, 24)

	runs := 0
	m.Compactor = func(context.Context) (session.CompactionReport, error) {
		runs++
		return session.CompactionReport{TokensSaved: 25, SegmentsElided: 2}, nil
	}

	m.Input.SetValue("/jev-compact-context")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", updated)
	}
	if !next.CompactionRunning {
		t.Fatal("dispatch must set the in-flight guard")
	}
	if cmd == nil {
		t.Fatal("dispatch must return the compaction command")
	}
	if next.Input.Value() != "" {
		t.Fatalf("dispatch must clear the input, got %q", next.Input.Value())
	}
	if runs != 0 {
		t.Fatal("the run must happen off the update loop, not during dispatch")
	}

	// The command runs off-loop; its report lands as a status.
	msg := next.startCompaction()()
	result, ok := msg.(compactionResultMsg)
	if !ok {
		t.Fatalf("command returned %T, want compactionResultMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("runner error = %v", result.err)
	}
	final, _ := next.Update(result)
	done := final.(Model)
	if done.CompactionRunning {
		t.Fatal("the result message must clear the in-flight guard")
	}
	if got := done.GetAgentState(done.Focused.ID()).StatusText; got != "compacted: saved ~25 tokens, 2 segments elided (scored 0/0 messages)… (estimate — the next request's usage refreshes the bar)" {
		t.Fatalf("StatusText = %q, want the saved report", got)
	}
	if runs != 1 {
		t.Fatalf("runner invoked %d times, want 1", runs)
	}
}

func TestCompactionResultStatuses(t *testing.T) {
	cases := []struct {
		name string
		msg  compactionResultMsg
		want string
	}{
		{
			name: "shadow run",
			msg: compactionResultMsg{report: session.CompactionReport{
				ShadowOnly: true, TokensSaved: 500, SegmentsElided: 7,
				MessagesScanned: 8, MessagesScored: 8,
			}},
			want: "shadow report: would save ~500 tokens, 7 segments elided (scored 8/8 messages; enable compaction-mode to apply)",
		},
		{
			name: "scorer failure",
			msg:  compactionResultMsg{err: fmt.Errorf("scorer down")},
			want: "compaction failed after scoring 0/0 messages: scorer down",
		},
		{
			// A mid-walk wholesale failure: the status says how far the
			// scoring got before it stopped (the honesty requirement).
			name: "scorer failure mid-walk",
			msg: compactionResultMsg{
				report: session.CompactionReport{MessagesScanned: 12, MessagesScored: 5},
				err:    fmt.Errorf("scorer down"),
			},
			want: "compaction failed after scoring 5/12 messages: scorer down",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel(&mockOrchestrator{}, nil, &config.Config{})
			m.SetSize(80, 24)
			m.CompactionRunning = true

			updated, _ := m.Update(tc.msg)
			next := updated.(Model)
			if next.CompactionRunning {
				t.Fatal("the result message must clear the in-flight guard")
			}
			if got := next.GetAgentState(next.Focused.ID()).StatusText; got != tc.want {
				t.Fatalf("StatusText = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestJevCompactContextEndToEnd(t *testing.T) {
	// A completing mutating run persists the compaction high-water mark
	// through the session meta sidecar — sandbox SessionDir so this test
	// never writes into the real user sessions directory.
	sessionDir := t.TempDir()
	originalSessionDir := session.SessionDir
	session.SessionDir = func() (string, error) { return sessionDir, nil }
	t.Cleanup(func() { session.SessionDir = originalSessionDir })

	sess := session.New(nil, filepath.Join(t.TempDir(), "history.json"), []client.ChatMessage{
		{Role: "user", Content: client.TextContent("Please analyze this build log.")},
		// The compaction candidate: assistant content annotating a tool
		// call (a pure-prose assistant message is never compacted).
		{
			Role:      "assistant",
			Content:   client.TextContent(strings.Repeat("verbose analysis ", 200)),
			ToolCalls: []client.ToolCall{{Index: 0, ID: "call_1", Type: "function", Function: client.FunctionCall{Name: "Bash", Arguments: `{"cmd":"make build"}`}}},
		},
	}, "system prompt", false)
	scorer := &fakeHistoryScorer{score: 0}
	store := compaction.NewStore()

	m := NewModel(&mockOrchestrator{}, nil, &config.Config{})
	m.SetSize(80, 24)
	m.Compactor = func(ctx context.Context) (session.CompactionReport, error) {
		return sess.CompactContext(ctx, scorer, store, session.CompactionOptions{})
	}

	msg := m.startCompaction()()
	result, ok := msg.(compactionResultMsg)
	if !ok {
		t.Fatalf("command returned %T, want compactionResultMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("CompactContext() error = %v", result.err)
	}
	if result.report.SegmentsElided == 0 || result.report.TokensSaved <= 0 {
		t.Fatalf("expected a real elision, report = %+v", result.report)
	}
	if scorer.calls == 0 {
		t.Fatal("the scorer was never consulted")
	}

	updated, _ := m.Update(result)
	next := updated.(Model)
	want := fmt.Sprintf("compacted: saved ~%d tokens, %d segments elided (scored %d/%d messages)… (estimate — the next request's usage refreshes the bar)",
		result.report.TokensSaved, result.report.SegmentsElided,
		result.report.MessagesScored, result.report.MessagesScanned)
	if got := next.GetAgentState(next.Focused.ID()).StatusText; got != want {
		t.Fatalf("StatusText = %q, want %q", got, want)
	}
	if !strings.Contains(sess.History[1].Content.Text, "[[elided") {
		t.Fatal("compaction must rewrite the assistant message in place with pointers")
	}
}

func TestJevAutoCompactFiresFromUsageUpdate(t *testing.T) {
	m := NewModel(&mockOrchestrator{}, nil, &config.Config{JevAutocompact: true})
	m.SetSize(80, 24)
	m.Compactor = okRunner(session.CompactionReport{TokensSaved: 10})

	updated, cmd := m.Update(OrchestratorEventMsg{Event: common.ContentEvent{
		ID:    m.Focused.ID(),
		Usage: client.Usage{TotalTokens: 100}, // 100% of the mock's 100-token context
	}})
	next := updated.(Model)
	s := next.GetAgentState(next.Focused.ID())
	if !next.CompactionRunning {
		t.Fatal("crossing the threshold with jev-autocompact on must start a compaction")
	}
	if !s.AutocompactDisarmed {
		t.Fatal("firing must disarm the agent until usage drops back")
	}
	if cmd == nil {
		t.Fatal("Update must return the compaction command")
	}
}

func TestJevAutoCompactDisabledNeverFires(t *testing.T) {
	m := NewModel(&mockOrchestrator{}, nil, &config.Config{JevAutocompact: false})
	m.SetSize(80, 24)
	m.Compactor = okRunner(session.CompactionReport{})

	updated, _ := m.Update(OrchestratorEventMsg{Event: common.ContentEvent{
		ID:    m.Focused.ID(),
		Usage: client.Usage{TotalTokens: 100},
	}})
	next := updated.(Model)
	s := next.GetAgentState(next.Focused.ID())
	if next.CompactionRunning || s.AutocompactDisarmed {
		t.Fatal("jev-autocompact off must never fire the trigger")
	}
}

func TestJevAutoCompactNoRunnerNeverFires(t *testing.T) {
	m := NewModel(&mockOrchestrator{}, nil, &config.Config{JevAutocompact: true})
	m.SetSize(80, 24)

	updated, _ := m.Update(OrchestratorEventMsg{Event: common.ContentEvent{
		ID:    m.Focused.ID(),
		Usage: client.Usage{TotalTokens: 100},
	}})
	next := updated.(Model)
	if next.CompactionRunning {
		t.Fatal("without the pipeline (nil Compactor) the trigger must never fire")
	}
}

func TestJevAutoCompactUnknownContextNeverFires(t *testing.T) {
	m := NewModel(&unknownCtxOrchestrator{mockOrchestrator{}}, nil, &config.Config{JevAutocompact: true})
	m.SetSize(80, 24)
	m.Compactor = okRunner(session.CompactionReport{})

	updated, _ := m.Update(OrchestratorEventMsg{Event: common.ContentEvent{
		ID:    m.Focused.ID(),
		Usage: client.Usage{TotalTokens: 10000},
	}})
	next := updated.(Model)
	if next.CompactionRunning {
		t.Fatal("without a known ctx size the trigger must skip silently")
	}
}

func TestJevAutoCompactOncePerCrossingRearms(t *testing.T) {
	m := NewModel(&mockOrchestrator{}, nil, &config.Config{JevAutocompact: true})
	m.SetSize(80, 24)
	m.Compactor = okRunner(session.CompactionReport{})
	s := m.GetAgentState(m.Focused.ID())
	s.CumulativeTokenCount = 100 // 100% of the 100-token context

	if cmd := m.maybeJevAutoCompact(s); cmd == nil {
		t.Fatal("first crossing must fire")
	}
	if !s.AutocompactDisarmed {
		t.Fatal("firing must disarm the agent")
	}

	// Still above the threshold: one crossing fires exactly once.
	m.CompactionRunning = false
	if cmd := m.maybeJevAutoCompact(s); cmd != nil {
		t.Fatal("a crossing must fire only once")
	}

	// Usage falls below the re-arm level (99-9 = 90% → under 90 tokens):
	// the trigger re-arms without firing.
	s.CumulativeTokenCount = 50
	if cmd := m.maybeJevAutoCompact(s); cmd != nil {
		t.Fatal("the re-arm update must not fire (usage below the threshold)")
	}
	if s.AutocompactDisarmed {
		t.Fatal("usage below the re-arm level must re-arm the trigger")
	}

	// The next crossing fires again.
	s.CumulativeTokenCount = 100
	if cmd := m.maybeJevAutoCompact(s); cmd == nil {
		t.Fatal("a re-armed crossing must fire again")
	}
}

func TestNewModelPlumbsAutocompactConfig(t *testing.T) {
	m := NewModel(&mockOrchestrator{}, nil, nil)
	if m.JevAutocompact || m.JevAutocompactPercent != config.DefaultJevAutocompactPercent {
		t.Fatalf("nil config must default to disabled/%d, got %v/%d",
			config.DefaultJevAutocompactPercent, m.JevAutocompact, m.JevAutocompactPercent)
	}

	m = NewModel(&mockOrchestrator{}, nil, &config.Config{JevAutocompact: true, JevAutocompactPercent: 55})
	if !m.JevAutocompact || m.JevAutocompactPercent != 55 {
		t.Fatalf("valid config must plumb through, got %v/%d", m.JevAutocompact, m.JevAutocompactPercent)
	}

	m = NewModel(&mockOrchestrator{}, nil, &config.Config{JevAutocompact: true, JevAutocompactPercent: 250})
	if !m.JevAutocompact || m.JevAutocompactPercent != config.DefaultJevAutocompactPercent {
		t.Fatalf("invalid percent must normalize to %d, got %v/%d",
			config.DefaultJevAutocompactPercent, m.JevAutocompact, m.JevAutocompactPercent)
	}
}

// TestJevAutoCompactPerModelOverrideFiresAtOverride pins the per-model
// override resolution in the trigger itself: a focused typed subagent whose
// agent_models-routed model entry carries jev-autocompact-percent 55 fires
// at 55% of the (mock 100-token) context, not at the global 99.
func TestJevAutoCompactPerModelOverrideFiresAtOverride(t *testing.T) {
	cfg := &config.Config{
		JevAutocompact:        true,
		JevAutocompactPercent: 99, // the global threshold
		Models: []config.ModelSetting{
			{ID: "small-ctx", URL: "http://a:8080", Key: "k", Model: "model-a", JevAutocompactPercent: 55},
		},
		AgentModels: map[string]string{"researcher": "small-ctx"},
	}
	m := NewModel(&mockOrchestrator{}, nil, cfg)
	m.SetSize(80, 24)
	m.Compactor = okRunner(session.CompactionReport{})
	// Focus a typed subagent: agentTypeForID maps this id to "researcher".
	m.Focused = &typedOrchestrator{mockOrchestrator{}, "researcher-subagent-0"}

	// 54 of 100 tokens: below the 55% override — no fire.
	m.GetAgentState(m.Focused.ID()).CumulativeTokenCount = 54
	if cmd := m.maybeJevAutoCompact(m.GetAgentState(m.Focused.ID())); cmd != nil {
		t.Fatal("55% override must not fire at 54% usage")
	}

	// 55 of 100 tokens: exactly the override threshold — fire.
	m.GetAgentState(m.Focused.ID()).CumulativeTokenCount = 55
	if cmd := m.maybeJevAutoCompact(m.GetAgentState(m.Focused.ID())); cmd == nil {
		t.Fatal("55% override must fire at exactly 55% usage")
	}
	if !m.CompactionRunning {
		t.Fatal("firing must set CompactionRunning")
	}
}

// TestJevAutoCompactPerModelOverrideRootAgent pins the same resolution for
// the root agent: agentTypeForID("main") maps to "orchestrator", whose
// agent_models entry carries the override.
func TestJevAutoCompactPerModelOverrideRootAgent(t *testing.T) {
	cfg := &config.Config{
		JevAutocompact:        true,
		JevAutocompactPercent: 99,
		Models: []config.ModelSetting{
			{ID: "big-ctx", URL: "http://a:8080", Key: "k", Model: "model-a", JevAutocompactPercent: 80},
		},
		AgentModels: map[string]string{"orchestrator": "big-ctx"},
	}
	m := NewModel(&mockOrchestrator{}, nil, cfg)
	m.SetSize(80, 24)
	m.Compactor = okRunner(session.CompactionReport{})
	// Focus the root as the real TUI does (the root's ID maps to
	// "orchestrator" through agentTypeForID).
	m.Focused = &typedOrchestrator{mockOrchestrator{}, common.MainAgentID}

	// 70 of 100 tokens is below the 80% override but would already have
	// fired under the 99% global.
	m.GetAgentState(m.Focused.ID()).CumulativeTokenCount = 70
	if cmd := m.maybeJevAutoCompact(m.GetAgentState(m.Focused.ID())); cmd != nil {
		t.Fatal("80% root override must not fire at 70% usage")
	}
	m.GetAgentState(m.Focused.ID()).CumulativeTokenCount = 80
	if cmd := m.maybeJevAutoCompact(m.GetAgentState(m.Focused.ID())); cmd == nil {
		t.Fatal("80% root override must fire at exactly 80% usage")
	}
}

// TestJevAutoCompactFallsBackToGlobalWithoutOverride pins the fallback: when
// the focused agent's model entry has no jev-autocompact-percent (or no
// entry applies at all), the trigger uses the global percent — 99 here, so
// 55% usage must not fire.
func TestJevAutoCompactFallsBackToGlobalWithoutOverride(t *testing.T) {
	cfg := &config.Config{
		JevAutocompact:        true,
		JevAutocompactPercent: 99,
		Models: []config.ModelSetting{
			{ID: "plain", URL: "http://a:8080", Key: "k", Model: "model-a"},
		},
		AgentModels: map[string]string{"researcher": "plain"},
	}
	m := NewModel(&mockOrchestrator{}, nil, cfg)
	m.SetSize(80, 24)
	m.Compactor = okRunner(session.CompactionReport{})
	m.Focused = &typedOrchestrator{mockOrchestrator{}, "researcher-subagent-0"}
	m.GetAgentState(m.Focused.ID()).CumulativeTokenCount = 55
	if cmd := m.maybeJevAutoCompact(m.GetAgentState(m.Focused.ID())); cmd != nil {
		t.Fatal("without an override the global 99% must apply: no fire at 55%")
	}

	// A config with models but no agent_models routing falls back too.
	cfgNoRouting := &config.Config{
		JevAutocompact:        true,
		JevAutocompactPercent: 99,
		Models: []config.ModelSetting{
			{ID: "overridden", URL: "http://a:8080", Key: "k", Model: "model-a", JevAutocompactPercent: 55},
		},
	}
	m = NewModel(&mockOrchestrator{}, nil, cfgNoRouting)
	m.SetSize(80, 24)
	m.Compactor = okRunner(session.CompactionReport{})
	m.Focused = &typedOrchestrator{mockOrchestrator{}, "researcher-subagent-0"}
	m.GetAgentState(m.Focused.ID()).CumulativeTokenCount = 55
	if cmd := m.maybeJevAutoCompact(m.GetAgentState(m.Focused.ID())); cmd != nil {
		t.Fatal("an unrouted model's override must never apply: no fire at 55%")
	}
}
