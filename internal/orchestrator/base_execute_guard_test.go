package orchestrator

import (
	"fmt"
	"late/internal/client"
	"late/internal/common"
	"late/internal/session"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newStallTestServer starts an SSE mock whose turn can be held open by the
// test: the handler signals gotReq once the request arrives, streams
// firstChunks content deltas (enough to overflow the 100-slot event buffer
// when firstChunks > 100), blocks until release is closed, then finishes the
// stream. It returns the release/gotReq channels for sequencing.
func newStallTestServer(t *testing.T, firstChunks int) (chan struct{}, chan struct{}, *httptest.Server) {
	t.Helper()
	release := make(chan struct{})
	gotReq := make(chan struct{}, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for i := 0; i < firstChunks; i++ {
			fmt.Fprintf(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"},\"finish_reason\":\"\"}]}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
		gotReq <- struct{}{}
		<-release
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(ts.Close)
	return release, gotReq, ts
}

// isolateSessionDir points the session layer's global SessionDir at a fresh
// temp directory for the duration of the test (mirror of the executor
// helper): the turn commit persists a .meta.json sidecar into the shared
// sessions dir.
func isolateSessionDir(t *testing.T) {
	t.Helper()
	oldDir := session.SessionDir
	session.SessionDir = func() (string, error) { return t.TempDir(), nil }
	t.Cleanup(func() { session.SessionDir = oldDir })
}

// drainEvents consumes o.eventCh until stop is closed, mirroring a live TUI
// forwarder so the intentionally-blocking terminal sends are delivered.
func drainEvents(o *BaseOrchestrator, stop chan struct{}) {
	go func() {
		for {
			select {
			case <-o.eventCh:
			case <-stop:
				return
			}
		}
	}()
}

// TestBaseOrchestrator_ExecuteRecoversFromAddUserMessageFailure pins the
// terminal-error hang fix on the Execute entry path: when the initial
// AddUserMessage fails (here: the history path sits under a regular file, so
// the atomic persist cannot create its directory), Execute must undo
// isRunning and emit a terminal status before returning. Before the fix the
// early return left isRunning stuck true — every later Execute failed with
// "orchestrator is already running" and every Submit queued into a run that
// would never start.
func TestBaseOrchestrator_ExecuteRecoversFromAddUserMessageFailure(t *testing.T) {
	isolateSessionDir(t)
	tmpDir := t.TempDir()

	// A regular file where the history's parent directory should be:
	// writeAtomic's MkdirAll must fail, making AddUserMessage fail.
	blocker := filepath.Join(tmpDir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	historyPath := filepath.Join(blocker, "sub", "history.json")

	c := client.NewClient(client.Config{BaseURL: "http://localhost:0"})
	sess := session.New(c, historyPath, nil, "", false)
	o := NewBaseOrchestrator("test", sess, nil, 1)

	// No drain goroutine here: the buffered channel holds the two terminal
	// events, and the assertion loop below is the sole consumer.

	if _, err := o.Execute("hello"); err == nil {
		t.Fatal("Execute() error = nil, want the persist failure")
	} else if strings.Contains(err.Error(), "already running") {
		t.Fatalf("Execute failed for the wrong reason (stuck isRunning): %v", err)
	}

	// The orchestrator must be re-usable: a second Execute reports the same
	// persist failure, not "already running".
	if _, err := o.Execute("again"); err == nil || strings.Contains(err.Error(), "already running") {
		t.Fatalf("second Execute() error = %v, want the persist failure again", err)
	}

	// A terminal status must have been delivered for the failures — the TUI
	// state machine resolves on it. Two Execute calls → two terminal errors.
	deadline := time.After(5 * time.Second)
	terminal := 0
	for terminal < 2 {
		select {
		case ev := <-o.eventCh:
			if se, ok := ev.(common.StatusEvent); ok && se.Status == "error" {
				terminal++
			}
		case <-deadline:
			t.Fatalf("no terminal error status was delivered (got %d of 2)", terminal)
		}
	}
}

// TestBaseOrchestrator_ExecutePreservesQueuedMessages pins the queued-message
// contract of Execute: a message submitted while the run is executing is
// stored in pendingMsgs, and after the run ends it must still be queued —
// NOT silently dropped. Before the fix Execute's terminal defer nil'ed
// pendingMsgs, discarding messages the TUI had already reported as queued
// (a child tab accepts input while its agent runs).
func TestBaseOrchestrator_ExecutePreservesQueuedMessages(t *testing.T) {
	isolateSessionDir(t)
	tmpDir := t.TempDir()
	release, gotReq, ts := newStallTestServer(t, 1)

	historyPath := filepath.Join(tmpDir, "session.json")
	initial := []client.ChatMessage{{Role: "user", Content: client.TextContent("goal")}}
	c := client.NewClient(client.Config{BaseURL: ts.URL})
	sess := session.New(c, historyPath, initial, "", false)
	o := NewBaseOrchestrator("test", sess, nil, 1)

	stop := make(chan struct{})
	drainEvents(o, stop)
	defer close(stop)

	done := make(chan error, 1)
	go func() {
		_, err := o.Execute("")
		done <- err
	}()

	// Wait until the run is mid-stream (the request reached the server, so
	// isRunning is set), then queue a message into the running agent.
	<-gotReq
	if err := o.Submit("queued while running", nil); err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Execute did not return")
	}

	queued := o.QueuedMessages()
	if len(queued) != 1 || queued[0] != "queued while running" {
		t.Fatalf("queued messages after Execute = %#v, want [queued while running]", queued)
	}
}

// TestBaseOrchestrator_SubmitDoesNotBlockWhenConsumerStalls pins the
// non-blocking behavior of Submit's MessageQueuedEvent send: with the event
// buffer already full (a stalled consumer), Submit must return immediately
// instead of wedging the caller (the TUI input path) on a cosmetic event.
// Before the fix the send was blocking.
func TestBaseOrchestrator_SubmitDoesNotBlockWhenConsumerStalls(t *testing.T) {
	isolateSessionDir(t)
	tmpDir := t.TempDir()

	release, gotReq, ts := newStallTestServer(t, 1)

	historyPath := filepath.Join(tmpDir, "session.json")
	initial := []client.ChatMessage{{Role: "user", Content: client.TextContent("goal")}}
	c := client.NewClient(client.Config{BaseURL: ts.URL})
	sess := session.New(c, historyPath, initial, "", false)
	o := NewBaseOrchestrator("test", sess, nil, 1)

	done := make(chan error, 1)
	go func() {
		_, err := o.Execute("")
		done <- err
	}()
	<-gotReq

	// Fill the event buffer with no consumer running, so Submit's
	// queued-message send runs against a full channel.
	filled := false
	for !filled {
		select {
		case o.eventCh <- common.ContentEvent{ID: o.id}:
		default:
			filled = true
		}
	}

	submitted := make(chan error, 1)
	go func() { submitted <- o.Submit("stalled queue", nil) }()
	select {
	case err := <-submitted:
		if err != nil {
			t.Fatalf("Submit() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Submit blocked on a full eventCh (blocking MessageQueuedEvent send)")
	}

	// Start consuming only NOW: while no consumer runs the buffer stays
	// full (the point of the test), and the intentionally-blocking terminal
	// sends need a consumer to complete once the run resumes.
	stop := make(chan struct{})
	drainEvents(o, stop)
	defer close(stop)
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Execute did not return after the consumer resumed")
	}

	// The queued message survived the stalled-consumer run (the
	// preserve-queued-messages contract).
	if queued := o.QueuedMessages(); len(queued) != 1 || queued[0] != "stalled queue" {
		t.Fatalf("queued messages after Execute = %#v, want [stalled queue]", queued)
	}
}
