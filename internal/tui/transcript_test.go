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
	if !strings.Contains(view, "│") || !strings.Contains(view, "thinking...") {
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
	first := m.renderAnimatedTagAt("thinking...", thoughtHeaderStyle, 80, true, time.UnixMilli(100))
	second := m.renderAnimatedTagAt("thinking...", thoughtHeaderStyle, 80, true, time.UnixMilli(350))
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
	m.Input.SetValue("/new")
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

func TestThinkingToReasoningKeepsLayout(t *testing.T) {
	for _, width := range []int{40, 100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m, s := newViewportBenchmarkModel(benchmarkHistory(2))
			m.SetSize(width, 40)
			s.State = StateThinking
			renderTestTranscript(m)
			before := strings.Split(ansi.Strip(m.transcriptView()), "\n")
			find := func(rows []string, marker string) (int, int) {
				seenThinking := false
				for y, row := range rows {
					if strings.Contains(row, "thinking") {
						seenThinking = true
					}
					if marker == "│" && !seenThinking {
						continue
					}
					if x := strings.Index(row, marker); x >= 0 {
						return y, ansi.StringWidth(row[:x])
					}
				}
				t.Fatalf("missing %q in %q", marker, rows)
				return -1, -1
			}
			headerY, headerX := find(before, "thinking")
			gutterY, gutterX := find(before, "│")
			if gutterY != headerY+1 {
				t.Fatal("placeholder must reserve a reasoning row below its header")
			}

			// Providers may send usage or role metadata before their first text token.
			updated, _ := m.Update(OrchestratorEventMsg{Event: common.ContentEvent{ID: m.Focused.ID()}})
			*m = updated.(Model)
			renderTestTranscript(m)
			if !s.Transcript.thinking {
				t.Fatal("empty streaming event removed placeholder")
			}
			empty := strings.Split(ansi.Strip(m.transcriptView()), "\n")
			if y, x := find(empty, "│"); y != gutterY || x != gutterX {
				t.Fatal("empty event shifted gutter")
			}

			updated, _ = m.Update(OrchestratorEventMsg{Event: common.ContentEvent{ID: m.Focused.ID(), ReasoningContent: "Inspecting the code"}})
			*m = updated.(Model)
			renderTestTranscript(m)
			after := strings.Split(ansi.Strip(m.transcriptView()), "\n")
			if y, x := find(after, "thinking"); y != headerY || x != headerX {
				t.Fatalf("header moved from (%d,%d) to (%d,%d)", headerX, headerY, x, y)
			}
			if y, x := find(after, "│"); y != gutterY || x != gutterX {
				t.Fatalf("gutter moved from (%d,%d) to (%d,%d)", gutterX, gutterY, x, y)
			}
			if y, _ := find(after, "Inspecting the code"); y != gutterY {
				t.Fatal("reasoning did not fill reserved gutter row")
			}
			if s.Transcript.thinking {
				t.Fatal("placeholder survived first reasoning token")
			}
		})
	}
}

func TestAnswerSpacingAndAlignment(t *testing.T) {
	for _, width := range []int{40, 100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m, s := newViewportBenchmarkModel(nil)
			m.SetSize(width, 40)
			s.State = StateStreaming
			s.StreamingState = common.ContentEvent{ReasoningContent: "REASONING", Content: "ANSWER " + strings.Repeat("wrapped text ", 20)}
			rows := strings.Split(ansi.Strip(testTranscriptContent(m)), "\n")
			reason, answer := -1, -1
			for i, row := range rows {
				if strings.Contains(row, "REASONING") {
					reason = i
				}
				if strings.Contains(row, "ANSWER") {
					answer = i
				}
			}
			if reason < 0 || answer != reason+2 {
				t.Fatalf("expected one blank line, reasoning=%d answer=%d", reason, answer)
			}
			if ansi.StringWidth(rows[reason][:strings.Index(rows[reason], "REASONING")]) != ansi.StringWidth(rows[answer][:strings.Index(rows[answer], "ANSWER")]) {
				t.Fatalf("answer and reasoning text are misaligned: %q / %q", rows[reason], rows[answer])
			}
			for _, row := range rows[answer:] {
				if !strings.HasPrefix(row, "    ") || !strings.HasSuffix(row, "    ") || ansi.StringWidth(row) != width {
					t.Fatalf("incorrect answer padding: %q", row)
				}
			}
		})
	}
}

func TestSharedActivityAnimationForThinkingAndTools(t *testing.T) {
	m, s := newViewportBenchmarkModel(nil)
	s.State = StateStreaming
	s.StreamingState = common.ContentEvent{ReasoningContent: "Inspecting", ToolCalls: []client.ToolCall{
		{Function: client.FunctionCall{Name: "read_file", Arguments: `{"path":"main.go"}`}},
		{Function: client.FunctionCall{Name: "bash", Arguments: `{"command":"go test"}`}},
	}}
	renderTestTranscript(m)
	if len(s.Transcript.activities) != 2 {
		t.Fatalf("expected two tool animations, got %d", len(s.Transcript.activities))
	}
	rows := s.Transcript.rows
	for line, label := range s.Transcript.activities {
		first := m.renderActivityAt(label, 100, time.UnixMilli(100))
		second := m.renderActivityAt(label, 100, time.UnixMilli(350))
		if first == second {
			t.Fatalf("static activity %q", label)
		}
		a, b := ansi.Strip(first), ansi.Strip(second)
		ar, br := []rune(a), []rune(b)
		if ar[2] == br[2] {
			t.Fatal("dot spinner did not advance")
		}
		if string(ar[3:]) != string(br[3:]) {
			t.Fatal("activity label moved")
		}

		if !strings.Contains(ansi.Strip(rows[line]), strings.TrimSpace(label)) {
			t.Fatal("animation is attached to the wrong row")
		}
		glowA := m.renderAnimatedTagAt(label, thoughtHeaderStyle, 80, true, time.UnixMilli(100))
		glowB := m.renderAnimatedTagAt(label, thoughtHeaderStyle, 80, true, time.UnixMilli(350))
		if glowA == glowB {
			t.Fatal("short activity label has no glow")
		}
	}
	updated, _ := m.Update(spinner.TickMsg{})
	*m = updated.(Model)
	_, worker := m.transcriptFrame()
	if worker != nil || &rows[0] != &s.Transcript.rows[0] {
		t.Fatal("activity tick rerendered transcript")
	}
	s.State = StateIdle
	s.StreamingState = common.ContentEvent{Completed: true}
	renderTestTranscript(m)
	if len(s.Transcript.activities) != 0 {
		t.Fatal("completed activities still animate")
	}
	for _, width := range []int{8, 20, 40} {
		row := m.renderActivityAt("read: a very long path with 漢字 and more text", width, time.UnixMilli(100))
		if strings.Contains(row, "\n") || ansi.StringWidth(row) > width {
			t.Fatalf("activity exceeds width %d", width)
		}
	}
}

func TestReasoningAnimationStopsWhenAnswerStarts(t *testing.T) {
	m, s := newViewportBenchmarkModel(nil)
	s.State = StateStreaming
	s.StreamingState = common.ContentEvent{ReasoningContent: "Inspecting code"}
	renderTestTranscript(m)
	if len(s.Transcript.activities) != 1 {
		t.Fatal("reasoning is not animated")
	}
	s.StreamingState.Content = "Here is the answer"
	renderTestTranscript(m)
	if len(s.Transcript.activities) != 0 {
		t.Fatal("reasoning still animates while writing answer")
	}
}

func TestThinkingSpinnerReplacesCircleUntilFinished(t *testing.T) {
	m, s := newViewportBenchmarkModel(nil)
	s.State = StateThinking
	for _, reasoning := range []string{"", "Inspecting code"} {
		s.StreamingState = common.ContentEvent{ReasoningContent: reasoning}
		renderTestTranscript(m)
		view := ansi.Strip(m.transcriptView())
		if strings.Contains(view, "· thinking") {
			t.Fatal("active thinking shows both spinner and circle")
		}
		found := false
		for _, frame := range spinner.Dot.Frames {
			if strings.Contains(view, strings.TrimSpace(frame)+" thinking...") {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing thinking spinner: %q", view)
		}
	}
	s.StreamingState.Content = "Answer"
	renderTestTranscript(m)
	view := ansi.Strip(m.transcriptView())
	if !strings.Contains(view, "· thinking") {
		t.Fatal("finished reasoning did not restore circle")
	}
	for _, frame := range spinner.Dot.Frames {
		if strings.Contains(view, strings.TrimSpace(frame)) {
			t.Fatal("finished reasoning retained spinner")
		}
	}
}
