//go:build !windows

package executor

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"late/internal/client"
	"late/internal/common"
	"late/internal/session"
)

// harnessSession builds a session with the bash tool registered exactly the
// way RegisterTools does (as *tool.ShellTool).
func harnessSession(t *testing.T) *session.Session {
	t.Helper()
	c := client.NewClient(client.Config{BaseURL: "http://localhost:0"})
	histPath := filepath.Join(t.TempDir(), "history.json")
	sess := session.New(c, histPath, nil, "", true)
	RegisterTools(sess.Registry, map[string]bool{"bash": true})
	return sess
}

// approvedMiddleware mirrors how the TUI confirm middleware approves a tool
// call: it injects common.ToolApprovalKey into the context before the base
// runner executes. A non-empty middleware slice also bypasses the
// ExecuteToolCalls no-middleware fail-closed shell guard, so the command
// actually runs.
func approvedMiddleware() common.ToolMiddleware {
	return func(next common.ToolRunner) common.ToolRunner {
		return func(ctx context.Context, tc client.ToolCall) (string, error) {
			return next(context.WithValue(ctx, common.ToolApprovalKey, true), tc)
		}
	}
}

func withOrchestrator(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, common.OrchestratorIDKey, id)
}

// TestExecuteToolCalls_CoderShellFailureNoteIsSeparateMessage verifies that a
// failed shell command run by a coder subagent produces a CLEAN tool result
// plus a SEPARATE user-role harness note message in session history — the
// note must not be appended inside the tool-result string adjacent to
// untrusted command output.
func TestExecuteToolCalls_CoderShellFailureNoteIsSeparateMessage(t *testing.T) {
	sess := harnessSession(t)
	ctx := withOrchestrator(context.Background(), "coder-subagent-1")

	toolCalls := []client.ToolCall{
		{ID: "tc_1", Function: client.FunctionCall{Name: "bash", Arguments: `{"command":"false"}`}},
	}

	if err := ExecuteToolCalls(ctx, sess, toolCalls, []common.ToolMiddleware{approvedMiddleware()}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sess.History) != 2 {
		t.Fatalf("expected 2 history entries (tool result + harness note), got %d", len(sess.History))
	}
	toolMsg, noteMsg := sess.History[0], sess.History[1]

	if toolMsg.Role != "tool" {
		t.Errorf("expected first message role 'tool', got %q", toolMsg.Role)
	}
	toolText := toolMsg.Content.String()
	if !strings.HasPrefix(toolText, "Command failed with exit code ") {
		t.Errorf("expected a shell failure tool result, got %q", toolText)
	}
	if strings.Contains(toolText, "[late harness]") {
		t.Errorf("tool result must not contain the harness note, got %q", toolText)
	}

	if noteMsg.Role != "user" {
		t.Errorf("expected harness note role 'user', got %q", noteMsg.Role)
	}
	noteText := noteMsg.Content.String()
	if !strings.HasPrefix(noteText, "[late harness] error note:") {
		t.Errorf("expected harness note as its own message, got %q", noteText)
	}
	if !strings.Contains(noteText, "stop and report back to the main agent") {
		t.Errorf("expected delegation-boundary guidance in the note, got %q", noteText)
	}

	// Order: the note follows the tool result it refers to.
	if toolMsg.ToolCallID != "tc_1" {
		t.Errorf("expected tool result for tc_1 first, got ToolCallID %q", toolMsg.ToolCallID)
	}
}

// TestExecuteToolCalls_UserStopDoesNotAttachNote verifies that a user stop
// (context canceled before the call) does NOT attach the harness note: the
// shell command is killed ("Error executing command: signal: killed"), which
// matches IsShellFailureResult and would otherwise produce a noisy
// "report back" note right after the user explicitly stopped the agent. The
// tool result itself must still land in history. A timeout
// (context.DeadlineExceeded) is NOT covered by this guard and still gets the
// note (see TestIsShellFailureResult in internal/tool for the timeout prefix).
func TestExecuteToolCalls_UserStopDoesNotAttachNote(t *testing.T) {
	sess := harnessSession(t)
	ctx, cancel := context.WithCancel(withOrchestrator(context.Background(), "coder-subagent-1"))
	cancel() // user pressed stop before the tool call ran

	toolCalls := []client.ToolCall{
		{ID: "tc_1", Function: client.FunctionCall{Name: "bash", Arguments: `{"command":"false"}`}},
	}

	if err := ExecuteToolCalls(ctx, sess, toolCalls, []common.ToolMiddleware{approvedMiddleware()}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var toolResult string
	for _, msg := range sess.History {
		if text := msg.Content.String(); strings.Contains(text, "[late harness]") {
			t.Fatalf("user stop must not attach the harness note, got %q", text)
		} else if msg.Role == "tool" && msg.ToolCallID == "tc_1" {
			toolResult = text
		}
	}
	if toolResult == "" {
		t.Fatalf("expected the bash tool result in history, got %d messages", len(sess.History))
	}
}

// TestExecuteToolCalls_NonCoderShellFailureHasNoNote verifies the harness note
// is scoped to coder subagents.
func TestExecuteToolCalls_NonCoderShellFailureHasNoNote(t *testing.T) {
	sess := harnessSession(t)
	ctx := withOrchestrator(context.Background(), "researcher-1")

	toolCalls := []client.ToolCall{
		{ID: "tc_1", Function: client.FunctionCall{Name: "bash", Arguments: `{"command":"false"}`}},
	}

	if err := ExecuteToolCalls(ctx, sess, toolCalls, []common.ToolMiddleware{approvedMiddleware()}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sess.History) != 1 {
		t.Fatalf("expected 1 history entry (tool result only), got %d", len(sess.History))
	}
	if sess.History[0].Role != "tool" {
		t.Errorf("expected role 'tool', got %q", sess.History[0].Role)
	}
	if strings.Contains(sess.History[0].Content.String(), "[late harness]") {
		t.Errorf("non-coder failure must not carry the harness note, got %q", sess.History[0].Content.String())
	}
}

// TestExecuteToolCalls_CoderShellSuccessHasNoNote verifies successful commands
// never get the harness note.
func TestExecuteToolCalls_CoderShellSuccessHasNoNote(t *testing.T) {
	sess := harnessSession(t)
	ctx := withOrchestrator(context.Background(), "coder-subagent-1")

	toolCalls := []client.ToolCall{
		{ID: "tc_1", Function: client.FunctionCall{Name: "bash", Arguments: `{"command":"echo ok"}`}},
	}

	if err := ExecuteToolCalls(ctx, sess, toolCalls, []common.ToolMiddleware{approvedMiddleware()}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sess.History) != 1 {
		t.Fatalf("expected 1 history entry (tool result only), got %d", len(sess.History))
	}
	if sess.History[0].Role != "tool" {
		t.Errorf("expected role 'tool', got %q", sess.History[0].Role)
	}
	if strings.Contains(sess.History[0].Content.String(), "[late harness]") {
		t.Errorf("successful command must not carry the harness note, got %q", sess.History[0].Content.String())
	}
}

// TestExecuteToolCalls_CoderNonShellFailureHasNoNote verifies the harness note
// is scoped to shell results only: another tool failing under a coder context
// must not trigger it.
func TestExecuteToolCalls_CoderNonShellFailureHasNoNote(t *testing.T) {
	sess := harnessSession(t)
	RegisterTools(sess.Registry, map[string]bool{"write_file": true})
	ctx := withOrchestrator(context.Background(), "coder-subagent-1")

	// write_file with empty content fails inside the tool.
	toolCalls := []client.ToolCall{
		{ID: "tc_1", Function: client.FunctionCall{Name: "write_file", Arguments: `{"path":"note-test.txt","content":""}`}},
	}

	if err := ExecuteToolCalls(ctx, sess, toolCalls, []common.ToolMiddleware{approvedMiddleware()}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sess.History) != 1 {
		t.Fatalf("expected 1 history entry (tool result only), got %d", len(sess.History))
	}
	if sess.History[0].Role != "tool" {
		t.Errorf("expected role 'tool', got %q", sess.History[0].Role)
	}
	if strings.Contains(sess.History[0].Content.String(), "[late harness]") {
		t.Errorf("non-shell tool failure must not carry the harness note, got %q", sess.History[0].Content.String())
	}
}
