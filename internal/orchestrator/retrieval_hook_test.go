package orchestrator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"late/internal/client"
	"late/internal/session"
)

// TestBaseOrchestrator_RetrievalHookRunsPerTurn: the hook installed by main
// behind compaction-retrieval fires at every turn start — right before that
// turn's stream request — and only when installed (the default is no-op).
func TestBaseOrchestrator_RetrievalHookRunsPerTurn(t *testing.T) {
	// The base_test.go stream stub: an immediate empty completion.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer ts.Close()

	c := client.NewClient(client.Config{BaseURL: ts.URL})
	sess := session.New(c, "", []client.ChatMessage{
		{Role: "user", Content: client.TextContent("hi")},
	}, "", false)

	o := NewBaseOrchestrator("test-orch", sess, nil, 1)
	var calls atomic.Int32
	o.SetRetrievalHook(func(context.Context) { calls.Add(1) })

	if _, err := o.Execute(""); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("retrieval hook ran %d times for a 1-turn run, want 1", got)
	}
}

// TestBaseOrchestrator_NoRetrievalHookIsNoOp: without a hook the turn start
// proceeds untouched (the default, compaction-retrieval off).
func TestBaseOrchestrator_NoRetrievalHookIsNoOp(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer ts.Close()

	c := client.NewClient(client.Config{BaseURL: ts.URL})
	sess := session.New(c, "", []client.ChatMessage{
		{Role: "user", Content: client.TextContent("hi")},
	}, "", false)

	o := NewBaseOrchestrator("test-orch", sess, nil, 1)
	if _, err := o.Execute(""); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}
