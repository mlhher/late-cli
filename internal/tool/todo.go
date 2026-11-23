package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"late/internal/common"
)

// Todo represents a single todo item with its completion status.
type Todo struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// CreateTodosTool creates and stores a list of todos for the session.
type CreateTodosTool struct {
	Todos *[]Todo
	Mu    *sync.Mutex
}

func resetTodos(todos *[]Todo, mu *sync.Mutex) {
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	if todos != nil {
		*todos = nil
	}
}

func (t CreateTodosTool) ResetConversationState() {
	resetTodos(t.Todos, t.Mu)
}

func (t CreateTodosTool) Name() string { return "create_todos" }
func (t CreateTodosTool) Description() string {
	return `Create a list of todos to track your progress.

Instructions:
1. Break down the work into clear, ordered milestones or steps.
2. Call this tool with ALL steps at once, in order.
3. Once created, use 'list_todos' to check your progress.
4. Use 'finish_todo' as you complete each step.

Important:
- Calling this tool again REPLACES the entire list and clears all completion
  status. To add or revise steps, include the full list every time.
- Call this tool with an EMPTY todos array to clear the list entirely (e.g.
  when the work is finished).

Tips:
- Make steps atomic and verifiable.
- More granularity is better than less.
- The list stays in memory for the entire session.`
}
func (t CreateTodosTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"todos": {
				"type": "array",
				"items": { "type": "string" },
				"description": "List of todo items in order (first item = first to do). Pass an empty array to clear the list."
			}
		},
		"required": ["todos"]
	}`)
}
func (t CreateTodosTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	// Only the main agent may manage todos; subagents must never modify them.
	if id := common.GetOrchestratorID(ctx); id != "" && id != common.MainAgentID {
		return "Error: todo tools are restricted to the main agent; subagents cannot modify the todo list.", nil
	}
	var params struct {
		Todos []string `json:"todos"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("invalid parameters: %w", err)
	}

	// An empty array clears the list: without this the agent has no way to
	// empty the todo pane once the work is done — finish_todo only marks
	// items done, so every clear attempt dead-ended in an error loop.
	if len(params.Todos) == 0 {
		if t.Mu != nil {
			t.Mu.Lock()
			if t.Todos != nil {
				*t.Todos = nil
			}
			t.Mu.Unlock()
		} else if t.Todos != nil {
			*t.Todos = nil
		}
		return "Cleared all todos. The todo list is now empty.", nil
	}

	newTodos := make([]Todo, len(params.Todos))
	for i, text := range params.Todos {
		newTodos[i] = Todo{Text: text, Done: false}
	}

	if t.Mu != nil {
		t.Mu.Lock()
		if t.Todos != nil {
			*t.Todos = newTodos
		}
		t.Mu.Unlock()
	} else if t.Todos != nil {
		*t.Todos = newTodos
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Created %d todo(s):\n\n", len(params.Todos)))
	for i, todoText := range params.Todos {
		sb.WriteString(fmt.Sprintf("  %d. %s\n", i+1, todoText))
	}
	sb.WriteString("\nUse 'list_todos' to see your plan, or 'finish_todo' to mark items complete.\n")
	return sb.String(), nil
}
func (t CreateTodosTool) RequiresConfirmation(args json.RawMessage) bool { return false }
func (t CreateTodosTool) CallString(args json.RawMessage) string {
	return "Creating todos..."
}

// ListTodosTool displays the current list of todos.
type ListTodosTool struct {
	Todos *[]Todo
	Mu    *sync.Mutex
}

func (t ListTodosTool) ResetConversationState() {
	resetTodos(t.Todos, t.Mu)
}

func (t ListTodosTool) Name() string { return "list_todos" }
func (t ListTodosTool) Description() string {
	return `List all todos and their completion status.

Shows the full plan with numbered items and checkboxes:
- [ ] = not done
- [✓] = completed

Use this tool frequently to stay organized and track progress.`
}
func (t ListTodosTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {},
		"required": []
	}`)
}
func (t ListTodosTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	// Only the main agent may manage todos; subagents must never modify them.
	if id := common.GetOrchestratorID(ctx); id != "" && id != common.MainAgentID {
		return "Error: todo tools are restricted to the main agent; subagents cannot modify the todo list.", nil
	}
	var snapshot []Todo
	if t.Mu != nil {
		t.Mu.Lock()
		if t.Todos != nil {
			snapshot = make([]Todo, len(*t.Todos))
			copy(snapshot, *t.Todos)
		}
		t.Mu.Unlock()
	} else if t.Todos != nil {
		snapshot = *t.Todos
	}

	if len(snapshot) == 0 {
		return "No todos have been created yet. Use 'create_todos' to set up your plan.", nil
	}
	var sb strings.Builder
	sb.WriteString("# Todo List\n\n")
	for i, todo := range snapshot {
		status := " "
		if todo.Done {
			status = "✓"
		}
		sb.WriteString(fmt.Sprintf("%d. [%s] %s\n", i+1, status, todo.Text))
	}
	return sb.String(), nil
}
func (t ListTodosTool) RequiresConfirmation(args json.RawMessage) bool { return false }
func (t ListTodosTool) CallString(args json.RawMessage) string {
	return "Listing todos..."
}

func (t ListTodosTool) GetTodos() []common.TodoItem {
	var snapshot []Todo
	if t.Mu != nil {
		t.Mu.Lock()
		if t.Todos != nil {
			snapshot = make([]Todo, len(*t.Todos))
			copy(snapshot, *t.Todos)
		}
		t.Mu.Unlock()
	} else if t.Todos != nil {
		snapshot = *t.Todos
	}

	if len(snapshot) == 0 {
		return nil
	}
	result := make([]common.TodoItem, len(snapshot))
	for i, td := range snapshot {
		result[i] = common.TodoItem{
			Text: td.Text,
			Done: td.Done,
		}
	}
	return result
}

// FinishTodoTool marks a todo as complete by exact string match.
type FinishTodoTool struct {
	Todos *[]Todo
	Mu    *sync.Mutex
}

func (t FinishTodoTool) ResetConversationState() {
	resetTodos(t.Todos, t.Mu)
}

func (t FinishTodoTool) Name() string { return "finish_todo" }
func (t FinishTodoTool) Description() string {
	return `Mark a todo item as complete, by INDEX (preferred) or by exact text.

Instructions:
1. Use 'list_todos' to see your current todo list with numbered items.
2. Prefer 'todo_index' — the 1-based number shown by 'list_todos' (e.g. 3
   for the third item). This is the most reliable way to finish a todo.
3. Alternatively pass 'todo_text' with the item's text copied EXACTLY
   (character-for-character: capitalization, punctuation, whitespace).
   Text matching fails easily after JSON escaping or paraphrasing — if it
   errors once, switch to 'todo_index' instead of retrying the text.

Provide exactly one of 'todo_index' or 'todo_text'.`
}
func (t FinishTodoTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"todo_index": {
				"type": "integer",
				"description": "1-based index of the todo to mark complete, as numbered by 'list_todos'. Preferred over todo_text."
			},
			"todo_text": {
				"type": "string",
				"description": "The exact text of the todo to mark as complete. Must match character-for-character. Use todo_index instead when possible."
			}
		},
		"required": []
	}`)
}
func (t FinishTodoTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	// Only the main agent may manage todos; subagents must never modify them.
	if id := common.GetOrchestratorID(ctx); id != "" && id != common.MainAgentID {
		return "Error: todo tools are restricted to the main agent; subagents cannot modify the todo list.", nil
	}
	var params struct {
		TodoIndex int    `json:"todo_index"`
		TodoText  string `json:"todo_text"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("invalid parameters: %w", err)
	}

	if t.Mu != nil {
		t.Mu.Lock()
		defer t.Mu.Unlock()
	}

	if t.Todos == nil || len(*t.Todos) == 0 {
		return "Error: No todos have been created yet. Use 'create_todos' to set up your plan.", nil
	}

	// Index path: the 1-based number 'list_todos' displays. It cannot be
	// corrupted by JSON escaping or paraphrasing, so it is the preferred
	// route. An explicit index also disambiguates duplicate texts (text
	// matching always completes the first).
	if params.TodoIndex > 0 {
		idx := params.TodoIndex - 1
		if idx >= len(*t.Todos) {
			return fmt.Sprintf("Error: todo_index %d is out of range: the list has %d item(s). Use 'list_todos' to see the numbered list.", params.TodoIndex, len(*t.Todos)), nil
		}
		if (*t.Todos)[idx].Done {
			return fmt.Sprintf("Todo %d ('%s') is already completed.", params.TodoIndex, (*t.Todos)[idx].Text), nil
		}
		(*t.Todos)[idx].Done = true
		return fmt.Sprintf("Completed: %s", (*t.Todos)[idx].Text), nil
	}

	todoText := strings.TrimSpace(params.TodoText)
	if todoText == "" {
		return "Error: provide either 'todo_index' (1-based number from 'list_todos', preferred) or 'todo_text' (exact text).", nil
	}

	todos := *t.Todos
	foundCompleted := false
	for i, todo := range todos {
		if todo.Text == todoText {
			if todo.Done {
				foundCompleted = true
				continue
			}
			todos[i].Done = true
			return fmt.Sprintf("Completed: %s", todo.Text), nil
		}
	}
	if foundCompleted {
		return fmt.Sprintf("Todo '%s' is already completed.", todoText), nil
	}
	return fmt.Sprintf("Error: No todo found matching '%s'. The text must match EXACTLY (including capitalization, punctuation, and whitespace). Prefer the reliable 'todo_index' parameter: run 'list_todos' to see the numbered list, then pass the item's number.", todoText), nil
}
func (t FinishTodoTool) RequiresConfirmation(args json.RawMessage) bool { return false }
func (t FinishTodoTool) CallString(args json.RawMessage) string {
	todoText := getToolParam(args, "todo_text")
	return fmt.Sprintf("Finishing todo: %s", truncate(todoText, 50))
}
