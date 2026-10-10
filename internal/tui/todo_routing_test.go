package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"late/internal/common"
	"late/internal/orchestrator"
	"late/internal/session"
	"late/internal/tool"
)

func TestTodoScrollRouting(t *testing.T) {
	tests := []struct {
		name     string
		focus    tea.Msg
		event    tea.Msg
		showPane bool
		wantTodo int
		wantChat int
	}{
		{"focused page down", tea.KeyPressMsg(tea.Key{Code: 't', Mod: tea.ModCtrl}), tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown}), true, 17, 20},
		{"clicked page up", tea.MouseClickMsg(tea.Mouse{X: 99, Y: 1, Button: tea.MouseLeft}), tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp}), true, 3, 20},
		{"focused home", tea.KeyPressMsg(tea.Key{Code: 't', Mod: tea.ModCtrl}), tea.KeyPressMsg(tea.Key{Code: tea.KeyHome}), true, 0, 20},
		{"focused end", tea.KeyPressMsg(tea.Key{Code: 't', Mod: tea.ModCtrl}), tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}), true, 22, 20},
		{"wheel over pane", nil, tea.MouseWheelMsg(tea.Mouse{X: 99, Y: 1, Button: tea.MouseWheelDown}), true, 13, 20},
		{"wheel over chat while pane focused", tea.KeyPressMsg(tea.Key{Code: 't', Mod: tea.ModCtrl}), tea.MouseWheelMsg(tea.Mouse{X: 1, Y: 1, Button: tea.MouseWheelUp}), true, 10, 18},
		{"unfocused page down", nil, tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown}), true, 10, 28},
		{"hidden pane wheel", nil, tea.MouseWheelMsg(tea.Mouse{X: 99, Y: 1, Button: tea.MouseWheelDown}), false, 10, 22},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := session.New(nil, "", nil, "", false)
			todos := make([]tool.Todo, 30)
			for i := range todos {
				todos[i].Text = "Task"
			}
			sess.Registry.Register(tool.ListTodosTool{Todos: &todos})
			root := orchestrator.NewBaseOrchestrator(common.MainAgentID, sess, nil, 0)
			m := NewModel(root, nil, nil)
			m.SetSize(100, 30)
			m.Viewport.SetHeight(10)
			m.ShowTodoPane = tt.showPane
			m.Input.SetValue("")
			if tt.focus != nil {
				updated, _ := m.Update(tt.focus)
				m = updated.(Model)
				if !m.TodoPaneFocused {
					t.Fatal("pane did not gain focus")
				}
			}
			m.TodoScrollOffset = 10
			s := m.GetAgentState(root.ID())
			s.Transcript.rows = make([]string, 100)
			s.Transcript.offset = 20
			s.Transcript.detached = true
			updated, _ := m.Update(tt.event)
			m = updated.(Model)
			if m.TodoScrollOffset != tt.wantTodo {
				t.Fatalf("todo offset = %d, want %d", m.TodoScrollOffset, tt.wantTodo)
			}
			if s.Transcript.offset != tt.wantChat {
				t.Fatalf("chat offset = %d, want %d", s.Transcript.offset, tt.wantChat)
			}
		})
	}
}

func TestTodoCommandResetsInput(t *testing.T) {
	root := orchestrator.NewBaseOrchestrator(common.MainAgentID, session.New(nil, "", nil, "", false), nil, 0)
	m := NewModel(root, nil, nil)
	m.SetSize(100, 30)

	m.Input.SetValue("/todos")
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	newModel := updated.(Model)

	if got := newModel.Input.Value(); got != "" {
		t.Fatalf("expected input to be reset to empty string after /todos, got %q", got)
	}
}

// newTodoKeyRoutingModel mirrors TestTodoScrollRouting's construction: a root
// orchestrator with a list_todos provider serving todoCount "Task" entries.
// Callers mirror the startup sequence: ShowTodoPane is set BEFORE SetSize so
// updateLayout reserves the side-pane width at >= 85 columns, then the
// viewport height is pinned so scroll math is deterministic.
func newTodoKeyRoutingModel(t *testing.T, todoCount int) Model {
	t.Helper()
	sess := session.New(nil, "", nil, "", false)
	todos := make([]tool.Todo, todoCount)
	for i := range todos {
		todos[i].Text = "Task"
	}
	sess.Registry.Register(tool.ListTodosTool{Todos: &todos})
	root := orchestrator.NewBaseOrchestrator(common.MainAgentID, sess, nil, 0)
	m := NewModel(root, nil, nil)
	m.ShowTodoPane = true
	m.SetSize(100, 30)
	m.Viewport.SetHeight(10)
	m.Input.SetValue("")
	return m
}

// TestTodoPaneUnfocusedNavKeysLandInInput pins the input-safety rule: an
// unfocused todo pane consumes NOTHING. j/k/g are only nav keys while the
// pane is focused; unfocused they are ordinary typing and must accumulate in
// the chat input without touching the todo scroll offset.
func TestTodoPaneUnfocusedNavKeysLandInInput(t *testing.T) {
	m := newTodoKeyRoutingModel(t, 30)
	m.TodoPaneFocused = false
	m.TodoScrollOffset = 10

	for _, key := range []string{"j", "k", "g"} {
		updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: rune(key[0]), Text: key}))
		m = updated.(Model)
	}

	if m.TodoPaneFocused {
		t.Fatal("unfocused pane must stay unfocused while typing")
	}
	if got := m.Input.Value(); got != "jkg" {
		t.Fatalf("input value = %q, want %q (keys must reach the chat input)", got, "jkg")
	}
	if m.TodoScrollOffset != 10 {
		t.Fatalf("todo offset = %d, want unchanged 10", m.TodoScrollOffset)
	}
}

// TestTodoPaneUnfocusedKPassesThroughToInput documents the original bug as a
// permanent regression pin: the focused handler used to be the only thing
// between "k" and the input, and once focus styling existed the trap became
// visible. With the pane shown but not focused, "k" must reach the chat
// input and never change TodoScrollOffset.
func TestTodoPaneUnfocusedKPassesThroughToInput(t *testing.T) {
	m := newTodoKeyRoutingModel(t, 30)
	m.TodoPaneFocused = false
	m.TodoScrollOffset = 10

	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: 'k', Text: "k"}))
	m = updated.(Model)

	if m.TodoScrollOffset != 10 {
		t.Fatalf("todo offset = %d, want unchanged 10 (unfocused pane consumes nothing)", m.TodoScrollOffset)
	}
	if got := m.Input.Value(); got != "k" {
		t.Fatalf("input value = %q, want %q", got, "k")
	}
}

// TestTodoPaneFocusedPrintableKeyReleasesFocusAndTypes covers the escape
// hatch for the j/k/g typing trap: while focused, the first printable
// non-nav key releases focus AND falls through so the same keypress lands in
// the chat input (no key is ever silently swallowed).
func TestTodoPaneFocusedPrintableKeyReleasesFocusAndTypes(t *testing.T) {
	m := newTodoKeyRoutingModel(t, 30)
	m.TodoPaneFocused = true
	m.TodoScrollOffset = 10

	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	m = updated.(Model)

	if m.TodoPaneFocused {
		t.Fatal("printable key must release todo pane focus")
	}
	if got := m.Input.Value(); got != "x" {
		t.Fatalf("input value = %q, want %q (released key must reach the chat input)", got, "x")
	}
	if m.TodoScrollOffset != 10 {
		t.Fatalf("todo offset = %d, want unchanged 10 (x is not a nav key)", m.TodoScrollOffset)
	}
}

// TestTodoPaneFocusedNavKeyScrollsAndStaysFocused pins the other half of the
// contract: while focused, j remains a nav key — it scrolls the pane, keeps
// focus, and types nothing.
func TestTodoPaneFocusedNavKeyScrollsAndStaysFocused(t *testing.T) {
	m := newTodoKeyRoutingModel(t, 30)
	m.TodoPaneFocused = true
	m.TodoScrollOffset = 10

	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	m = updated.(Model)

	if !m.TodoPaneFocused {
		t.Fatal("nav key must keep todo pane focus")
	}
	if m.TodoScrollOffset != 11 {
		t.Fatalf("todo offset = %d, want 11", m.TodoScrollOffset)
	}
	if got := m.Input.Value(); got != "" {
		t.Fatalf("input value = %q, want empty (nav key must not type)", got)
	}
}

// TestTodoPaneClickOutsideReleasesFocus: a left click in the transcript
// region (left of the 44-column pane on a 100-column terminal) unfocuses the
// pane — the click itself keeps its normal downstream work.
func TestTodoPaneClickOutsideReleasesFocus(t *testing.T) {
	m := newTodoKeyRoutingModel(t, 30)
	m.TodoPaneFocused = true

	updated, _ := m.Update(tea.MouseClickMsg(tea.Mouse{X: 1, Y: 1, Button: tea.MouseLeft}))
	m = updated.(Model)

	if m.TodoPaneFocused {
		t.Fatal("left click outside the todo pane must release focus")
	}
}

// TestTodoPaneClickInsideGainsFocus pins the existing focus-on-click
// behavior: a left click inside the pane region focuses it.
func TestTodoPaneClickInsideGainsFocus(t *testing.T) {
	m := newTodoKeyRoutingModel(t, 30)
	m.TodoPaneFocused = false

	updated, _ := m.Update(tea.MouseClickMsg(tea.Mouse{X: 99, Y: 1, Button: tea.MouseLeft}))
	m = updated.(Model)

	if !m.TodoPaneFocused {
		t.Fatal("left click inside the todo pane must focus it")
	}
}

// TestKeyReleaseNeverScrollsOrUnfocusesTodoPane pins the release-event fix:
// tea.KeyReleaseMsg implements tea.KeyMsg, so on terminals that report
// release events (kitty keyboard protocol) a release of "j" used to match the
// pane's nav switch a second time — double-scrolling — and a release of
// other keys could still reach downstream handlers. Releases must be inert.
func TestKeyReleaseNeverScrollsOrUnfocusesTodoPane(t *testing.T) {
	m := newTodoKeyRoutingModel(t, 30)
	m.TodoPaneFocused = true
	m.TodoScrollOffset = 10

	updated, _ := m.Update(tea.KeyReleaseMsg(tea.Key{Code: rune('j'), Text: "j"}))
	next := updated.(Model)

	if !next.TodoPaneFocused {
		t.Fatal("a key release must never unfocus the pane")
	}
	if next.TodoScrollOffset != 10 {
		t.Fatalf("todo offset = %d after a key release, want unchanged 10 (no double scroll)", next.TodoScrollOffset)
	}
	if got := next.Input.Value(); got != "" {
		t.Fatalf("input = %q after a key release, want nothing typed", got)
	}
}

// TestTodoPaneFocusReleasesOnMultiRunePrintableKey covers dead-key /
// combining-sequence input: some keyboards deliver "e"+U+0301 as ONE event
// whose Key.Text holds multiple runes. That is typing — the pane must release
// focus so the text lands in the chat input, exactly like a single-rune key.
func TestTodoPaneFocusReleasesOnMultiRunePrintableKey(t *testing.T) {
	m := newTodoKeyRoutingModel(t, 30)
	m.TodoPaneFocused = true
	m.TodoScrollOffset = 10

	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: 'e', Text: "é"}))
	next := updated.(Model)

	if next.TodoPaneFocused {
		t.Fatal("a multi-rune printable key (composed é) must release the pane focus")
	}
	if next.TodoScrollOffset != 10 {
		t.Fatalf("todo offset = %d, want unchanged 10", next.TodoScrollOffset)
	}
}
