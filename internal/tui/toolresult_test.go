package tui

import (
	"fmt"
	"strings"
	"testing"

	"late/internal/client"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestSummarizeToolOutcome(t *testing.T) {
	const editArgs = `{"file":"f.go","search":"a\nb\nc","replace":"a\nB\nc\nd"}`
	tests := []struct {
		name, tool, args, result string
		failed, denied           bool
		summary                  string
	}{
		{name: "bash output", tool: "bash", result: "one\ntwo\nthree", summary: "3 lines"},
		{name: "bash single line", tool: "bash", result: "ok\n", summary: "1 line"},
		{name: "bash silent", tool: "bash", result: "\n", summary: "no output"},
		{name: "bash binary", tool: "bash", result: "(binary output detected)", summary: "binary output"},
		{name: "bash truncated", tool: "bash", result: "a\nb\n... (output truncated)", summary: "2 lines, truncated"},
		{
			name: "bash failure shows the verdict line", tool: "bash", failed: true,
			result:  "Command failed with exit code 1\n--- FAIL: TestX\nFAIL\nFAIL\tlate/internal/tui\t0.02s\n",
			summary: "exit 1 · FAIL late/internal/tui 0.02s",
		},
		{
			name: "bash failure without output", tool: "bash", failed: true,
			result: "Command failed with exit code 127", summary: "exit 127",
		},
		{
			name: "bash failure strips the command's own colours", tool: "bash", failed: true,
			result: "Command failed with exit code 2\n\x1b[31merror:\x1b[0m nope", summary: "exit 2 · error: nope",
		},
		{name: "bash could not start", tool: "bash", failed: true, result: "Error executing command: fork/exec: no such file\n", summary: "fork/exec: no such file"},
		{name: "denied", tool: "bash", denied: true, result: "Tool execution cancelled by user", summary: "denied"},
		{
			name: "denied with reason", tool: "bash", denied: true,
			result:  "Tool execution cancelled by user. The user said: use make clean\nthanks",
			summary: "denied · “use make clean”",
		},
		{name: "executor error", tool: "target_edit", failed: true, result: "Error executing tool target_edit: search block not found in file", summary: "search block not found in file"},
		{name: "unknown tool", tool: "nope", failed: true, result: "Error: tool 'nope' not found", summary: "tool 'nope' not found"},
		{name: "tool-reported error", tool: "create_todos", failed: true, result: "Error: todos array cannot be empty.", summary: "todos array cannot be empty."},
		{name: "edit stats", tool: "target_edit", args: editArgs, result: "Successfully applied edit to f.go", summary: "+2 -1"},
		{name: "edit with unreadable args", tool: "target_edit", args: "{", result: "Successfully applied edit to f.go", summary: "edited"},
		{name: "write", tool: "write_file", args: `{"path":"p","content":"a\nb\nc\n"}`, result: "Successfully wrote to p", summary: "wrote 3 lines"},
		{name: "read", tool: "read_file", result: "l1\nl2\n", summary: "2 lines"},
		{name: "search hit", tool: "search_content", result: "a.go:1: x\nb.go:2: x\n", summary: "2 lines"},
		{name: "search miss", tool: "search_content", result: "No matches found for 'x'. Consider widening 'path'.", summary: "no matches"},
		{name: "free-form report", tool: "spawn_subagent", result: "## **Findings**\n\nlots of text", summary: "Findings"},
		{name: "empty result", tool: "finish_todo", result: "  \n", summary: "done"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := summarizeToolOutcome(tc.tool, tc.args, tc.result)
			if got.summary != tc.summary || got.failed != tc.failed || got.denied != tc.denied {
				t.Fatalf("got %+v, want summary=%q failed=%v denied=%v", got, tc.summary, tc.failed, tc.denied)
			}
		})
	}
}

func TestToolBadgeWithOutcomeFitsEveryWidth(t *testing.T) {
	label := "Executing: go test -race ./internal/tui/... ./internal/tool/..."
	outcomes := []toolOutcome{
		{summary: "+12 -3"},
		{failed: true, summary: "exit 1 · FAIL late/internal/tui 0.02s"},
		{denied: true, summary: "denied · “use make clean instead”"},
	}
	marks := []string{"✓", "✗", "⊘"}
	for oi, o := range outcomes {
		for width := 12; width <= 140; width++ {
			out := toolBadgeWithOutcome(label, o, false, width)
			plain := ansi.Strip(out)
			if w := lipgloss.Width(out); w > width {
				t.Fatalf("outcome %d width %d: row is %d cells: %q", oi, width, w, plain)
			}
			if !strings.Contains(plain, marks[oi]) {
				t.Fatalf("outcome %d width %d: success/failure mark lost: %q", oi, width, plain)
			}
		}
	}
}

func TestToolBadgeWithOutcomeKeepsSummaryBeforeLabel(t *testing.T) {
	label := "Executing: " + strings.Repeat("x", 80)
	got := ansi.Strip(toolBadgeWithOutcome(label, toolOutcome{summary: "+2 -1"}, false, 60))
	if !strings.Contains(got, "+2 -1") {
		t.Fatalf("short summary should survive a long label: %q", got)
	}
	if !strings.Contains(got, "...") {
		t.Fatalf("long label should be shortened: %q", got)
	}
}

func TestToolBadgeWithOutcomeShowsExpansionState(t *testing.T) {
	if got := ansi.Strip(toolBadgeWithOutcome("x", toolOutcome{summary: "ok"}, false, 80)); !strings.Contains(got, "▸") {
		t.Errorf("collapsed badge should show ▸: %q", got)
	}
	if got := ansi.Strip(toolBadgeWithOutcome("x", toolOutcome{summary: "ok"}, true, 80)); !strings.Contains(got, "▾") {
		t.Errorf("expanded badge should show ▾: %q", got)
	}
}

func TestPlainDetailRowsKeepsHeadAndTailOfLongOutput(t *testing.T) {
	var lines []string
	for i := range 200 {
		lines = append(lines, fmt.Sprintf("line %03d", i))
	}
	lines = append(lines, "FAIL")
	rows := plainDetailRows(strings.Join(lines, "\n"), 40)
	if len(rows) > maxToolDetailRows {
		t.Fatalf("%d rows, want at most %d", len(rows), maxToolDetailRows)
	}
	joined := ansi.Strip(strings.Join(rows, "\n"))
	for _, want := range []string{"line 000", "FAIL", "lines hidden"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "line 100") {
		t.Errorf("middle of the output should be elided:\n%s", joined)
	}
}

func TestPlainDetailRowsWrapsStripsColoursAndExpandsTabs(t *testing.T) {
	rows := plainDetailRows("\x1b[31m"+strings.Repeat("a", 50)+"\x1b[0m\n\tindented", 20)
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 3 wrapped + 1: %q", len(rows), rows)
	}
	for _, r := range rows {
		if strings.Contains(ansi.Strip(r), "\t") || strings.Contains(ansi.Strip(r), "\x1b") {
			t.Errorf("raw control characters survived: %q", r)
		}
		if lipgloss.Width(r) > 20 {
			t.Errorf("row too wide (%d): %q", lipgloss.Width(r), ansi.Strip(r))
		}
	}
}

func TestToolDetailRowsForEditsAreADiff(t *testing.T) {
	rows := toolDetailRows("target_edit", `{"file":"f","search":"a\nb","replace":"a\nB"}`, "Successfully applied edit", 60)
	got := ansi.Strip(strings.Join(rows, "\n"))
	if !strings.Contains(got, "- b") || !strings.Contains(got, "+ B") {
		t.Fatalf("expected a diff, got:\n%s", got)
	}
	for _, r := range rows {
		if lipgloss.Width(r) != 60 {
			t.Errorf("diff row is %d cells, want the full 60", lipgloss.Width(r))
		}
	}
}

// --- transcript integration -------------------------------------------------

func tcall(id, name, args string) client.ToolCall {
	return client.ToolCall{ID: id, Function: client.FunctionCall{Name: name, Arguments: args}}
}

func tresult(id, content string) client.ChatMessage {
	return client.ChatMessage{Role: "tool", ToolCallID: id, Content: client.TextContent(content)}
}

func resultsHistory() []client.ChatMessage {
	return []client.ChatMessage{
		{Role: "user", Content: client.TextContent("fix the failing test")},
		{Role: "assistant", Content: client.TextContent("On it."), ToolCalls: []client.ToolCall{
			tcall("c1", "target_edit", `{"file":"f.go","search":"b := 2","replace":"b := 20"}`),
			tcall("c2", "bash", `{"command":"go test ./..."}`),
			tcall("c3", "spawn_subagent", `{"goal":"map it","agent_type":"researcher"}`),
		}},
		tresult("c1", "Successfully applied edit to f.go"),
		tresult("c2", "Command failed with exit code 1\nFAIL\tlate/internal/tui"),
		tresult("c3", "## Findings\n\n- renderer lives in view.go\n- rows are cached\n"),
		{Role: "assistant", Content: client.TextContent("All done.")},
	}
}

func TestTranscriptShowsOutcomeNextToEachToolCall(t *testing.T) {
	m, _ := newViewportBenchmarkModel(resultsHistory())
	view := ansi.Strip(testTranscriptContent(m))
	for _, want := range []string{"✓ +1 -1", "✗ exit 1 · FAIL late/internal/tui", "✓ Findings"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q in:\n%s", want, view)
		}
	}
	// Collapsed: none of the detail is on screen.
	for _, hidden := range []string{"rows are cached", "- b := 2"} {
		if strings.Contains(view, hidden) {
			t.Errorf("collapsed transcript leaked detail %q:\n%s", hidden, view)
		}
	}
}

func TestExpandAllShowsDiffOutputAndRenderedReport(t *testing.T) {
	m, _ := newViewportBenchmarkModel(resultsHistory())
	m.SetSize(100, 60)
	m.ShowToolOutput = true
	view := ansi.Strip(testTranscriptContent(m))
	for _, want := range []string{"- b := 2", "+ b := 20", "Command failed with exit code 1", "• rows are cached", "▾"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q in:\n%s", want, view)
		}
	}
	if strings.Contains(view, "- renderer") || strings.Contains(view, "## Findings") {
		t.Errorf("the report should be rendered as Markdown, not shown raw:\n%s", view)
	}
}

func TestToolRowsPointAtTheBadgeRows(t *testing.T) {
	for _, expanded := range []bool{false, true} {
		m, s := newViewportBenchmarkModel(resultsHistory())
		m.SetSize(100, 60)
		m.ShowToolOutput = expanded
		renderTestTranscript(m)

		want := map[string]string{"c1": "+1 -1", "c2": "exit 1", "c3": "Findings"}
		if len(s.Transcript.toolRows) != len(want) {
			t.Fatalf("expanded=%v: toolRows = %v", expanded, s.Transcript.toolRows)
		}
		for row, id := range s.Transcript.toolRows {
			plain := ansi.Strip(s.Transcript.rows[row])
			if !strings.Contains(plain, "↳") || !strings.Contains(plain, want[id]) {
				t.Errorf("expanded=%v: row %d for %s is %q", expanded, row, id, plain)
			}
		}
	}
}

func TestActivityRowStaysOnTheRunningCallBesideExpandedOnes(t *testing.T) {
	history := []client.ChatMessage{
		{Role: "user", Content: client.TextContent("go")},
		{Role: "assistant", ToolCalls: []client.ToolCall{
			tcall("done", "bash", `{"command":"ls"}`),
			tcall("busy", "bash", `{"command":"sleep 99"}`),
		}},
		tresult("done", "a\nb\nc\nd\ne"),
	}
	m, s := newViewportBenchmarkModel(history)
	m.SetSize(100, 60)
	m.ShowToolOutput = true // the finished call now spans several rows
	s.State = StateThinking
	renderTestTranscript(m)

	if len(s.Transcript.activities) != 1 {
		t.Fatalf("activities = %v, want exactly the running call", s.Transcript.activities)
	}
	for row := range s.Transcript.activities {
		if plain := ansi.Strip(s.Transcript.rows[row]); !strings.Contains(plain, "running") {
			t.Fatalf("activity animation is aimed at row %d = %q, not the running call", row, plain)
		}
	}
	if len(s.Transcript.toolRows) != 1 {
		t.Fatalf("toolRows = %v, want only the finished call", s.Transcript.toolRows)
	}
}

func TestClickingAToolRowTogglesJustThatCall(t *testing.T) {
	m, s := newViewportBenchmarkModel(resultsHistory())
	m.SetSize(100, 60)
	renderTestTranscript(m)

	var row int
	for r, id := range s.Transcript.toolRows {
		if id == "c2" {
			row = r
		}
	}
	click := tea.MouseClickMsg(tea.Mouse{X: 10, Y: row - s.Transcript.offset, Button: tea.MouseLeft})

	updated, _ := m.Update(click)
	*m = updated.(Model)
	view := ansi.Strip(testTranscriptContent(m))
	if !strings.Contains(view, "Command failed with exit code 1") {
		t.Fatalf("click did not expand the call:\n%s", view)
	}
	if strings.Contains(view, "- b := 2") || strings.Contains(view, "rows are cached") {
		t.Fatalf("click expanded more than the clicked call:\n%s", view)
	}

	// The row itself moved nothing above it, so the same position collapses it again.
	s = m.GetAgentState(m.Focused.ID())
	updated, _ = m.Update(tea.MouseClickMsg(tea.Mouse{X: 10, Y: row - s.Transcript.offset, Button: tea.MouseLeft}))
	*m = updated.(Model)
	if view := ansi.Strip(testTranscriptContent(m)); strings.Contains(view, "Command failed with exit code 1") {
		t.Fatalf("second click did not collapse the call:\n%s", view)
	}
}

func TestClickingElsewhereDoesNotToggle(t *testing.T) {
	m, s := newViewportBenchmarkModel(resultsHistory())
	m.SetSize(100, 60)
	renderTestTranscript(m)
	updated, _ := m.Update(tea.MouseClickMsg(tea.Mouse{X: 10, Y: 0, Button: tea.MouseLeft}))
	*m = updated.(Model)
	if len(m.GetAgentState(m.Focused.ID()).ToggledTools) != 0 || len(s.ToggledTools) != 0 {
		t.Fatalf("a click outside any tool row toggled something: %v", s.ToggledTools)
	}
}

func TestCtrlRTogglesAllAndResetsIndividualToggles(t *testing.T) {
	m, s := newViewportBenchmarkModel(resultsHistory())
	m.SetSize(100, 60)
	renderTestTranscript(m)
	m.toggleToolDetail(s, "c1")

	ctrlR := tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl})
	updated, _ := m.Update(ctrlR)
	*m = updated.(Model)
	if !m.ShowToolOutput || len(m.GetAgentState(m.Focused.ID()).ToggledTools) != 0 {
		t.Fatalf("ctrl+r: ShowToolOutput=%v toggled=%v", m.ShowToolOutput, s.ToggledTools)
	}
	if !strings.Contains(m.ToastMessage, "expanded") {
		t.Errorf("toast = %q", m.ToastMessage)
	}
	if !strings.Contains(ansi.Strip(testTranscriptContent(m)), "- b := 2") {
		t.Error("ctrl+r did not expand every call")
	}

	// With everything expanded, a per-call toggle collapses just that call.
	m.toggleToolDetail(s, "c1")
	view := ansi.Strip(testTranscriptContent(m))
	if strings.Contains(view, "- b := 2") || !strings.Contains(view, "Command failed with exit code 1") {
		t.Errorf("per-call toggle under expand-all misbehaved:\n%s", view)
	}

	updated, _ = m.Update(ctrlR)
	*m = updated.(Model)
	if m.ShowToolOutput || !strings.Contains(m.ToastMessage, "collapsed") {
		t.Errorf("second ctrl+r: ShowToolOutput=%v toast=%q", m.ShowToolOutput, m.ToastMessage)
	}
}

func TestCtrlRIsIgnoredOutsideChat(t *testing.T) {
	m, _ := newViewportBenchmarkModel(resultsHistory())
	m.Mode = ViewHelp
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	if updated.(Model).ShowToolOutput {
		t.Fatal("ctrl+r changed tool output while the help screen was open")
	}
}

func TestRunningToolWithoutResultKeepsItsPlainBadge(t *testing.T) {
	history := []client.ChatMessage{
		{Role: "user", Content: client.TextContent("go")},
		{Role: "assistant", ToolCalls: []client.ToolCall{tcall("x", "bash", `{"command":"sleep 5"}`)}},
	}
	m, s := newViewportBenchmarkModel(history)
	s.State = StateIdle // e.g. the run was stopped before the tool answered
	view := ansi.Strip(testTranscriptContent(m))
	if strings.ContainsAny(view, "✓✗⊘") {
		t.Fatalf("a call without a result must not claim an outcome:\n%s", view)
	}
	if len(s.Transcript.toolRows) != 0 {
		t.Fatalf("toolRows = %v", s.Transcript.toolRows)
	}
}

func TestToolResultsAfterStopsAtTheNextNonToolMessage(t *testing.T) {
	history := []client.ChatMessage{
		{Role: "assistant", ToolCalls: []client.ToolCall{tcall("a", "x", "{}"), tcall("b", "x", "{}")}},
		tresult("a", "one"),
		tresult("b", "two"),
		{Role: "assistant", Content: client.TextContent("later")},
		tresult("late", "not mine"),
	}
	got := toolResultsAfter(history, 0)
	if len(got) != 2 || got["a"] != "one" || got["b"] != "two" {
		t.Fatalf("results = %v", got)
	}
	if toolResultsAfter(history, 3)["late"] != "not mine" || toolResultsAfter(history, 4) != nil {
		t.Fatal("scan must start right after the given message and end at the history's end")
	}
}
