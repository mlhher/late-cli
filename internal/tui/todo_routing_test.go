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
