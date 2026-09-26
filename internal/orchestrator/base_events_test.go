package orchestrator

import (
	"fmt"
	"late/internal/client"
	"late/internal/session"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// TestBaseOrchestrator_ExecuteDoesNotDeadlockWhenEventConsumerStalls pins the
// non-blocking behavior of the progress-event sends: with a NEVER-read eventCh
// (no consumer goroutine at all), Execute must stream past the buffered-100
// channel capacity instead of deadlocking on the 101st ContentEvent send.
//
// The fake SSE server streams 150 content chunks. The two transient "thinking"
// statuses and the first 98 chunk events fill the 100-slot buffer; the
// remaining 52 chunk sends must be dropped (counted in droppedEvents), not
// blocking. Before the hardening, Execute wedged forever inside the stream
// callback with droppedEvents == 0 and this test would time out.
//
// The terminal status events are intentionally still blocking (the TUI hangs
// in its running state without them), so after observing the drops the test
// resumes consumption — as a recovered TUI would — and asserts that Execute
// returns, i.e. the only sends that ever waited were the guaranteed-delivery
// terminal ones.
func TestBaseOrchestrator_ExecuteDoesNotDeadlockWhenEventConsumerStalls(t *testing.T) {
	const chunkCount = 150
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for i := 0; i < chunkCount; i++ {
			fmt.Fprintf(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"},\"finish_reason\":\"\"}]}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer ts.Close()

	tmpDir := t.TempDir()
	historyPath := filepath.Join(tmpDir, "session.json")
	initial := []client.ChatMessage{
		{Role: "user", Content: client.TextContent("goal")},
	}
	c := client.NewClient(client.Config{BaseURL: ts.URL})
	sess := session.New(c, historyPath, initial, "", false)
	o := NewBaseOrchestrator("test", sess, nil, 1)

	// NO consumer for o.eventCh — this is the point of the test.
	done := make(chan error, 1)
	var res string
	go func() {
		r, err := o.Execute("")
		res = r
		done <- err
	}()

	// Wait until the stream has overflowed the buffer: proves >100 progress
	// events were sent through the onStreamChunk callback path without any
	// consumer and without deadlock. Exact counts are not asserted (only the
	// two thinking statuses precede the chunks; >= half the stream suffices).
	deadline := time.Now().Add(15 * time.Second)
	for o.droppedEvents.Load() < 50 {
		if time.Now().After(deadline) {
			t.Fatalf("progress events were never dropped (droppedEvents=%d): Execute appears blocked on a full eventCh", o.droppedEvents.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Snapshot now: onEndTurn reports and resets the counter once the
	// (blocking) turn-boundary ContentEvent is delivered below.
	observedDrops := o.droppedEvents.Load()

	// Resume consumption so the intentionally-blocking terminal sends can be
	// delivered, and verify Execute returns.
	stopDrain := make(chan struct{})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			select {
			case <-o.eventCh:
			case <-stopDrain:
				return
			}
		}
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Execute did not return after the event consumer resumed (deadlock)")
	}
	close(stopDrain)
	<-drained

	if got := observedDrops; got < 50 {
		t.Fatalf("droppedEvents = %d, want >= 50 (150 chunks overflow the 100-slot buffer)", got)
	}
	// The run loop must have accumulated the FULL stream even though UI
	// events were dropped: the session history is authoritative, not the
	// lossy event channel.
	if len(res) != chunkCount {
		t.Fatalf("Execute() content length = %d, want %d", len(res), chunkCount)
	}
}
