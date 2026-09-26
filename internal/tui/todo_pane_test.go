package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"late/internal/client"
	"late/internal/common"
	"late/internal/orchestrator"
	"late/internal/session"
	"late/internal/tool"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestWrapTodoText(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		maxLen int
		want   []string
	}{
		{
			name:   "short text fits on single line",
			text:   "Setup Reviewer",
			maxLen: 28,
			want:   []string{"Setup Reviewer"},
		},
		{
			name:   "long text wraps to multiple lines",
			text:   "Setup Implementation Reviewer Subagent Configuration",
			maxLen: 28,
			want: []string{
				"Setup Implementation",
				"Reviewer Subagent",
				"Configuration",
			},
		},
		{
			name:   "empty text",
			text:   "",
			maxLen: 28,
			want:   []string{""},
		},
		{
			name:   "wraps by terminal cell width",
			text:   "界界界 next",
			maxLen: 6,
			want:   []string{"界界界", "next"},
		},
		{
			name:   "does not split emoji grapheme clusters",
			text:   "👩‍💻👩‍💻 done",
			maxLen: 4,
			want:   []string{"👩‍💻👩‍💻", "done"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapTodoText(tt.text, tt.maxLen)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("wrapTodoText(%q, %d) = %q, want %q", tt.text, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestTodoContentLinesAlignWrappedText(t *testing.T) {
	todos := []common.TodoItem{
		{Text: "A task whose description needs to wrap cleanly", Done: false},
	}

	lines := todoContentLines(todos, 24)
	if len(lines) < 2 {
		t.Fatalf("expected wrapped content, got %q", lines)
	}

	firstLine := ansi.Strip(lines[0])
	if !strings.Contains(firstLine, "A task") {
		t.Fatalf("first line does not contain task text: %q", lines[0])
	}
	firstIndent := lipgloss.Width(firstLine) - lipgloss.Width(strings.TrimLeft(firstLine, " 1○✓"))
	continuation := ansi.Strip(lines[1])
	if got := lipgloss.Width(continuation) - lipgloss.Width(strings.TrimLeft(continuation, " ")); got != firstIndent {
		t.Fatalf("continuation indent = %d, want %d; lines: %q", got, firstIndent, lines)
	}
}

func TestTodoContentLinesUseCompactCompletedStyle(t *testing.T) {
	lines := todoContentLines([]common.TodoItem{{Text: "Finished", Done: true}}, 30)
	rendered := strings.Join(lines, "\n")
	if !strings.Contains(rendered, "✓") {
		t.Fatalf("completed item has no check mark: %q", rendered)
	}
	if strings.Contains(rendered, "\x1b[9m") {
		t.Fatalf("completed item should not use strikethrough: %q", rendered)
	}
}

func TestTodoMaxScrollOffset(t *testing.T) {
	todos := make([]common.TodoItem, 10)
	for i := range todos {
		todos[i] = common.TodoItem{Text: "A short task"}
	}
	content := todoContentLines(todos, todoPaneWidth-1)
	height := 7
	want := len(content) - (height - 2)
	if want < 0 {
		want = 0
	}
	if got := todoMaxScrollOffsetFor(todos, height); got != want {
		t.Fatalf("max scroll offset = %d, want %d", got, want)
	}
}

// newTodoPaneStartupModel builds a Model whose root registers a list_todos
// provider. Callers mirror the startup sequence in cmd/late/main.go: set
// ShowTodoPane from the resolved "show-todo-pane" config BEFORE the initial
// SetSize, whose updateLayout reserves the side-pane width at >= 85 columns
// and silently closes the pane again below that threshold.
func newTodoPaneStartupModel(t *testing.T, todos []tool.Todo) Model {
	t.Helper()
	// A real (offline) client keeps BaseOrchestrator.MaxTokens() happy while
	// buildScreen renders the status bar; session.New(nil, ...) would leave
	// Client() nil and panic on the first View().
	sess := session.New(client.NewClient(client.Config{}), "", nil, "", false)
	sess.Registry.Register(tool.ListTodosTool{Todos: &todos})
	root := orchestrator.NewBaseOrchestrator(common.MainAgentID, sess, nil, 0)
	return NewModel(root, nil, nil)
}

func TestTodoPaneOpenByDefaultAtStartup(t *testing.T) {
	todos := []tool.Todo{
		{Text: "Scaffold the parser"},
		{Text: "Wire the CLI flags", Done: true},
	}
	m := newTodoPaneStartupModel(t, todos)
	m.ShowTodoPane = true // resolved default: show-todo-pane absent -> open
	m.SetSize(100, 30)

	if !m.ShowTodoPane {
		t.Fatal("todos pane should stay open at 100 cols")
	}
	if got := m.Viewport.Width(); got != 100-todoPaneWidth {
		t.Fatalf("viewport width = %d, want %d (todos pane width reserved)", got, 100-todoPaneWidth)
	}
	screen := ansi.Strip(m.View().Content)
	if !strings.Contains(screen, "Todos") {
		t.Fatalf("todos pane header missing from startup screen: %q", screen)
	}
	if !strings.Contains(screen, "Scaffold the parser") {
		t.Fatalf("todo content missing from startup screen: %q", screen)
	}
}

func TestTodoCommandTogglesStartupPaneOff(t *testing.T) {
	m := newTodoPaneStartupModel(t, nil)
	m.ShowTodoPane = true
	m.SetSize(100, 30)

	m.Input.SetValue("/todos")
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(Model)

	if m.ShowTodoPane {
		t.Fatal("/todos should close the pane that starts open by default")
	}
	if got := m.Viewport.Width(); got != 100 {
		t.Fatalf("viewport width = %d, want full %d after closing the pane", got, 100)
	}
	if screen := ansi.Strip(m.View().Content); strings.Contains(screen, "No todos created yet.") {
		t.Fatal("todos pane still rendered after /todos")
	}
}

func TestTodoPaneStartupWidthGuardIsSilent(t *testing.T) {
	m := newTodoPaneStartupModel(t, nil)
	// Resolved default is open, but the terminal is narrower than the
	// 85-column pane threshold: SetSize -> updateLayout closes the pane
	// silently instead of toasting at startup (unlike /todos on a narrow
	// terminal, which toasts). The user can /todos it open later.
	m.ShowTodoPane = true
	m.SetSize(84, 30)

	if m.ShowTodoPane {
		t.Fatal("todos pane must start closed below 85 cols")
	}
	if m.ToastMessage != "" {
		t.Fatalf("startup width guard must be silent, got toast %q", m.ToastMessage)
	}
	if got := m.Viewport.Width(); got != 84 {
		t.Fatalf("viewport width = %d, want full %d", got, 84)
	}
	if screen := ansi.Strip(m.View().Content); strings.Contains(screen, "No todos created yet.") {
		t.Fatalf("todos pane rendered despite narrow terminal: %q", screen)
	}
}

// newFocusedTodoPaneModel returns a model with the todo pane open and focused,
// plus the same model in the unfocused state, at a terminal size where the
// pane is reserved (>= 85 columns).
func newFocusedTodoPaneModel(t *testing.T, todos []tool.Todo) (focused, unfocused Model, focusedView, unfocusedView string) {
	t.Helper()
	m := newTodoPaneStartupModel(t, todos)
	m.ShowTodoPane = true
	m.SetSize(100, 30)

	unfocused = m
	unfocusedView = m.todoPaneView(m.Viewport.Height())

	m.TodoPaneFocused = true
	focused = m
	focusedView = m.todoPaneView(m.Viewport.Height())

	return focused, unfocused, focusedView, unfocusedView
}

func TestTodoPaneFocusedRenderDiffersFromUnfocused(t *testing.T) {
	_, _, focused, unfocused := newFocusedTodoPaneModel(t, []tool.Todo{{Text: "Scaffold the parser"}})

	if focused == unfocused {
		t.Fatal("focused todo pane render must differ from the unfocused render")
	}
}

func TestTodoPaneFocusMarker(t *testing.T) {
	_, _, focused, unfocused := newFocusedTodoPaneModel(t, []tool.Todo{{Text: "Scaffold the parser"}})

	// (b) focused render carries the explicit focus/unfocus hint.
	if got := ansi.Strip(focused); !strings.Contains(got, "[focused · esc to unfocus]") {
		t.Fatalf("focused pane missing [focused · esc to unfocus] marker: %q", got)
	}
	// (c) unfocused render carries no focus marker.
	if got := ansi.Strip(unfocused); strings.Contains(got, "[focused") {
		t.Fatalf("unfocused pane must not contain a [focused marker: %q", got)
	}
}

// Truecolor SGR parameter fragments for the theme colors the pane switches
// between (same hardcoded-fragment pattern as theme_test.go):
// appBgColor #0B0C0E, todoFocusedBg #1B1E28, primaryColor #E5A85C.
const (
	todoTestAppBgSgrParams   = "48;2;11;12;14"
	todoTestFocusBgSgrParams = "48;2;27;30;40"
	todoTestBorderSgrParams  = "38;2;229;168;92"
)

func TestTodoPaneFocusedBackgroundAndBorder(t *testing.T) {
	_, _, focused, unfocused := newFocusedTodoPaneModel(t, []tool.Todo{{Text: "Scaffold the parser"}})

	// Focused: elevated background (content lines included) + brighter border.
	if !strings.Contains(focused, todoTestFocusBgSgrParams) {
		t.Fatalf("focused pane does not paint the focused background (#1B1E28): %q", focused)
	}
	if !strings.Contains(focused, todoTestBorderSgrParams) {
		t.Fatalf("focused pane does not use the brighter primary border (#E5A85C): %q", focused)
	}
	if strings.Contains(focused, todoTestAppBgSgrParams) {
		t.Fatalf("focused pane still paints the app background (#0B0C0E): %q", focused)
	}

	// Unfocused: unchanged baseline (app background, dark border).
	if !strings.Contains(unfocused, todoTestAppBgSgrParams) {
		t.Fatalf("unfocused pane does not paint the app background (#0B0C0E): %q", unfocused)
	}
	if strings.Contains(unfocused, todoTestFocusBgSgrParams) {
		t.Fatalf("unfocused pane paints the focused background (#1B1E28): %q", unfocused)
	}
	if strings.Contains(unfocused, todoTestBorderSgrParams) {
		t.Fatalf("unfocused pane uses the primary border (#E5A85C): %q", unfocused)
	}
}

// TestTodoPaneFocusedScreenVTEClean runs the focused pane through the real
// screen pipeline (View -> sanitizeVTE) and reuses validateNoVTELeaks to prove
// every cell — pane background included — carries an explicit background, so
// the focused color survives VTE reset re-assertion.
func TestTodoPaneFocusedScreenVTEClean(t *testing.T) {
	focused, _, _, _ := newFocusedTodoPaneModel(t, []tool.Todo{{Text: "Scaffold the parser"}})

	screen := focused.View().Content
	if !strings.Contains(screen, "[focused · esc to unfocus]") {
		t.Fatalf("focused pane missing from rendered screen: %q", ansi.Strip(screen))
	}
	if !strings.Contains(screen, todoTestFocusBgSgrParams) {
		t.Fatalf("rendered screen does not paint the focused todo pane background: %q", screen)
	}
	validateNoVTELeaks(t, "focused todo pane", screen)
}
