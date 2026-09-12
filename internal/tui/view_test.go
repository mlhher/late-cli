package tui

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"late/internal/common"
	"late/internal/orchestrator"
	"late/internal/session"
	"late/internal/tool"
)

// TestTodoItemsAlwaysShowsMainAgentTodos proves the Todo pane always shows the
// MAIN agent's todos, even when a subagent is focused. The pane previously read
// from the focused agent's registry, which has no todo tools (they are
// orchestrator-only, not inherited by subagents), making the list appear to
// reset whenever focus moved to a subagent.
func TestTodoItemsAlwaysShowsMainAgentTodos(t *testing.T) {
	// 1. Build the root (main) agent with todo tools registered on its session
	//    registry as values, matching how executor.RegisterTools wires them up.
	rootSess := session.New(nil, "", nil, "", false)
	var todos []tool.Todo
	var mu sync.Mutex
	rootSess.Registry.Register(tool.CreateTodosTool{Todos: &todos, Mu: &mu})
	rootSess.Registry.Register(tool.ListTodosTool{Todos: &todos, Mu: &mu})
	rootSess.Registry.Register(tool.FinishTodoTool{Todos: &todos, Mu: &mu})
	root := orchestrator.NewBaseOrchestrator(common.MainAgentID, rootSess, nil, 0)

	// 2. Seed todos through the root's registry using the main-agent context.
	ctx := context.WithValue(context.Background(), common.OrchestratorIDKey, common.MainAgentID)
	if _, err := rootSess.Registry.Get("create_todos").Execute(ctx, json.RawMessage(`{"todos": ["Implement fix", "Write tests"]}`)); err != nil {
		t.Fatalf("create_todos failed: %v", err)
	}

	// 3. Build a focused subagent with an EMPTY registry (no todo tools).
	subSess := session.New(nil, "", nil, "", false)
	sub := orchestrator.NewBaseOrchestrator("coder-subagent-0", subSess, nil, 0)

	// 4. Construct a minimal Model with the subagent focused. Only Root and
	//    Focused are set; the rest are zero values, which is fine because
	//    todoItems() only touches Root.
	m := Model{Root: root, Focused: sub}

	// 5. The todo pane must show the MAIN agent's todos, not the focused
	//    subagent's (empty) list.
	items := m.todoItems()
	if len(items) != 2 {
		t.Fatalf("expected 2 todo items from the main agent, got %d: %v", len(items), items)
	}
	if items[0].Text != "Implement fix" {
		t.Errorf("items[0].Text = %q, want %q", items[0].Text, "Implement fix")
	}
	if items[0].Done {
		t.Errorf("items[0].Done = true, want false")
	}
	if items[1].Text != "Write tests" {
		t.Errorf("items[1].Text = %q, want %q", items[1].Text, "Write tests")
	}
	if items[1].Done {
		t.Errorf("items[1].Done = true, want false")
	}

	// 6. Document the OLD behavior: the focused subagent's registry has no
	//    todo tools at all, which is why reading the pane from it emptied the
	//    list. This assertion pins down the root cause of the bug.
	if got := sub.Registry().Get("list_todos"); got != nil {
		t.Errorf("subagent registry should not expose list_todos, got %v", got)
	}
}

// TestTodoItemsNilRootReturnsNil verifies todoItems() degrades gracefully when
// no root agent is set, returning nil instead of panicking.
func TestTodoItemsNilRootReturnsNil(t *testing.T) {
	subSess := session.New(nil, "", nil, "", false)
	sub := orchestrator.NewBaseOrchestrator("coder-subagent-0", subSess, nil, 0)

	m := Model{Root: nil, Focused: sub}

	if items := m.todoItems(); items != nil {
		t.Fatalf("expected nil todo items, got %v", items)
	}
	if items := m.todoItems(); len(items) != 0 {
		t.Fatalf("expected 0 todo items, got %d", len(items))
	}
}
