package orchestrator

import (
	"context"
	"fmt"
	"late/internal/client"
	"late/internal/common"
	"late/internal/session"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestBaseOrchestrator_ResetContextIfCancelledPreservesConfiguration(t *testing.T) {
	o := NewBaseOrchestrator("test-orch", nil, nil, 10)

	ctx, cancel := context.WithCancel(context.WithValue(
		context.Background(), common.SkipConfirmationKey, true,
	))
	cancel()
	o.ctx = ctx

	o.resetContextIfCancelled()

	if err := o.ctx.Err(); err != nil {
		t.Fatalf("reset context is still cancelled: %v", err)
	}
	if skip, ok := o.ctx.Value(common.SkipConfirmationKey).(bool); !ok || !skip {
		t.Fatal("reset context lost SkipConfirmationKey")
	}
}

func TestBaseOrchestrator_Rewind(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "late-orchestrator-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)
	originalSessionDir := session.SessionDir
	session.SessionDir = func() (string, error) { return tmpDir, nil }
	t.Cleanup(func() { session.SessionDir = originalSessionDir })

	historyPath := filepath.Join(tmpDir, "history.json")
	history := []client.ChatMessage{
		{Role: "user", Content: client.TextContent("Msg 1")},
		{Role: "assistant", Content: client.TextContent("Reply 1")},
		{Role: "user", Content: client.TextContent("Msg 2")},
		{Role: "assistant", Content: client.TextContent("Reply 2")},
	}

	c := client.NewClient(client.Config{BaseURL: "http://localhost:8080"})
	sess := session.New(c, historyPath, history, "", false)
	o := NewBaseOrchestrator("test-orch", sess, nil, 10)

	// Test invalid rewind index
	if err := o.Rewind(-1); err == nil {
		t.Error("Expected error for negative index, got nil")
	}
	if err := o.Rewind(5); err == nil {
		t.Error("Expected error for out-of-bounds index, got nil")
	}

	// Rewind to index 2 (Msg 2)
	// After rewinding to index 2, history should contain index 0 and 1: Msg 1 and Reply 1.
	if err := o.Rewind(2); err != nil {
		t.Fatalf("Failed to rewind: %v", err)
	}

	updatedHistory := o.History()
	if len(updatedHistory) != 2 {
		t.Fatalf("Expected history length 2, got %d", len(updatedHistory))
	}
	if updatedHistory[0].Content.String() != "Msg 1" {
		t.Errorf("Expected first message 'Msg 1', got %q", updatedHistory[0].Content.String())
	}
	if updatedHistory[1].Content.String() != "Reply 1" {
		t.Errorf("Expected second message 'Reply 1', got %q", updatedHistory[1].Content.String())
	}
}

func TestBaseOrchestrator_ResetStartsNewConversation(t *testing.T) {
	tmpDir := t.TempDir()
	originalSessionDir := session.SessionDir
	session.SessionDir = func() (string, error) { return tmpDir, nil }
	t.Cleanup(func() { session.SessionDir = originalSessionDir })
	originalPath := filepath.Join(tmpDir, "session-original.json")
	history := []client.ChatMessage{
		{Role: "user", Content: client.TextContent("keep me")},
		{Role: "assistant", Content: client.TextContent("preserved")},
	}

	if err := session.SaveHistory(originalPath, history); err != nil {
		t.Fatal(err)
	}

	sess := session.New(nil, originalPath, history, "", false)
	o := NewBaseOrchestrator("test-orch", sess, nil, 10)
	if err := o.Reset(); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}

	preserved, err := session.LoadHistory(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(preserved) != 2 || preserved[0].Content.String() != "keep me" {
		t.Fatalf("original history was not preserved: %#v", preserved)
	}
	if len(o.History()) != 0 {
		t.Fatalf("new conversation history length = %d, want 0", len(o.History()))
	}
	if sess.HistoryPath == originalPath {
		t.Fatal("new conversation reused the original history path")
	}
	if filepath.Dir(sess.HistoryPath) != tmpDir {
		t.Fatalf("new history directory = %q, want %q", filepath.Dir(sess.HistoryPath), tmpDir)
	}
	newPath := sess.HistoryPath
	if err := sess.AddUserMessage("new chat"); err != nil {
		t.Fatalf("saving new conversation: %v", err)
	}
	newHistory, err := session.LoadHistory(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(newHistory) != 1 || newHistory[0].Content.String() != "new chat" {
		t.Fatalf("new history was not saved separately: %#v", newHistory)
	}
	preserved, err = session.LoadHistory(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(preserved) != 2 {
		t.Fatalf("saving the new conversation changed original history: %#v", preserved)
	}
}

func TestNextChildID_FormatAndMonotonic(t *testing.T) {
	o := NewBaseOrchestrator("parent", session.New(nil, "", nil, "", false), nil, 0)

	var got []string
	for i := 0; i < 3; i++ {
		id, err := o.NextChildID("researcher")
		if err != nil {
			t.Fatalf("NextChildID() error = %v", err)
		}
		got = append(got, id)
	}
	id, err := o.NextChildID("coder")
	if err != nil {
		t.Fatalf("NextChildID() error = %v", err)
	}
	got = append(got, id)

	want := []string{
		"researcher-subagent-0",
		"researcher-subagent-1",
		"researcher-subagent-2",
		// The counter is global per parent, NOT per type.
		"coder-subagent-3",
	}

	if len(got) != len(want) {
		t.Fatalf("got %d IDs, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ID %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNextChildID_Concurrent(t *testing.T) {
	o := NewBaseOrchestrator("parent", session.New(nil, "", nil, "", false), nil, 0)

	const goroutines = 64
	const perGoroutine = 16

	var mu sync.Mutex
	ids := make([]string, 0, goroutines*perGoroutine)

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				id, err := o.NextChildID("researcher")
				if err != nil {
					t.Errorf("NextChildID() error = %v", err)
					return
				}
				mu.Lock()
				ids = append(ids, id)
				mu.Unlock()
			}
			// Interleave lightweight child registration to ensure concurrent
			// AddChild calls don't interfere with ID minting.
			o.AddChild(NewBaseOrchestrator(fmt.Sprintf("dummy-%d", g), nil, nil, 0))
		}(g)
	}
	wg.Wait()

	if len(ids) != goroutines*perGoroutine {
		t.Fatalf("minted %d IDs, want %d", len(ids), goroutines*perGoroutine)
	}

	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate ID minted: %q", id)
		}
		seen[id] = struct{}{}
	}

	if n := len(o.Children()); n != goroutines {
		t.Errorf("Children() length = %d, want %d", n, goroutines)
	}
}

func TestNextChildIDPersistsSequenceForResume(t *testing.T) {
	tmpDir := t.TempDir()
	originalSessionDir := session.SessionDir
	session.SessionDir = func() (string, error) { return tmpDir, nil }
	t.Cleanup(func() { session.SessionDir = originalSessionDir })

	historyPath := filepath.Join(tmpDir, "session-test.json")
	sess := session.New(nil, historyPath, nil, "", false)
	sess.SetSubagentMetadata(4, nil)
	o := NewBaseOrchestrator("parent", sess, nil, 0)

	id, err := o.NextChildID("researcher")
	if err != nil {
		t.Fatalf("NextChildID() error = %v", err)
	}
	if id != "researcher-subagent-4" {
		t.Fatalf("NextChildID() = %q, want researcher-subagent-4", id)
	}

	meta, err := session.LoadSessionMeta("session-test")
	if err != nil {
		t.Fatalf("LoadSessionMeta() error = %v", err)
	}
	if meta.SubagentSeq != 5 {
		t.Fatalf("SubagentSeq = %d, want 5", meta.SubagentSeq)
	}

	resumed := session.New(nil, historyPath, nil, "", false)
	resumed.SetSubagentMetadata(meta.SubagentSeq, meta.SaveSubagentHistories)
	resumedOrchestrator := NewBaseOrchestrator("parent", resumed, nil, 0)
	resumedID, err := resumedOrchestrator.NextChildID("coder")
	if err != nil {
		t.Fatalf("resumed NextChildID() error = %v", err)
	}
	if resumedID != "coder-subagent-5" {
		t.Errorf("resumed NextChildID() = %q, want coder-subagent-5", resumedID)
	}
}
