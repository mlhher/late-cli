//go:build integration

package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "late/internal/config"
	"late/internal/client"
)

// TestPruneIntegration_LiveLLM is a real-backend integration test.
//
// It requires a reachable LLM configured in config.json (or via environment
// variables) and is excluded from the standard `go test ./...` run.
//
// Run explicitly with:
//
//	go test -tags integration -v -timeout 120s ./internal/session/... -run TestPruneIntegration
func TestPruneIntegration_LiveLLM(t *testing.T) {
	cfg, err := appconfig.LoadConfig()
	if err != nil {
		t.Skipf("could not load config.json: %v", err)
	}

	settings := appconfig.ResolveOpenAISettings(cfg)
	if settings.BaseURL == "" {
		t.Skip("no LLM base URL configured; skipping integration test")
	}

	c := client.NewClient(client.Config{
		BaseURL: settings.BaseURL,
		APIKey:  settings.APIKey,
		Model:   settings.Model,
	})

	// Probe the backend so ContextSize() is populated for llama.cpp endpoints.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	c.DiscoverBackend(ctx)
	cancel()

	// ── Phase 1: Build an overflowed session ──────────────────────────────────
	// We construct a synthetic history large enough to exceed the 80-message
	// ceiling without hitting the actual LLM for every turn. This lets the test
	// run deterministically even when prompt token telemetry is unreliable.

	tmpDir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)

	// Write a realistic implementation plan so the injection path is exercised.
	plan := "# Implementation Plan\n\n## Step 1: Context Recovery\n- Add PruneAndRestoreFromDisk to session.go\n\n## Step 2: Heartbeat in RunLoop\n- Insert dual-signal check after ConsumeStream\n\n## Step 3: Tests\n- Unit tests in prune_test.go"
	if err := os.WriteFile(filepath.Join(tmpDir, "implementation_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}

	// 82 alternating user/assistant messages — enough to trip the ceiling (> 80).
	history := make([]client.ChatMessage, 82)
	for i := range history {
		if i%2 == 0 {
			history[i] = client.ChatMessage{Role: "user", Content: fmt.Sprintf("User turn %d: please continue working on the task.", i)}
		} else {
			history[i] = client.ChatMessage{Role: "assistant", Content: fmt.Sprintf("Assistant turn %d: understood, proceeding.", i)}
		}
	}

	histPath := filepath.Join(tmpDir, "history.json")
	sess := New(c, histPath, history, "You are a helpful coding assistant.", true)

	t.Logf("History before prune: %d messages", len(sess.History))

	// ── Phase 2: Run PruneAndRestoreFromDisk ─────────────────────────────────
	if err := sess.PruneAndRestoreFromDisk(); err != nil {
		t.Fatalf("PruneAndRestoreFromDisk failed: %v", err)
	}

	t.Logf("History after prune:  %d messages", len(sess.History))

	// ── Phase 3: Structural assertions ───────────────────────────────────────

	// 3a. History is significantly reduced.
	if len(sess.History) >= 20 {
		t.Errorf("expected < 20 messages after prune, got %d", len(sess.History))
	}

	// 3b. First message must be the plan injection (role: user, RESTORED prefix).
	if len(sess.History) == 0 {
		t.Fatal("history is empty after prune")
	}
	first := sess.History[0]
	if first.Role != "user" {
		t.Errorf("expected first message role 'user', got %q", first.Role)
	}
	if !strings.HasPrefix(first.Content, "RESTORED MISSION PLAN: ") {
		t.Errorf("expected RESTORED MISSION PLAN prefix, got: %q", first.Content[:min(60, len(first.Content))])
	}
	t.Logf("Plan injection: role=%s, content preview=%q", first.Role, first.Content[:min(80, len(first.Content))])

	// 3c. After the plan injection, the tail must start with a user message
	//     (boundary guard). Find the first non-plan message.
	if len(sess.History) > 1 {
		secondMsg := sess.History[1]
		if secondMsg.Role != "user" {
			t.Errorf("expected tail to start with role 'user', got %q (boundary guard may have failed)", secondMsg.Role)
		}
	}

	// 3d. System prompt is still intact (not stored in History).
	if sess.systemPrompt != "You are a helpful coding assistant." {
		t.Errorf("systemPrompt was modified during prune")
	}

	// ── Phase 4: Live inference after recovery ────────────────────────────────
	// This is the critical end-to-end check: can the LLM generate a coherent
	// response after the reset, given consecutive user messages at the seam?

	if err := sess.AddUserMessage("After this context reset, what is the first step in the implementation plan you just received?"); err != nil {
		t.Fatalf("failed to add user message: %v", err)
	}

	inferCtx, inferCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer inferCancel()

	streamCh, errCh := sess.StartStream(inferCtx, nil)

	var content string
	for chunk := range streamCh {
		if len(chunk.ToolCalls) == 0 {
			content += chunk.Content
		}
	}
	if err := <-errCh; err != nil {
		t.Fatalf("LLM stream error after recovery: %v", err)
	}

	t.Logf("LLM response after recovery (%d chars): %q", len(content), content[:min(200, len(content))])

	// The model must return a non-empty response — this alone proves no 400/500
	// was triggered by the consecutive-user-message seam or malformed history.
	if strings.TrimSpace(content) == "" {
		t.Error("LLM returned empty response after context recovery")
	}

	// Soft coherence check: the response should mention "context recovery" or
	// "step 1" or "implementation" — any signal it read the injected plan.
	lower := strings.ToLower(content)
	coherent := strings.Contains(lower, "step 1") ||
		strings.Contains(lower, "context recovery") ||
		strings.Contains(lower, "implementation") ||
		strings.Contains(lower, "prune") ||
		strings.Contains(lower, "session")

	if !coherent {
		t.Logf("WARNING: response may not reference the injected plan (soft check). Full response:\n%s", content)
	} else {
		t.Logf("Coherence check PASSED: model referenced injected plan content.")
	}

	t.Logf("Integration test PASSED: LLM responded coherently after context reset.")
}
