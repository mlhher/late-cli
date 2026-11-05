package tui

import (
	"testing"

	"charm.land/bubbles/v2/spinner"

	"late/internal/client"
	"late/internal/common"
	"late/internal/orchestrator"
	"late/internal/session"
	"late/internal/tool"
)

// newTodoAutoOpenModel builds a model whose orchestrator todo list the test
// can grow later through the returned pointer, armed the way main.go arms it
// when show-todo-pane is unset: pane closed, auto-open pending.
func newTodoAutoOpenModel(t *testing.T, width int) (Model, *[]tool.Todo) {
	t.Helper()
	todos := &[]tool.Todo{}
	sess := session.New(client.NewClient(client.Config{}), "", nil, "", false)
	sess.Registry.Register(tool.ListTodosTool{Todos: todos})
	root := orchestrator.NewBaseOrchestrator(common.MainAgentID, sess, nil, 0)
	m := NewModel(root, nil, nil)
	m.TodoPaneAuto = true
	m.SetSize(width, 30)
	return m, todos
}

func tickUpdate(t *testing.T, m Model) Model {
	t.Helper()
	updated, _ := m.Update(spinner.TickMsg{})
	return updated.(Model)
}

func TestTodoPaneStaysClosedUntilTodosExist(t *testing.T) {
	m, _ := newTodoAutoOpenModel(t, 120)
	m = tickUpdate(t, m)
	if m.ShowTodoPane {
		t.Fatal("pane must stay closed while there are no todos")
	}
	if !m.TodoPaneAuto {
		t.Fatal("pane should stay armed while there are no todos")
	}
	if got := m.Viewport.Width(); got != 120 {
		t.Fatalf("closed pane must not take width, viewport = %d", got)
	}
}

func TestTodoPaneOpensWhenTodosAreCreated(t *testing.T) {
	m, todos := newTodoAutoOpenModel(t, 120)
	*todos = append(*todos, tool.Todo{Text: "Plan"}, tool.Todo{Text: "Build"})
	m = tickUpdate(t, m)

	if !m.ShowTodoPane {
		t.Fatal("pane should open once todos exist")
	}
	if m.TodoPaneAuto {
		t.Fatal("auto-open should be used up after opening")
	}
	if m.TodoPaneFocused {
		t.Fatal("auto-open must not steal focus from the input")
	}
	if got := m.Viewport.Width(); got != 120-todoPaneWidth {
		t.Fatalf("viewport width = %d, want %d (pane width reserved)", got, 120-todoPaneWidth)
	}
}

func TestTodoPaneAutoOpenWaitsForAWideEnoughTerminal(t *testing.T) {
	m, todos := newTodoAutoOpenModel(t, 70)
	*todos = append(*todos, tool.Todo{Text: "Plan"})
	m = tickUpdate(t, m)
	if m.ShowTodoPane || !m.TodoPaneAuto {
		t.Fatalf("narrow terminal: pane open=%v armed=%v, want closed and still armed", m.ShowTodoPane, m.TodoPaneAuto)
	}

	m.SetSize(120, 30)
	m = tickUpdate(t, m)
	if !m.ShowTodoPane {
		t.Fatal("widening the terminal should let the armed pane open")
	}
}

func TestTodoPaneAutoOpenHappensOnlyOnce(t *testing.T) {
	m, todos := newTodoAutoOpenModel(t, 120)
	*todos = append(*todos, tool.Todo{Text: "Plan"})
	m = tickUpdate(t, m)

	// The user closes it: it must not come back by itself.
	m.ShowTodoPane = false
	m.updateLayout()
	m = tickUpdate(t, m)
	if m.ShowTodoPane {
		t.Fatal("a pane the user closed must not reopen by itself")
	}
}

func TestTodoPaneManualToggleDisarmsAutoOpen(t *testing.T) {
	m, todos := newTodoAutoOpenModel(t, 120)
	m.Input.SetValue("/todos")
	m = pressEnter(t, m) // user opens it
	m.Input.SetValue("/todos")
	m = pressEnter(t, m) // and closes it again
	if m.ShowTodoPane || m.TodoPaneAuto {
		t.Fatalf("after two toggles: open=%v armed=%v, want closed and disarmed", m.ShowTodoPane, m.TodoPaneAuto)
	}

	*todos = append(*todos, tool.Todo{Text: "Plan"})
	m = tickUpdate(t, m)
	if m.ShowTodoPane {
		t.Fatal("a user who took control must not have the pane opened for them")
	}
}

func TestTodoPaneNotArmedWithoutAutoFlag(t *testing.T) {
	m, todos := newTodoAutoOpenModel(t, 120)
	m.TodoPaneAuto = false // show-todo-pane set explicitly in config
	*todos = append(*todos, tool.Todo{Text: "Plan"})
	m = tickUpdate(t, m)
	if m.ShowTodoPane {
		t.Fatal("an explicit config choice must not be overridden")
	}
}
