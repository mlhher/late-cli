package executor

import (
	"context"
	"encoding/json"
	"late/internal/client"
	"late/internal/common"
	"late/internal/session"
	"path/filepath"
	"strings"
	"testing"
)

// panickyTool is a tool whose Execute always panics — the shape of a buggy
// builtin, plugin, or MCP bridge implementation.
type panickyTool struct{}

func (panickyTool) Name() string                              { return "panicky" }
func (panickyTool) Description() string                       { return "always panics" }
func (panickyTool) Parameters() json.RawMessage               { return json.RawMessage(`{"type":"object"}`) }
func (panickyTool) RequiresConfirmation(json.RawMessage) bool { return false }
func (panickyTool) CallString(json.RawMessage) string         { return "panicky()" }
func (panickyTool) Execute(context.Context, json.RawMessage) (string, error) {
	panic("boom: tool implementation bug")
}

// healthyTool records its execution so the test can prove the batch continued
// after the panicking call.
type healthyTool struct{ called *bool }

func (healthyTool) Name() string                              { return "healthy" }
func (healthyTool) Description() string                       { return "always works" }
func (healthyTool) Parameters() json.RawMessage               { return json.RawMessage(`{"type":"object"}`) }
func (healthyTool) RequiresConfirmation(json.RawMessage) bool { return false }
func (healthyTool) CallString(json.RawMessage) string         { return "healthy()" }
func (t healthyTool) Execute(context.Context, json.RawMessage) (string, error) {
	*t.called = true
	return "healthy result", nil
}

// TestExecuteToolCalls_ToolPanicIsContained pins the panic-containment
// contract of the tool path: a panicking tool implementation must be
// converted into an error RESULT for that call (visible to the model, run
// continues) instead of unwinding the run-loop goroutine and killing the
// whole process — root agent, every subagent, and the TUI with it.
func TestExecuteToolCalls_ToolPanicIsContained(t *testing.T) {
	isolateSessionDir(t)
	c := client.NewClient(client.Config{BaseURL: "http://localhost:0"})
	histPath := filepath.Join(t.TempDir(), "history.json")
	sess := session.New(c, histPath, nil, "", false)

	called := false
	sess.Registry.Register(panickyTool{})
	sess.Registry.Register(healthyTool{called: &called})

	toolCalls := []client.ToolCall{
		{ID: "tc_1", Function: client.FunctionCall{Name: "panicky", Arguments: "{}"}},
		{ID: "tc_2", Function: client.FunctionCall{Name: "healthy", Arguments: "{}"}},
	}

	err := ExecuteToolCalls(context.Background(), sess, toolCalls, nil)
	if err != nil {
		t.Fatalf("ExecuteToolCalls propagated the panic as an error: %v", err)
	}
	if !called {
		t.Fatal("execution did not continue to the call after the panicking one")
	}

	// Both calls must have results in history: the panic surfaces as an
	// error-shaped tool result, the healthy call as its normal result.
	var panicResult, healthyResult string
	for _, msg := range sess.History {
		if msg.Role != "tool" {
			continue
		}
		switch msg.ToolCallID {
		case "tc_1":
			panicResult = msg.Content.String()
		case "tc_2":
			healthyResult = msg.Content.String()
		}
	}
	if !strings.Contains(panicResult, "panicked") || !strings.Contains(panicResult, "boom") {
		t.Fatalf("panicking call result does not carry the panic: %q", panicResult)
	}
	if healthyResult != "healthy result" {
		t.Fatalf("healthy call result = %q, want %q", healthyResult, "healthy result")
	}
}

// TestRunToolCall_RecoversPanicAsError exercises the containment primitive
// directly: a recovered panic yields a zero result and a non-nil error that
// names both the tool and the panic value.
func TestRunToolCall_RecoversPanicAsError(t *testing.T) {
	var runner common.ToolRunner = func(ctx context.Context, tc client.ToolCall) (string, error) {
		panic("kaboom")
	}
	result, err := runToolCall(runner, context.Background(), client.ToolCall{
		ID:       "tc_x",
		Function: client.FunctionCall{Name: "exploder", Arguments: "{}"},
	})
	if err == nil {
		t.Fatal("runToolCall returned nil error for a panicking runner")
	}
	if result != "" {
		t.Fatalf("runToolCall returned a result alongside the panic error: %q", result)
	}
	if !strings.Contains(err.Error(), "exploder") || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("panic error does not name the tool and the panic value: %v", err)
	}
}

// TestRunToolCall_PassesThroughNormalResults guards against the containment
// wrapper changing the success path.
func TestRunToolCall_PassesThroughNormalResults(t *testing.T) {
	var runner common.ToolRunner = func(ctx context.Context, tc client.ToolCall) (string, error) {
		return "ok", nil
	}
	result, err := runToolCall(runner, context.Background(), client.ToolCall{
		ID:       "tc_y",
		Function: client.FunctionCall{Name: "calm", Arguments: "{}"},
	})
	if err != nil || result != "ok" {
		t.Fatalf("runToolCall(healthy) = (%q, %v), want (\"ok\", nil)", result, err)
	}
}
