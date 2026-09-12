package tool

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"late/internal/common"
)

func TestCreateTodos(t *testing.T) {
	var todos []Todo
	var mu sync.Mutex
	tool := CreateTodosTool{Todos: &todos, Mu: &mu}

	// Create a list of todos
	args := json.RawMessage(`{"todos": ["Step 1", "Step 2", "Step 3"]}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Created 3 todo(s)") {
		t.Fatalf("expected success message with count, got: %s", result)
	}
	if len(todos) != 3 || todos[0].Text != "Step 1" || todos[2].Text != "Step 3" {
		t.Fatalf("todos not stored correctly: %v", todos)
	}
	t.Logf("result: %s", result)
}

func TestCreateTodosEmpty(t *testing.T) {
	var todos []Todo
	tool := CreateTodosTool{Todos: &todos}

	args := json.RawMessage(`{"todos": []}`)
	result, _ := tool.Execute(context.Background(), args)
	if !strings.Contains(result, "cannot be empty") {
		t.Fatalf("expected empty error, got: %s", result)
	}
}

func TestListTodos(t *testing.T) {
	todos := []Todo{
		{Text: "Setup", Done: false},
		{Text: "Code", Done: false},
		{Text: "Test", Done: false},
	}
	tool := ListTodosTool{Todos: &todos}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "# Todo List") {
		t.Fatalf("expected header, got: %s", result)
	}
	if !strings.Contains(result, "1. [ ] Setup") {
		t.Fatalf("expected checkbox format, got: %s", result)
	}
	if !strings.Contains(result, "2. [ ] Code") {
		t.Fatalf("expected checkbox format, got: %s", result)
	}
	if !strings.Contains(result, "3. [ ] Test") {
		t.Fatalf("expected checkbox format, got: %s", result)
	}
	t.Logf("result: %s", result)
}

func TestListTodosEmpty(t *testing.T) {
	var todos []Todo
	tool := ListTodosTool{Todos: &todos}

	result, _ := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if !strings.Contains(result, "No todos") {
		t.Fatalf("expected empty message, got: %s", result)
	}
}

func TestFinishTodoSuccess(t *testing.T) {
	todos := []Todo{
		{Text: "Step 1", Done: false},
		{Text: "Step 2", Done: false},
		{Text: "Step 3", Done: false},
	}
	tool := FinishTodoTool{Todos: &todos}

	// Finish the second todo
	args := json.RawMessage(`{"todo_text": "Step 2"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Completed: Step 2") {
		t.Fatalf("expected completion message, got: %s", result)
	}
	if !todos[1].Done {
		t.Fatalf("todo not marked complete: %v", todos[1])
	}
	t.Logf("result: %s", result)
}

func TestFinishTodoAlreadyCompleted(t *testing.T) {
	todos := []Todo{
		{Text: "Step 1", Done: true},
	}
	tool := FinishTodoTool{Todos: &todos}

	args := json.RawMessage(`{"todo_text": "Step 1"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "already completed") {
		t.Fatalf("expected already completed message, got: %s", result)
	}
}

func TestFinishTodoNotFound(t *testing.T) {
	todos := []Todo{
		{Text: "Step 1", Done: false},
		{Text: "Step 2", Done: false},
	}
	tool := FinishTodoTool{Todos: &todos}

	// Try with wrong text
	args := json.RawMessage(`{"todo_text": "Step 4"}`)
	result, _ := tool.Execute(context.Background(), args)
	if !strings.Contains(result, "No todo found") {
		t.Fatalf("expected not found error, got: %s", result)
	}
	if !strings.Contains(result, "list_todos") {
		t.Fatalf("expected error to mention list_todos, got: %s", result)
	}
	// Todos should be unchanged
	if len(todos) != 2 {
		t.Fatalf("todos should be unchanged, got: %v", todos)
	}
}

func TestFinishTodoExactMatch(t *testing.T) {
	todos := []Todo{{Text: "Do The Thing", Done: false}}
	tool := FinishTodoTool{Todos: &todos}

	// Case-sensitive: "do the thing" should NOT match "Do The Thing"
	args := json.RawMessage(`{"todo_text": "do the thing"}`)
	result, _ := tool.Execute(context.Background(), args)
	if !strings.Contains(result, "No todo found") {
		t.Fatalf("expected case-sensitive mismatch, got: %s", result)
	}

	// Exact match should work
	args = json.RawMessage(`{"todo_text": "Do The Thing"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Completed") {
		t.Fatalf("expected success, got: %s", result)
	}
}

func TestCreateFinishListFlow(t *testing.T) {
	var todos []Todo
	var mu sync.Mutex

	createTool := CreateTodosTool{Todos: &todos, Mu: &mu}
	listTool := ListTodosTool{Todos: &todos, Mu: &mu}
	finishTool := FinishTodoTool{Todos: &todos, Mu: &mu}

	// 1. Create todos
	_, err := createTool.Execute(context.Background(), json.RawMessage(`{"todos": ["Write code", "Write test"]}`))
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// 2. Finish first todo
	_, err = finishTool.Execute(context.Background(), json.RawMessage(`{"todo_text": "Write code"}`))
	if err != nil {
		t.Fatalf("finish failed: %v", err)
	}

	// 3. List todos and check checkbox output
	listResult, err := listTool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}

	if !strings.Contains(listResult, "1. [✓] Write code") {
		t.Errorf("expected completed item formatted as '1. [✓] Write code', got:\n%s", listResult)
	}
	if !strings.Contains(listResult, "2. [ ] Write test") {
		t.Errorf("expected uncompleted item formatted as '2. [ ] Write test', got:\n%s", listResult)
	}
}

func TestCreateTodosNilPointerSafe(t *testing.T) {
	var mu sync.Mutex
	tool := CreateTodosTool{Todos: nil, Mu: &mu}

	args := json.RawMessage(`{"todos": ["Step 1", "Step 2"]}`)
	// Should not panic when Todos is nil
	_, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFinishTodoDuplicates(t *testing.T) {
	todos := []Todo{
		{Text: "Run tests", Done: false},
		{Text: "Update docs", Done: false},
		{Text: "Run tests", Done: false},
	}
	tool := FinishTodoTool{Todos: &todos}

	// First finish should complete the first "Run tests"
	res1, err := tool.Execute(context.Background(), json.RawMessage(`{"todo_text": "Run tests"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res1, "Completed: Run tests") {
		t.Fatalf("expected completion of first item, got: %s", res1)
	}
	if !todos[0].Done || todos[2].Done {
		t.Fatalf("expected only first 'Run tests' to be done, got: %v", todos)
	}

	// Second finish should complete the second "Run tests"
	res2, err := tool.Execute(context.Background(), json.RawMessage(`{"todo_text": "Run tests"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res2, "Completed: Run tests") {
		t.Fatalf("expected completion of second item, got: %s", res2)
	}
	if !todos[0].Done || !todos[2].Done {
		t.Fatalf("expected both 'Run tests' to be done, got: %v", todos)
	}

	// Third finish should report already completed
	res3, _ := tool.Execute(context.Background(), json.RawMessage(`{"todo_text": "Run tests"}`))
	if !strings.Contains(res3, "already completed") {
		t.Fatalf("expected already completed message, got: %s", res3)
	}
}

func TestFinishTodoTrimSpace(t *testing.T) {
	todos := []Todo{
		{Text: "Do Something", Done: false},
	}
	tool := FinishTodoTool{Todos: &todos}

	// Leading/trailing whitespace should be trimmed and match
	args := json.RawMessage(`{"todo_text": "  Do Something  \n"}`)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Completed: Do Something") {
		t.Fatalf("expected success with trimmed text, got: %s", result)
	}
	if !todos[0].Done {
		t.Fatalf("expected todo to be marked done")
	}
}

func TestListTodosGetTodos(t *testing.T) {
	var mu sync.Mutex
	todos := []Todo{
		{Text: "Task A", Done: true},
		{Text: "Task B", Done: false},
	}
	tool := ListTodosTool{Todos: &todos, Mu: &mu}

	items := tool.GetTodos()
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Text != "Task A" || !items[0].Done {
		t.Fatalf("unexpected item[0]: %+v", items[0])
	}
	if items[1].Text != "Task B" || items[1].Done {
		t.Fatalf("unexpected item[1]: %+v", items[1])
	}
}

func TestTodoToolsRejectSubagentOrchestrator(t *testing.T) {
	ctx := context.WithValue(context.Background(), common.OrchestratorIDKey, "coder-subagent-0")

	// create must be rejected and not modify the todo list
	var createTodos []Todo
	var createMu sync.Mutex
	createResult, createErr := CreateTodosTool{Todos: &createTodos, Mu: &createMu}.Execute(ctx, json.RawMessage(`{"todos": ["A", "B"]}`))
	if createErr != nil {
		t.Fatalf("expected nil error from subagent guard, got: %v", createErr)
	}
	if !strings.Contains(createResult, "restricted to the main agent") {
		t.Fatalf("expected subagent restriction message, got: %s", createResult)
	}
	if len(createTodos) != 0 {
		t.Fatalf("expected todos to be unchanged, got: %v", createTodos)
	}

	// list must be rejected
	var listTodos []Todo
	var listMu sync.Mutex
	listResult, listErr := ListTodosTool{Todos: &listTodos, Mu: &listMu}.Execute(ctx, json.RawMessage(`{}`))
	if listErr != nil {
		t.Fatalf("expected nil error from subagent guard, got: %v", listErr)
	}
	if !strings.Contains(listResult, "restricted to the main agent") {
		t.Fatalf("expected subagent restriction message, got: %s", listResult)
	}

	// finish must be rejected and not modify the todo list
	var finishTodos []Todo
	var finishMu sync.Mutex
	finishResult, finishErr := FinishTodoTool{Todos: &finishTodos, Mu: &finishMu}.Execute(ctx, json.RawMessage(`{"todo_text": "A"}`))
	if finishErr != nil {
		t.Fatalf("expected nil error from subagent guard, got: %v", finishErr)
	}
	if !strings.Contains(finishResult, "restricted to the main agent") {
		t.Fatalf("expected subagent restriction message, got: %s", finishResult)
	}
	if len(finishTodos) != 0 {
		t.Fatalf("expected todos to be unchanged, got: %v", finishTodos)
	}
}

func TestTodoToolsAllowMainOrchestrator(t *testing.T) {
	ctx := context.WithValue(context.Background(), common.OrchestratorIDKey, common.MainAgentID)

	var todos []Todo
	var mu sync.Mutex

	// create should work for the main agent
	createResult, err := CreateTodosTool{Todos: &todos, Mu: &mu}.Execute(ctx, json.RawMessage(`{"todos": ["Step 1", "Step 2"]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(createResult, "Created 2 todo(s)") {
		t.Fatalf("expected success message with count, got: %s", createResult)
	}
	if len(todos) != 2 {
		t.Fatalf("expected 2 todos, got: %v", todos)
	}

	// finish should work for the main agent
	finishResult, err := FinishTodoTool{Todos: &todos, Mu: &mu}.Execute(ctx, json.RawMessage(`{"todo_text": "Step 1"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(finishResult, "Completed: Step 1") {
		t.Fatalf("expected completion message, got: %s", finishResult)
	}
	if !todos[0].Done {
		t.Fatalf("expected first todo to be marked done, got: %v", todos[0])
	}

	// list should work for the main agent
	listResult, err := ListTodosTool{Todos: &todos, Mu: &mu}.Execute(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(listResult, "# Todo List") {
		t.Fatalf("expected header, got: %s", listResult)
	}
}
