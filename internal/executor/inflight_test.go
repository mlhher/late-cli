package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"late/internal/client"
	"late/internal/session"
)

// fakeTool is a minimal common.Tool implementation for executor tests.
type fakeTool struct {
	name string
	exec func(ctx context.Context, args json.RawMessage) (string, error)
}

func (f *fakeTool) Name() string        { return f.name }
func (f *fakeTool) Description() string { return "fake tool for executor tests" }
func (f *fakeTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (f *fakeTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return f.exec(ctx, args)
}
func (f *fakeTool) RequiresConfirmation(json.RawMessage) bool { return false }
func (f *fakeTool) CallString(json.RawMessage) string         { return f.name }

// TestExecuteToolCalls_InFlightToolKillContinues verifies the killable
// in-flight tool path: cancelling the in-flight tool (via the session hook the
// orchestrator's idle watchdog uses) turns the hung call into a "tool
// cancelled" result, ExecuteToolCalls keeps going, and the NEXT tool call runs
// with a fresh context on a still-alive parent.
func TestExecuteToolCalls_InFlightToolKillContinues(t *testing.T) {
	c := client.NewClient(client.Config{BaseURL: "http://localhost:0"})
	sess := session.New(c, "", nil, "", true) // no history path: in-memory session

	var sawCancel atomic.Bool
	started := make(chan struct{})
	hung := &fakeTool{name: "hung_tool", exec: func(ctx context.Context, args json.RawMessage) (string, error) {
		close(started)
		<-ctx.Done()
		sawCancel.Store(true)
		return "", fmt.Errorf("tool cancelled: %v", ctx.Err())
	}}
	quick := &fakeTool{name: "quick_tool", exec: func(ctx context.Context, args json.RawMessage) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("inherited a cancelled context: %v", err)
		}
		return "second ok", nil
	}}
	sess.Registry.Register(hung)
	sess.Registry.Register(quick)

	toolCalls := []client.ToolCall{
		{ID: "tc_1", Function: client.FunctionCall{Name: "hung_tool", Arguments: "{}"}},
		{ID: "tc_2", Function: client.FunctionCall{Name: "quick_tool", Arguments: "{}"}},
	}

	parentCtx, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()

	done := make(chan error, 1)
	go func() {
		done <- ExecuteToolCalls(parentCtx, sess, toolCalls, nil)
	}()

	// Wait until the hung tool is executing — the executor registered its
	// cancel on the session before invoking it.
	<-started

	// Kill the in-flight tool exactly the way the idle watchdog does.
	if !sess.CancelInFlightTool() {
		t.Fatal("expected a registered in-flight tool cancel while the hung tool ran")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ExecuteToolCalls returned an error after a tool kill: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ExecuteToolCalls did not finish after the in-flight tool was killed")
	}

	if !sawCancel.Load() {
		t.Error("the hung tool did not observe the cancellation of its context")
	}
	if parentCtx.Err() != nil {
		t.Errorf("parent context must stay alive after a tool kill, got %v", parentCtx.Err())
	}

	// Both results must be recorded: the cancellation for the killed call and
	// the normal output for the call that ran afterwards.
	if len(sess.History) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(sess.History))
	}
	first := sess.History[0].Content.String()
	if !strings.Contains(first, "cancelled") {
		t.Errorf("first result must report the tool cancellation, got %q", first)
	}
	if strings.Contains(first, "Error executing tool") {
		t.Errorf("a tool kill is not a tool error, got %q", first)
	}
	if second := sess.History[1].Content.String(); second != "second ok" {
		t.Errorf("second tool call must still have run with a fresh context, got %q", second)
	}

	// The hook must be cleared once the calls finished.
	if sess.CancelInFlightTool() {
		t.Error("in-flight cancel hook was not cleared after ExecuteToolCalls returned")
	}
}

// TestExecuteToolCalls_ParentCancelStillAborts pins the pre-existing
// semantics for a cancelled PARENT context: the result notes the error (it is
// not rewritten into a tool-cancellation message) and the loop still processes
// the remaining calls — the run itself is unwound by the run loop's ctx
// checks, not by ExecuteToolCalls.
func TestExecuteToolCalls_ParentCancelStillAborts(t *testing.T) {
	c := client.NewClient(client.Config{BaseURL: "http://localhost:0"})
	sess := session.New(c, "", nil, "", true)

	started := make(chan struct{})
	blocker := &fakeTool{name: "blocking_tool", exec: func(ctx context.Context, args json.RawMessage) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}}
	probe := &fakeTool{name: "probe_tool", exec: func(ctx context.Context, args json.RawMessage) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return "probe ok", nil
	}}
	sess.Registry.Register(blocker)
	sess.Registry.Register(probe)

	toolCalls := []client.ToolCall{
		{ID: "tc_1", Function: client.FunctionCall{Name: "blocking_tool", Arguments: "{}"}},
		{ID: "tc_2", Function: client.FunctionCall{Name: "probe_tool", Arguments: "{}"}},
	}

	parentCtx, parentCancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- ExecuteToolCalls(parentCtx, sess, toolCalls, nil)
	}()

	<-started
	parentCancel() // cancel the parent mid-first-call

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ExecuteToolCalls returned an error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ExecuteToolCalls did not finish after the parent context was cancelled")
	}

	if parentCtx.Err() != context.Canceled {
		t.Fatalf("parent ctx.Err() = %v, want context.Canceled", parentCtx.Err())
	}

	if len(sess.History) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(sess.History))
	}
	first := sess.History[0].Content.String()
	if !strings.Contains(first, "Error executing tool blocking_tool") ||
		!strings.Contains(first, "context canceled") {
		t.Errorf("a cancelled parent must note the error, got %q", first)
	}
	if strings.Contains(first, "tool cancelled by the harness idle watchdog") {
		t.Errorf("a parent cancellation must not be reported as a tool kill, got %q", first)
	}
	// Pre-change semantics: the loop kept processing the remaining calls; the
	// probe tool fails fast on the dead context.
	second := sess.History[1].Content.String()
	if !strings.Contains(second, "Error executing tool probe_tool") {
		t.Errorf("expected the second call to note the dead context, got %q", second)
	}

	if sess.CancelInFlightTool() {
		t.Error("in-flight cancel hook was not cleared after ExecuteToolCalls returned")
	}
}
