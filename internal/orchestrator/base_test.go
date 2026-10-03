package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"late/internal/client"
	"late/internal/common"
	"late/internal/session"
	"late/internal/tool"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
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

	// (Step 14) Rewinding clamps the compaction high-water mark to the
	// truncated history — the frozen prefix never outlives the history it
	// froze — and the clamp persists through the metadata write.
	sess.SetCompactionHighWater(4)
	if err := o.Rewind(1); err != nil {
		t.Fatalf("Failed to rewind to 1: %v", err)
	}
	if got, want := sess.CompactionHighWater(), 1; got != want {
		t.Errorf("CompactionHighWater after rewind to 1 = %d, want %d", got, want)
	}
	meta, err := session.LoadSessionMeta("history")
	if err != nil || meta == nil {
		t.Fatalf("LoadSessionMeta(history) = (%v, %v)", meta, err)
	}
	if meta.CompactionHighWater != 1 {
		t.Errorf("persisted CompactionHighWater after rewind = %d, want 1", meta.CompactionHighWater)
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
	var todos []tool.Todo
	var todoMu sync.Mutex
	createTodos := tool.CreateTodosTool{Todos: &todos, Mu: &todoMu}
	listTodos := tool.ListTodosTool{Todos: &todos, Mu: &todoMu}
	sess.Registry.Register(createTodos)
	sess.Registry.Register(listTodos)
	ctx := context.WithValue(context.Background(), common.OrchestratorIDKey, common.MainAgentID)
	if _, err := createTodos.Execute(ctx, json.RawMessage(`{"todos":["old conversation task"]}`)); err != nil {
		t.Fatalf("creating todo: %v", err)
	}
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
	if got := listTodos.GetTodos(); len(got) != 0 {
		t.Fatalf("new conversation retained todos: %#v", got)
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

func TestBaseOrchestrator_Execute_EmptyTextDoesNotAddMessage(t *testing.T) {
	// Execute's run loop commits via AddAssistantMessageWithTools →
	// saveAndNotify → UpdateSessionMetadata, which writes the .meta.json
	// sidecar into the global sessions dir. Redirect it so nothing leaves
	// the temp dir.
	tmpDir := t.TempDir()
	originalSessionDir := session.SessionDir
	session.SessionDir = func() (string, error) { return tmpDir, nil }
	t.Cleanup(func() { session.SessionDir = originalSessionDir })

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer ts.Close()

	historyPath := filepath.Join(tmpDir, "session.json")
	initial := []client.ChatMessage{
		{Role: "user", Content: client.TextContent("initial goal")},
	}
	c := client.NewClient(client.Config{BaseURL: ts.URL})
	sess := session.New(c, historyPath, initial, "", false)
	o := NewBaseOrchestrator("test", sess, nil, 1)

	_, _ = o.Execute("")

	userMsgCount := 0
	for _, msg := range o.History() {
		if msg.Role == "user" {
			userMsgCount++
			if msg.Content.String() != "initial goal" {
				t.Fatalf("unexpected user message in history: %#v", msg)
			}
		}
	}
	if userMsgCount != 1 {
		t.Fatalf("expected exactly 1 user message, got %d", userMsgCount)
	}
}

func TestBaseOrchestrator_DrainQueuedMessages(t *testing.T) {
	tmpDir := t.TempDir()
	historyPath := filepath.Join(tmpDir, "session.json")
	c := client.NewClient(client.Config{BaseURL: "http://localhost:0"})
	sess := session.New(c, historyPath, nil, "", false)
	o := NewBaseOrchestrator("test", sess, nil, 1)

	// Simulate orchestrator is currently running
	o.isRunning = true

	// Submit queued messages
	if err := o.Submit("queued 1", nil); err != nil {
		t.Fatalf("Submit error: %v", err)
	}
	if err := o.Submit("queued 2", nil); err != nil {
		t.Fatalf("Submit error: %v", err)
	}

	queued := o.QueuedMessages()
	if len(queued) != 2 || queued[0] != "queued 1" || queued[1] != "queued 2" {
		t.Fatalf("QueuedMessages() = %v, want [queued 1, queued 2]", queued)
	}

	drained := o.DrainQueuedMessages()
	if len(drained) != 2 || drained[0] != "queued 1" || drained[1] != "queued 2" {
		t.Fatalf("DrainQueuedMessages() = %v, want [queued 1, queued 2]", drained)
	}

	// After draining, QueuedMessages should be empty
	if len(o.QueuedMessages()) != 0 {
		t.Fatalf("QueuedMessages() after drain = %v, want empty", o.QueuedMessages())
	}

	// Subsequent drain returns nil
	if o.DrainQueuedMessages() != nil {
		t.Fatalf("DrainQueuedMessages() second call want nil")
	}
}

func TestBaseOrchestrator_CancelClearsQueuedMessages(t *testing.T) {
	tmpDir := t.TempDir()
	historyPath := filepath.Join(tmpDir, "session.json")
	c := client.NewClient(client.Config{BaseURL: "http://localhost:0"})
	sess := session.New(c, historyPath, nil, "", false)
	o := NewBaseOrchestrator("test", sess, nil, 1)

	o.isRunning = true
	_ = o.Submit("queued prompt", nil)

	if len(o.QueuedMessages()) != 1 {
		t.Fatalf("expected 1 queued message")
	}

	o.Cancel()

	if len(o.QueuedMessages()) != 0 {
		t.Fatalf("Cancel() did not clear queued messages: %v", o.QueuedMessages())
	}
}

func TestBaseOrchestrator_CancelDuringRunDoesNotCommitQueuedMessages(t *testing.T) {
	// The "turn 1" submit runs a full turn: AddUserMessage + the run-loop's
	// AddAssistantMessageWithTools both persist via saveAndNotify →
	// UpdateSessionMetadata, writing the .meta.json sidecar into the global
	// sessions dir. Redirect it so nothing leaves the temp dir.
	tmpDir := t.TempDir()
	originalSessionDir := session.SessionDir
	session.SessionDir = func() (string, error) { return tmpDir, nil }
	t.Cleanup(func() { session.SessionDir = originalSessionDir })

	// Setup a server that holds the connection until cancelled
	holdCh := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-holdCh
	}))
	defer ts.Close()
	defer close(holdCh)

	historyPath := filepath.Join(tmpDir, "session.json")
	c := client.NewClient(client.Config{BaseURL: ts.URL})
	sess := session.New(c, historyPath, nil, "", false)
	o := NewBaseOrchestrator("test", sess, nil, 5)

	// Start initial turn
	if err := o.Submit("turn 1", nil); err != nil {
		t.Fatalf("Submit turn 1 failed: %v", err)
	}

	// Wait briefly so run() starts
	time.Sleep(50 * time.Millisecond)

	// Queue turn 2
	if err := o.Submit("turn 2", nil); err != nil {
		t.Fatalf("Submit turn 2 failed: %v", err)
	}

	// Drain queued messages (simulating user pressing ctrl+g)
	drained := o.DrainQueuedMessages()
	if len(drained) != 1 || drained[0] != "turn 2" {
		t.Fatalf("drained = %v, want [turn 2]", drained)
	}

	// Cancel orchestrator
	o.Cancel()

	// Wait for run() to finish
	deadline := time.Now().Add(2 * time.Second)
	for {
		o.mu.RLock()
		running := o.isRunning
		o.mu.RUnlock()
		if !running || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	o.mu.RLock()
	running := o.isRunning
	o.mu.RUnlock()
	if running {
		t.Fatalf("orchestrator is still running after Cancel()")
	}

	// Verify that "turn 2" was NEVER committed to session history
	for _, m := range o.History() {
		if m.Content.String() == "turn 2" {
			t.Fatalf("turn 2 was committed to session history despite cancellation")
		}
	}
}
