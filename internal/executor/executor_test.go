package executor

import (
	"context"
	"fmt"
	"late/internal/client"
	"late/internal/common"
	"late/internal/session"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestStreamAccumulator_Append(t *testing.T) {
	acc := StreamAccumulator{}

	// Append content
	acc.Append(common.StreamResult{Content: "Hello "})
	acc.Append(common.StreamResult{Content: "world"})

	if acc.Content != "Hello world" {
		t.Errorf("expected 'Hello world', got '%s'", acc.Content)
	}
}

func TestStreamAccumulator_AppendReasoning(t *testing.T) {
	acc := StreamAccumulator{}

	acc.Append(common.StreamResult{ReasoningContent: "Step 1. "})
	acc.Append(common.StreamResult{ReasoningContent: "Step 2."})

	if acc.Reasoning != "Step 1. Step 2." {
		t.Errorf("expected 'Step 1. Step 2.', got '%s'", acc.Reasoning)
	}
}

func TestStreamAccumulator_AppendToolCalls(t *testing.T) {
	acc := StreamAccumulator{}

	// First delta creates a new tool call
	acc.Append(common.StreamResult{
		ToolCalls: []client.ToolCall{
			{Index: 0, ID: "call_1", Function: client.FunctionCall{Name: "read_file", Arguments: `{"path"`}},
		},
	})

	// Second delta appends to existing tool call arguments
	acc.Append(common.StreamResult{
		ToolCalls: []client.ToolCall{
			{Index: 0, Function: client.FunctionCall{Arguments: `: "test.go"}`}},
		},
	})

	if len(acc.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(acc.ToolCalls))
	}
	if acc.ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("expected tool name 'read_file', got '%s'", acc.ToolCalls[0].Function.Name)
	}
	expected := `{"path": "test.go"}`
	if acc.ToolCalls[0].Function.Arguments != expected {
		t.Errorf("expected args '%s', got '%s'", expected, acc.ToolCalls[0].Function.Arguments)
	}
	if acc.ToolCalls[0].ID != "call_1" {
		t.Errorf("expected ID 'call_1', got '%s'", acc.ToolCalls[0].ID)
	}
}

func TestStreamAccumulator_AppendMultipleToolCalls(t *testing.T) {
	acc := StreamAccumulator{}

	acc.Append(common.StreamResult{
		ToolCalls: []client.ToolCall{
			{Index: 0, ID: "call_1", Function: client.FunctionCall{Name: "read_file", Arguments: `{"path": "a.go"}`}},
		},
	})
	acc.Append(common.StreamResult{
		ToolCalls: []client.ToolCall{
			{Index: 1, ID: "call_2", Function: client.FunctionCall{Name: "write_file", Arguments: `{"path": "."}`}},
		},
	})

	if len(acc.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(acc.ToolCalls))
	}
	if acc.ToolCalls[1].Function.Name != "write_file" {
		t.Errorf("expected 'write_file', got '%s'", acc.ToolCalls[1].Function.Name)
	}
}

func TestStreamAccumulator_Reset(t *testing.T) {
	acc := StreamAccumulator{
		Content:   "test",
		Reasoning: "thought",
		ToolCalls: []client.ToolCall{{ID: "1"}},
	}

	acc.Reset()

	if acc.Content != "" || acc.Reasoning != "" || acc.ToolCalls != nil {
		t.Error("expected all fields to be zero after Reset")
	}
}

func TestStreamAccumulator_NameUpdate(t *testing.T) {
	acc := StreamAccumulator{}

	// First delta: tool call with empty name (streaming)
	acc.Append(common.StreamResult{
		ToolCalls: []client.ToolCall{
			{Index: 0, ID: "call_1", Function: client.FunctionCall{Name: "", Arguments: `{`}},
		},
	})

	// Second delta: name arrives
	acc.Append(common.StreamResult{
		ToolCalls: []client.ToolCall{
			{Index: 0, Function: client.FunctionCall{Name: "bash", Arguments: `"cmd": "ls"}`}},
		},
	})

	if acc.ToolCalls[0].Function.Name != "bash" {
		t.Errorf("expected name to be updated to 'bash', got '%s'", acc.ToolCalls[0].Function.Name)
	}
}

// TestExecuteToolCalls_NotFound verifies that missing tools produce an error message
func TestExecuteToolCalls_NotFound(t *testing.T) {
	c := client.NewClient(client.Config{BaseURL: "http://localhost:0"})
	histPath := filepath.Join(t.TempDir(), "history.json")
	sess := session.New(c, histPath, nil, "", false)

	toolCalls := []client.ToolCall{
		{ID: "tc_1", Function: client.FunctionCall{Name: "nonexistent", Arguments: "{}"}},
	}

	err := ExecuteToolCalls(context.Background(), sess, toolCalls, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have added a tool result message with error
	if len(sess.History) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(sess.History))
	}
	if sess.History[0].Role != "tool" {
		t.Errorf("expected role 'tool', got '%s'", sess.History[0].Role)
	}
}

// TestExecuteToolCalls_Denied verifies denied confirmation produces cancel message
func TestExecuteToolCalls_Denied(t *testing.T) {
	c := client.NewClient(client.Config{BaseURL: "http://localhost:0"})
	histPath := filepath.Join(t.TempDir(), "history.json")
	sess := session.New(c, histPath, nil, "", true)

	// Register bash tool which requires confirmation
	RegisterTools(sess.Registry, nil, false)

	toolCalls := []client.ToolCall{
		{ID: "tc_1", Function: client.FunctionCall{Name: "bash", Arguments: `{"command":"echo hi"}`}},
	}

	denyMiddleware := func(next common.ToolRunner) common.ToolRunner {
		return func(ctx context.Context, tc client.ToolCall) (string, error) {
			return "Tool execution cancelled by user", nil
		}
	}

	err := ExecuteToolCalls(context.Background(), sess, toolCalls, []common.ToolMiddleware{denyMiddleware})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sess.History) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(sess.History))
	}
	if sess.History[0].Content != "Tool execution cancelled by user" {
		t.Errorf("expected cancel message, got '%s'", sess.History[0].Content)
	}
}

// TestExecuteToolCalls_NoMiddlewareFailsClosed verifies shell commands cannot
// run when confirmation middleware is missing.
func TestExecuteToolCalls_NoMiddlewareFailsClosed(t *testing.T) {
	c := client.NewClient(client.Config{BaseURL: "http://localhost:0"})
	histPath := filepath.Join(t.TempDir(), "history.json")
	sess := session.New(c, histPath, nil, "", true)

	RegisterTools(sess.Registry, map[string]bool{"bash": true}, false)

	toolCalls := []client.ToolCall{
		{ID: "tc_1", Function: client.FunctionCall{Name: "bash", Arguments: `{"command":"echo hi"}`}},
	}

	err := ExecuteToolCalls(context.Background(), sess, toolCalls, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sess.History) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(sess.History))
	}

	if !strings.Contains(sess.History[0].Content, "requires explicit approval") {
		t.Fatalf("expected fail-closed approval message, got %q", sess.History[0].Content)
	}
}

// TestConsumeStream verifies ConsumeStream drains a channel correctly
func TestConsumeStream(t *testing.T) {
	outCh := make(chan common.StreamResult, 3)
	errCh := make(chan error, 1)

	outCh <- common.StreamResult{Content: "Hello "}
	outCh <- common.StreamResult{Content: "world"}
	outCh <- common.StreamResult{ReasoningContent: "thinking..."}
	close(outCh)
	close(errCh)

	var chunks int
	acc, err := ConsumeStream(context.Background(), outCh, errCh, func(r common.StreamResult) {
		chunks++
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if acc.Content != "Hello world" {
		t.Errorf("expected 'Hello world', got '%s'", acc.Content)
	}
	if acc.Reasoning != "thinking..." {
		t.Errorf("expected 'thinking...', got '%s'", acc.Reasoning)
	}
	if chunks != 3 {
		t.Errorf("expected 3 chunks, got %d", chunks)
	}
}

// TestConsumeStream_WithError verifies stream errors are returned
func TestConsumeStream_WithError(t *testing.T) {
	outCh := make(chan common.StreamResult, 1)
	errCh := make(chan error, 1)

	outCh <- common.StreamResult{Content: "partial"}
	close(outCh)
	errCh <- context.DeadlineExceeded
	close(errCh)

	acc, err := ConsumeStream(context.Background(), outCh, errCh, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if acc.Content != "partial" {
		t.Errorf("expected 'partial', got '%s'", acc.Content)
	}
}

// TestRegisterTools verifies that tools are registered
func TestRegisterTools(t *testing.T) {
	c := client.NewClient(client.Config{BaseURL: "http://localhost:0"})
	histPath := filepath.Join(t.TempDir(), "history.json")
	sess := session.New(c, histPath, nil, "", false)

	enabledTools := map[string]bool{
		"read_file":   true,
		"write_file":  true,
		"target_edit": true,
		"bash":        false,
	}
	RegisterTools(sess.Registry, enabledTools, false)

	expected := []string{"read_file", "write_file", "target_edit"}
	for _, name := range expected {
		if sess.Registry.Get(name) == nil {
			t.Errorf("expected tool '%s' to be registered", name)
		}
	}

	// Bash should NOT be registered when enableBash is false
	if sess.Registry.Get("bash") != nil {
		t.Error("bash should not be registered when enableBash is false")
	}
}

// TestRegisterTools_WithBash verifies bash tool is registered when enabled
func TestRegisterTools_WithBash(t *testing.T) {
	c := client.NewClient(client.Config{BaseURL: "http://localhost:0"})
	histPath := filepath.Join(t.TempDir(), "history.json")
	sess := session.New(c, histPath, nil, "", false)

	enabledTools := map[string]bool{
		"bash": true,
	}
	RegisterTools(sess.Registry, enabledTools, false)

	if sess.Registry.Get("bash") == nil {
		t.Error("bash should be registered when enableBash is true")
	}
}

func TestRegisterTools_WithReadFile(t *testing.T) {
	c := client.NewClient(client.Config{BaseURL: "http://localhost:0"})
	histPath := filepath.Join(t.TempDir(), "history.json")
	sess := session.New(c, histPath, nil, "", false)

	enabledTools := map[string]bool{
		"read_file": true,
	}
	RegisterTools(sess.Registry, enabledTools, false)

	// Verify ReadFileTool is still there (implied by default check), but maybe check its description/params if needed?
	// For now, just ensuring no error is thrown during registration is good enough.
	if sess.Registry.Get("read_file") == nil {
		t.Error("read_file should be registered")
	}
}

func TestRegisterTools_Planning(t *testing.T) {
	c := client.NewClient(client.Config{BaseURL: "http://localhost:0"})
	histPath := filepath.Join(t.TempDir(), "history.json")
	sess := session.New(c, histPath, nil, "", false)

	enabledTools := map[string]bool{
		"read_file":  true,
		"write_file": true,
		"bash":       true,
	}
	RegisterTools(sess.Registry, enabledTools, true)

	// In planning mode, write_file should NOT be registered
	if sess.Registry.Get("write_file") != nil {
		t.Error("write_file should not be registered in planning mode")
	}

	// But write_implementation_plan should be
	if sess.Registry.Get("write_implementation_plan") == nil {
		t.Error("write_implementation_plan should be registered in planning mode")
	}

	// read_file and bash should be there
	if sess.Registry.Get("read_file") == nil {
		t.Error("read_file should be registered in planning mode")
	}
	if sess.Registry.Get("bash") == nil {
		t.Error("bash should be registered in planning mode")
	}
}

// sseOnce returns an httptest.Server that serves a single SSE chunk with the
// given content followed by [DONE]. Suitable for driving one RunLoop turn.
func sseOnce(t *testing.T, content string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := fmt.Sprintf(`{"choices":[{"delta":{"content":%q,"role":"assistant"},"finish_reason":null}]}`, content)
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
}

// newRunLoopSession creates a session backed by sseServer with n pre-filled
// alternating user/assistant messages. history.json is written to t.TempDir().
func newRunLoopSession(t *testing.T, srv *httptest.Server, n int) *session.Session {
	t.Helper()
	msgs := make([]client.ChatMessage, n)
	for i := range msgs {
		if i%2 == 0 {
			msgs[i] = client.ChatMessage{Role: "user", Content: fmt.Sprintf("user %d", i)}
		} else {
			msgs[i] = client.ChatMessage{Role: "assistant", Content: fmt.Sprintf("assistant %d", i)}
		}
	}
	c := client.NewClient(client.Config{BaseURL: srv.URL})
	histPath := filepath.Join(t.TempDir(), "history.json")
	return session.New(c, histPath, msgs, "", false)
}

// TestRunLoop_ContextRecovery_Disabled verifies that when ContextRecoveryEnabled
// is false (the default), a history exceeding the 80-message ceiling is NOT
// pruned — RunLoop leaves history untouched.
func TestRunLoop_ContextRecovery_Disabled(t *testing.T) {
	srv := sseOnce(t, "response")
	defer srv.Close()

	const prefill = 82
	sess := newRunLoopSession(t, srv, prefill)
	sess.ContextRecoveryEnabled = false // explicit default

	_, err := RunLoop(context.Background(), sess, 1, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("RunLoop error: %v", err)
	}

	// History grows by 1 (the assistant reply) — it is never pruned.
	if len(sess.History) <= prefill {
		t.Errorf("expected history > %d (no prune), got %d", prefill, len(sess.History))
	}
	if len(sess.History) < prefill {
		t.Errorf("history was pruned despite ContextRecoveryEnabled=false: got %d messages", len(sess.History))
	}
}

// TestRunLoop_ContextRecovery_Enabled verifies that when ContextRecoveryEnabled
// is true, a history exceeding the 80-message ceiling IS pruned after the turn.
func TestRunLoop_ContextRecovery_Enabled(t *testing.T) {
	srv := sseOnce(t, "response")
	defer srv.Close()

	const prefill = 82
	sess := newRunLoopSession(t, srv, prefill)
	sess.ContextRecoveryEnabled = true

	_, err := RunLoop(context.Background(), sess, 1, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("RunLoop error: %v", err)
	}

	// PruneAndRestoreFromDisk keeps at most 10 tail messages.
	if len(sess.History) > 10 {
		t.Errorf("expected history <= 10 after recovery, got %d", len(sess.History))
	}
	if len(sess.History) == 0 {
		t.Error("history is empty after recovery; expected tail to be retained")
	}
}
