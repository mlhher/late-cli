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
1. Break down the work into clear, sequential milestones or steps.
2. Call this tool with ALL steps at once, in order.
3. Once created, use 'list_todos' to check your progress.
4. Use 'finish_todo' as you complete each step.

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
				"description": "List of todo items in order (first item = first to do)"
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
	if len(params.Todos) == 0 {
		return "Error: todos array cannot be empty. Please provide at least one todo item.", nil
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
	return `Mark a todo item as complete by typing its EXACT text.

Instructions:
1. Use 'list_todos' to see your current todo list.
2. Copy the EXACT text of the todo you want to mark complete.
3. Pass it to this tool. The match is EXACT — whitespace matters.
4. If your string does not match, you will get an error telling you to use 'list_todos'.

Important: The string must match character-for-character with the item in your todo list.
Do not modify capitalization, punctuation, or whitespace.`
}
func (t FinishTodoTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"todo_text": {
				"type": "string",
				"description": "The exact text of the todo to mark as complete. Must match exactly."
			}
		},
		"required": ["todo_text"]
	}`)
}
func (t FinishTodoTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	// Only the main agent may manage todos; subagents must never modify them.
	if id := common.GetOrchestratorID(ctx); id != "" && id != common.MainAgentID {
		return "Error: todo tools are restricted to the main agent; subagents cannot modify the todo list.", nil
	}
	var params struct {
		TodoText string `json:"todo_text"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("invalid parameters: %w", err)
	}
	todoText := strings.TrimSpace(params.TodoText)
	if todoText == "" {
		return "Error: todo_text cannot be empty. Use 'list_todos' to see your current todos.", nil
	}

	if t.Mu != nil {
		t.Mu.Lock()
		defer t.Mu.Unlock()
	}

	if t.Todos == nil || len(*t.Todos) == 0 {
		return "Error: No todos have been created yet. Use 'create_todos' to set up your plan.", nil
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
	return fmt.Sprintf("Error: No todo found matching '%s'. The text must match EXACTLY (including capitalization, punctuation, and whitespace). Use the 'list_todos' tool to see your current todo list and get the correct text.", todoText), nil
}
func (t FinishTodoTool) RequiresConfirmation(args json.RawMessage) bool { return false }
func (t FinishTodoTool) CallString(args json.RawMessage) string {
	todoText := getToolParam(args, "todo_text")
	return fmt.Sprintf("Finishing todo: %s", truncate(todoText, 50))
}
