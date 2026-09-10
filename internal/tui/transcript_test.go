package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"late/internal/client"
	"late/internal/common"
)

// Run workers explicitly, as Bubble Tea would, without relying on timers.
func renderTestTranscript(m *Model) {
	m.updateViewport()
	for cmd := m.renderTranscriptCmd(); cmd != nil; cmd = m.renderTranscriptCmd() {
		m.applyTranscript(cmd().(transcriptRenderedMsg))
	}
}
func testTranscriptContent(m *Model) string {
	renderTestTranscript(m)
	return strings.Join(m.GetAgentState(m.Focused.ID()).Transcript.rows, "\n")
}

func TestTranscriptScrollingPreservesActiveResponse(t *testing.T) {
	m, s := newViewportBenchmarkModel(benchmarkHistory(100))
	s.State = StateStreaming
	var lines []string
	for i := 0; i < 500; i++ {
		lines = append(lines, fmt.Sprintf("ACTIVELINE%04d", i))
	}
	s.StreamingState = common.ContentEvent{ID: m.Focused.ID(), Content: strings.Join(lines, "\n")}
	content := testTranscriptContent(m)
	for _, marker := range []string{"ACTIVELINE0000", "ACTIVELINE0499"} {
		if !strings.Contains(content, marker) {
			t.Fatalf("missing %s", marker)
		}
	}
	if got := strings.Count(m.transcriptView(), "\n") + 1; got != m.Viewport.Height() {
		t.Fatalf("visible rows = %d", got)
	}
	m.scrollTranscript(0, -1)
	before := m.transcriptView()
	s.StreamingState.Content += "\nNEWTAIL"
	renderTestTranscript(m)
	if m.transcriptView() != before {
		t.Fatal("streaming moved detached viewport")
	}
	m.scrollTranscript(0, 1)
	if !strings.Contains(m.transcriptView(), "NEWTAIL") {
		t.Fatal("returning to bottom lost active tail")
	}
}

func TestTranscriptHistoryMutationAndIdleCache(t *testing.T) {
	m, s := newViewportBenchmarkModel([]client.ChatMessage{{Role: "assistant", Content: client.TextContent("OLDHISTORYCONTENT")}})
	old := s.Transcript.cache[cacheKeyForTest(s)]
	updated, _ := m.Update(OrchestratorEventMsg{Event: common.StatusEvent{ID: m.Focused.ID(), Status: "idle"}})
	*m = updated.(Model)
	renderTestTranscript(m)
	if &old[0] != &s.Transcript.cache[cacheKeyForTest(s)][0] {
		t.Fatal("idle discarded cached message rows")
	}
	m.Focused.(*benchmarkOrchestrator).history[0].Content = client.TextContent("NEWHISTORYCONTENT")
	content := testTranscriptContent(m)
	if !strings.Contains(content, "NEWHISTORYCONTENT") || strings.Contains(content, "OLDHISTORYCONTENT") {
		t.Fatal("history rewrite retained stale content")
	}
}
func cacheKeyForTest(s *AppState) string {
	for key := range s.Transcript.cache {
		return key
	}
	return ""
}

func TestTranscriptFocusSwitch(t *testing.T) {
	root := &focusTestOrchestrator{id: "root", history: benchmarkHistory(100)}
	child := &focusTestOrchestrator{id: "child", parent: root, history: []client.ChatMessage{{Role: "assistant", Content: client.TextContent("CHILDHISTORYMARKER")}}}
	root.children = []common.Orchestrator{child}
	m := NewModel(root, nil, nil)
	m.SetSize(100, 40)
	renderTestTranscript(&m)
	m.scrollTranscript(0, -1)
	m.Focused = child
	s := m.GetAgentState(child.ID())
	s.State = StateStreaming
	s.StreamingState = common.ContentEvent{ID: child.ID(), Content: "child streaming"}
	renderTestTranscript(&m)
	if !strings.Contains(m.transcriptView(), "CHILDHISTORYMARKER") {
		t.Fatal("switch retained root viewport")
	}
}

func TestTranscriptLazyHistoryLoading(t *testing.T) {
	m := NewModel(&benchmarkOrchestrator{history: benchmarkHistory(20)}, nil, nil)
	m.LazyHistory = true
	m.SetSize(100, 15)
	cmd := m.renderTranscriptCmd()
	if cmd == nil {
		t.Fatal("no initial worker")
	}
	result := cmd().(transcriptRenderedMsg)
	if !result.partial || len(result.blocks) != 4 || result.blocks[0].MessageIndex != 16 {
		t.Fatal("initial worker did not render just the tail")
	}
	m.applyTranscript(result)
	m.scrollTranscript(0, -1)
	before := m.transcriptView()
	cmd = m.renderTranscriptCmd()
	if cmd == nil {
		t.Fatal("no background history worker")
	}
	result = cmd().(transcriptRenderedMsg)
	m.applyTranscript(result)
	if result.partial || len(result.blocks) != 20 {
		t.Fatal("background worker lost history")
	}
	if m.transcriptView() != before {
		t.Fatal("loading earlier history moved visible passage")
	}
}

func TestThinkingAnimationUsesGutterWithoutRerenderingHistory(t *testing.T) {
	m, s := newViewportBenchmarkModel(benchmarkHistory(4))
	s.State = StateThinking
	renderTestTranscript(m)
	if !s.Transcript.thinking {
		t.Fatal("missing animated thinking row")
	}
	view := ansi.Strip(m.transcriptView())
	if !strings.Contains(view, "│") || !strings.Contains(view, "· thinking...") {
		t.Fatalf("missing thinking gutter: %s", view)
	}
	rows := s.Transcript.rows
	updated, cmd := m.Update(spinner.TickMsg{})
	*m = updated.(Model)
	if cmd == nil || !m.screenDirty {
		t.Fatal("spinner did not schedule presentation")
	}
	_, worker := m.transcriptFrame()
	if worker != nil || &rows[0] != &s.Transcript.rows[0] {
		t.Fatal("animation rerendered transcript")
	}
	first := m.renderAnimatedTagAt("· thinking...", thinkingStyle, 80, true, time.UnixMilli(100))
	second := m.renderAnimatedTagAt("· thinking...", thinkingStyle, 80, true, time.UnixMilli(350))
	if first == second {
		t.Fatal("thinking animation is static")
	}
	if ansi.Strip(first) != ansi.Strip(second) {
		t.Fatal("animation shifts thinking block")
	}
	s.State = StateStreaming
	s.StreamingState = common.ContentEvent{Content: "response"}
	renderTestTranscript(m)
	if s.Transcript.thinking {
		t.Fatal("thinking placeholder survived response")
	}
}

func TestTranscriptRejectsStaleWorkerAfterNew(t *testing.T) {
	orch := &mockOrchestrator{history: benchmarkHistory(10)}
	m := NewModel(orch, nil, nil)
	m.SetSize(100, 40)
	stale := m.renderTranscriptCmd()().(transcriptRenderedMsg)
	m.Input.SetValue("> /new")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	orch.history = nil // The shared mock Reset is a no-op.
	renderTestTranscript(&m)
	before := m.transcriptView()
	m.applyTranscript(stale)
	if m.transcriptView() != before {
		t.Fatal("old worker restored pre-reset conversation")
	}
	if m.GetAgentState(m.Focused.ID()).Transcript.offset != 0 {
		t.Fatal("new conversation retained scroll offset")
	}
}

func BenchmarkTranscriptScroll(b *testing.B) {
	m, _ := newViewportBenchmarkModel(benchmarkHistory(1000))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.scrollTranscript(-1, 0)
		_ = m.transcriptView()
	}
}
