package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"late/internal/client"
)

// buildHistory creates n messages alternating user/assistant roles.
func buildHistory(n int) []client.ChatMessage {
	msgs := make([]client.ChatMessage, n)
	for i := range msgs {
		if i%2 == 0 {
			msgs[i] = client.ChatMessage{Role: "user", Content: fmt.Sprintf("user message %d", i)}
		} else {
			msgs[i] = client.ChatMessage{Role: "assistant", Content: fmt.Sprintf("assistant message %d", i)}
		}
	}
	return msgs
}

// newTestSession creates a Session with a real history path under t.TempDir(),
// changing the working directory to tmpDir so implementation_plan.md lookups
// work predictably.
func newTestSession(t *testing.T, history []client.ChatMessage, systemPrompt string) (*Session, string) {
	t.Helper()
	tmpDir := t.TempDir()
	// Change CWD so implementation_plan.md reads resolve correctly.
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })

	histPath := filepath.Join(tmpDir, "history.json")
	sess := New(nil, histPath, history, systemPrompt, false)
	return sess, tmpDir
}

// --- Unit Tests: PruneAndRestoreFromDisk ---

// TestPrune_ReducesHistory verifies the history is significantly shortened.
func TestPrune_ReducesHistory(t *testing.T) {
	history := buildHistory(100)
	sess, _ := newTestSession(t, history, "you are an agent")

	if err := sess.PruneAndRestoreFromDisk(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sess.History) >= 20 {
		t.Errorf("expected history < 20 after prune, got %d", len(sess.History))
	}
}

// TestPrune_SystemPromptUntouched asserts s.systemPrompt is never modified.
func TestPrune_SystemPromptUntouched(t *testing.T) {
	const prompt = "you are the lead architect"
	history := buildHistory(50)
	sess, _ := newTestSession(t, history, prompt)

	if err := sess.PruneAndRestoreFromDisk(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if sess.systemPrompt != prompt {
		t.Errorf("systemPrompt was mutated: got %q", sess.systemPrompt)
	}
}

// TestPrune_TailStartsWithUser asserts the boundary guard works:
// after pruning, the first history message must have Role == "user".
func TestPrune_TailStartsWithUser(t *testing.T) {
	// Build a history whose last 10 messages start with non-user roles
	// (assistant, tool, tool, assistant, ...) to exercise the boundary trim.
	history := buildHistory(20)
	// Append 5 orphaned tool/assistant messages at the tail.
	for i := 0; i < 5; i++ {
		history = append(history, client.ChatMessage{
			Role:    "assistant",
			Content: fmt.Sprintf("orphan assistant %d", i),
		})
	}
	sess, _ := newTestSession(t, history, "")

	if err := sess.PruneAndRestoreFromDisk(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sess.History) == 0 {
		t.Fatal("history is empty after prune")
	}
	if sess.History[0].Role != "user" {
		t.Errorf("expected first message role 'user', got %q", sess.History[0].Role)
	}
}

// TestPrune_MissionInjected asserts that when a valid implementation_plan.md
// is present and under 8000 chars, it is injected as the first history message.
func TestPrune_MissionInjected(t *testing.T) {
	history := buildHistory(50)
	sess, tmpDir := newTestSession(t, history, "")

	plan := "# Implementation Plan\n## Step 1\nDo the thing."
	if err := os.WriteFile(filepath.Join(tmpDir, "implementation_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}

	if err := sess.PruneAndRestoreFromDisk(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sess.History) == 0 {
		t.Fatal("history is empty after prune")
	}
	first := sess.History[0]
	if first.Role != "user" {
		t.Errorf("expected plan injection role 'user', got %q", first.Role)
	}
	if !strings.HasPrefix(first.Content, "RESTORED MISSION PLAN: ") {
		t.Errorf("expected 'RESTORED MISSION PLAN:' prefix, got %q", first.Content[:min(50, len(first.Content))])
	}
	if !strings.Contains(first.Content, plan) {
		t.Error("injected message does not contain the plan content")
	}
}

// TestPrune_PlanAbsent_NoInjection verifies no plan message is injected when
// implementation_plan.md does not exist (plain chat workflow).
func TestPrune_PlanAbsent_NoInjection(t *testing.T) {
	history := buildHistory(50)
	sess, _ := newTestSession(t, history, "")
	// No plan file written.

	if err := sess.PruneAndRestoreFromDisk(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, msg := range sess.History {
		if strings.HasPrefix(msg.Content, "RESTORED MISSION PLAN:") {
			t.Error("plan message injected even though no file exists")
		}
	}
}

// TestPrune_PlanTooBig_Skipped asserts the size guard: a plan >= 8000 chars
// must not be injected (to prevent recovery-loop due to plan bloat).
func TestPrune_PlanTooBig_Skipped(t *testing.T) {
	history := buildHistory(50)
	sess, tmpDir := newTestSession(t, history, "")

	bigPlan := strings.Repeat("x", 8001)
	if err := os.WriteFile(filepath.Join(tmpDir, "implementation_plan.md"), []byte(bigPlan), 0644); err != nil {
		t.Fatal(err)
	}

	if err := sess.PruneAndRestoreFromDisk(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, msg := range sess.History {
		if strings.HasPrefix(msg.Content, "RESTORED MISSION PLAN:") {
			t.Error("oversized plan was injected; size guard failed")
		}
	}
}

// TestPrune_SmallHistory_NoPanic ensures sessions with fewer than 10 messages
// are handled safely (no out-of-bounds panic).
func TestPrune_SmallHistory_NoPanic(t *testing.T) {
	history := buildHistory(3) // fewer than tail window
	sess, _ := newTestSession(t, history, "")

	if err := sess.PruneAndRestoreFromDisk(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should have at most 3 messages (the original set, boundary-trimmed).
	if len(sess.History) > 3 {
		t.Errorf("expected <= 3 messages for small history, got %d", len(sess.History))
	}
}

// TestPrune_EmptyHistory_NoPanic ensures an empty session does not panic.
func TestPrune_EmptyHistory_NoPanic(t *testing.T) {
	sess, _ := newTestSession(t, nil, "")
	// Should not panic or error.
	if err := sess.PruneAndRestoreFromDisk(); err != nil {
		t.Fatalf("unexpected error on empty history: %v", err)
	}
}

// TestPrune_PersistenceSync verifies the on-disk history is updated after prune.
func TestPrune_PersistenceSync(t *testing.T) {
	history := buildHistory(50)
	tmpDir := t.TempDir()
	orig, _ := os.Getwd()
	os.Chdir(tmpDir)
	t.Cleanup(func() { os.Chdir(orig) })

	histPath := filepath.Join(tmpDir, "history.json")
	sess := New(nil, histPath, history, "", false)

	if err := sess.PruneAndRestoreFromDisk(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Reload from disk and verify it reflects the pruned state.
	loaded, err := LoadHistory(histPath)
	if err != nil {
		t.Fatalf("failed to reload history: %v", err)
	}
	if len(loaded) != len(sess.History) {
		t.Errorf("on-disk history length %d != in-memory length %d", len(loaded), len(sess.History))
	}
}

// --- Integration: Dual-Signal Detection thresholds ---

// TestMsgCeilingThreshold verifies that 81 messages exceed the hard ceiling (> 80).
func TestMsgCeilingThreshold(t *testing.T) {
	const ceiling = 80
	history := buildHistory(81)
	if len(history) <= ceiling {
		t.Fatalf("setup error: history length %d should exceed ceiling %d", len(history), ceiling)
	}
	// This test documents the threshold contract; actual RunLoop invocation is
	// covered by the executor_test.go integration tests.
}

// TestTokenTelemetryRatio verifies the 75% threshold arithmetic is correct.
func TestTokenTelemetryRatio(t *testing.T) {
	const maxCtx = 65536       // 64k
	const promptTokens = 49152 // 75% of 64k
	ratio := float64(promptTokens) / float64(maxCtx)
	if ratio < 0.75 {
		t.Errorf("expected ratio >= 0.75 for overflow trigger, got %.4f", ratio)
	}

	const safeTokens = 32768 // 50%
	safeRatio := float64(safeTokens) / float64(maxCtx)
	if safeRatio >= 0.75 {
		t.Errorf("expected ratio < 0.75 for safe usage, got %.4f", safeRatio)
	}
}

// min is a local helper to stay compatible with older Go versions in test code.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestPrune_OrphanedToolCallsRemoved verifies that the structural sanitizer
// removes assistant messages whose tool_call_ids are not resolved within the
// tail. This is the exact condition that produces a 400 from OpenAI-compatible
// APIs when pruning splits a multi-turn tool exchange.
func TestPrune_OrphanedToolCallsRemoved(t *testing.T) {
	// Build a history where the last 10 messages contain an assistant message
	// with tool_calls whose results are NOT in the tail window.
	var history []client.ChatMessage

	// Fill with 70 clean messages so the tail starts mid-exchange.
	for i := 0; i < 70; i++ {
		if i%2 == 0 {
			history = append(history, client.ChatMessage{Role: "user", Content: fmt.Sprintf("u%d", i)})
		} else {
			history = append(history, client.ChatMessage{Role: "assistant", Content: fmt.Sprintf("a%d", i)})
		}
	}

	// Message 70: assistant with tool_calls (results will NOT be in tail)
	history = append(history, client.ChatMessage{
		Role:    "assistant",
		Content: "",
		ToolCalls: []client.ToolCall{
			{ID: "call_orphan", Function: client.FunctionCall{Name: "read_file", Arguments: `{"path":"x"}`}},
		},
	})
	// Message 71: tool result for call_orphan (also NOT in tail of 10)
	history = append(history, client.ChatMessage{
		Role:       "tool",
		ToolCallID: "call_orphan",
		Content:    "file content",
	})

	// Messages 72–81: clean 10 messages that form the tail
	history = append(history, client.ChatMessage{Role: "user", Content: "clean start"})
	history = append(history, client.ChatMessage{
		Role:    "assistant",
		Content: "",
		ToolCalls: []client.ToolCall{
			{ID: "call_resolved", Function: client.FunctionCall{Name: "bash", Arguments: `{"command":"ls"}`}},
		},
	})
	history = append(history, client.ChatMessage{Role: "tool", ToolCallID: "call_resolved", Content: "file.go"})
	history = append(history, client.ChatMessage{Role: "assistant", Content: "done"})
	history = append(history, client.ChatMessage{Role: "user", Content: "next"})
	history = append(history, client.ChatMessage{Role: "assistant", Content: "ok"})
	history = append(history, client.ChatMessage{Role: "user", Content: "final"})
	// tail is now exactly 7 messages (72-78); pad to get past the 10-msg window for msg 70
	// Actual window: last 10 = messages [69..78] → includes msg 70 (assistant orphan) and 71 (tool)
	// but NOT the tool result "call_orphan" is at msg 71 which IS in the tail...
	// Let me rebuild so the orphan's RESULT is outside the window.

	// Reset and rebuild more carefully:
	history = nil
	// 72 base messages
	for i := 0; i < 72; i++ {
		if i%2 == 0 {
			history = append(history, client.ChatMessage{Role: "user", Content: fmt.Sprintf("u%d", i)})
		} else {
			history = append(history, client.ChatMessage{Role: "assistant", Content: fmt.Sprintf("a%d", i)})
		}
	}
	// msg[72]: assistant with unresolved tool_call (its result was already committed at msg[71])
	// The tail = last 10 = msg[63..72]. msg[72] has tool_calls but msg[73] is not yet added.
	// Actually we need the tool RESULT to be outside (before) the tail window.
	// Tail starts at len-10. Insert assistant+tool at positions that put the result outside:
	// Insert at positions 60 and 61 (assistant+tool), then fill to 82 messages.
	history = nil
	for i := 0; i < 60; i++ {
		if i%2 == 0 {
			history = append(history, client.ChatMessage{Role: "user", Content: fmt.Sprintf("u%d", i)})
		} else {
			history = append(history, client.ChatMessage{Role: "assistant", Content: fmt.Sprintf("a%d", i)})
		}
	}
	// pos 60: assistant with tool_calls (result will be pos 61 — outside tail window)
	history = append(history, client.ChatMessage{
		Role: "assistant",
		ToolCalls: []client.ToolCall{
			{ID: "call_outside", Function: client.FunctionCall{Name: "read_file", Arguments: `{"path":"f"}`}},
		},
	})
	// pos 61: tool result (this will be OUTSIDE the last-10 window when total len >= 72)
	history = append(history, client.ChatMessage{Role: "tool", ToolCallID: "call_outside", Content: "data"})
	// pos 62-81: 20 more clean messages (so tail = last 10 = pos 72-81, cutting out pos 61)
	for i := 0; i < 20; i++ {
		if i%2 == 0 {
			history = append(history, client.ChatMessage{Role: "user", Content: fmt.Sprintf("tail-u%d", i)})
		} else {
			history = append(history, client.ChatMessage{Role: "assistant", Content: fmt.Sprintf("tail-a%d", i)})
		}
	}

	sess, _ := newTestSession(t, history, "")
	if err := sess.PruneAndRestoreFromDisk(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify no message references an unresolved tool_call_id.
	// Build the set of tool_call_ids that have results in the pruned history.
	resolvedIDs := map[string]bool{}
	for _, m := range sess.History {
		if m.Role == "tool" && m.ToolCallID != "" {
			resolvedIDs[m.ToolCallID] = true
		}
	}
	for _, m := range sess.History {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				if !resolvedIDs[tc.ID] {
					t.Errorf("pruned history contains unresolved tool_call_id %q (would cause 400)", tc.ID)
				}
			}
		}
	}

	// Verify history still starts with user role.
	if len(sess.History) > 0 && sess.History[0].Role != "user" {
		t.Errorf("expected first message role 'user', got %q", sess.History[0].Role)
	}
}
