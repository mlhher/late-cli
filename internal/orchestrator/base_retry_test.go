package orchestrator

// Regression tests for the orchestrator-side stream accumulator: the shared,
// per-turn accumulator (BaseOrchestrator.acc, guarded by o.mu) must be reset
// when a stream retry starts. The executor's RunLoop builds a fresh LOCAL
// accumulator per attempt, but the orchestrator appends every onStreamChunk
// delta into o.acc and builds its ContentEvents from it; without a reset on
// the onRetry path, the next ContentEvent carries
// "<failed attempt partial><retry output>" and the TUI renders the spliced
// text ("parok...").
//
// The tests drive the real onRetry callbacks end-to-end:
//
//	Execute / run -> executor.RunLoop -> session.StartStream ->
//	client.ChatCompletionStream -> in-process httptest SSE server
//
// The first attempt is a valid 200 whose body is truncated mid-response
// (hijacked connection, declared Content-Length larger than the bytes sent,
// one complete SSE data line, then FIN): net/http surfaces the short body as
// io.ErrUnexpectedEOF, which the retry tier classifies as retryable. The
// retry attempt serves a complete stream. Assertions only pin counts and
// content — never exact timings; the retry budget is kept small via
// common.MaxStreamRetriesKey so the jittered backoff (base 500ms) stays well
// under the deadline in every interleaving.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"late/internal/client"
	"late/internal/common"
	"late/internal/session"
)

// retryChunkPartial is the delta streamed by the failed attempt before the
// body dies: the partial content the orchestrator must not splice into the
// retry's output.
const retryChunkPartial = `{"choices":[{"index":0,"delta":{"content":"par"}}]}`

// newAccumulatorRetryServer serves a truncated mid-stream 200 on the first
// POST to */chat/completions and a complete SSE stream carrying "ok" on every
// later POST. Discovery probes (GET /props, GET /v1/models) answer 404.
func newAccumulatorRetryServer(t *testing.T) *httptest.Server {
	t.Helper()
	var failureServed atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if !failureServed.CompareAndSwap(false, true) {
			// Every attempt after the first: a complete stream with only "ok".
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: "+`{"id":"1","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n")
			fmt.Fprint(w, "data: [DONE]\n")
			return
		}
		// First attempt: a valid 200 whose body is truncated mid-response.
		// Hijack the connection, declare a Content-Length larger than the
		// bytes actually sent, write one valid partial SSE data line, then
		// FIN without the remaining body. The client sees EOF before the
		// declared Content-Length and the read surfaces io.ErrUnexpectedEOF,
		// which the executor's retry tier treats as retryable (same fixture
		// as the executor's own mid-body disconnect test).
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("server ResponseWriter does not support Hijack")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"message":"hijack unsupported","type":"server_error"}}`)
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack failed: %v", err)
			return
		}
		defer conn.Close()

		head := "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: 1000\r\n\r\n"
		body := "data: " + retryChunkPartial + "\n\n"
		if _, err := conn.Write([]byte(head)); err != nil {
			t.Errorf("writing truncated response head: %v", err)
			return
		}
		if _, err := conn.Write([]byte(body)); err != nil {
			t.Errorf("writing truncated response body: %v", err)
			return
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

// newAccumulatorRetryOrchestrator builds a BaseOrchestrator over an in-memory
// session bound to the test server, with a ctx carrying a small retry budget.
func newAccumulatorRetryOrchestrator(t *testing.T, baseURL string) *BaseOrchestrator {
	t.Helper()
	c := client.NewClient(client.Config{BaseURL: baseURL})
	sess := session.New(c, "", nil, "", false)
	o := NewBaseOrchestrator("test-orch", sess, nil, 5)
	ctx, cancel := context.WithTimeout(
		context.WithValue(context.Background(), common.MaxStreamRetriesKey, 3),
		15*time.Second,
	)
	t.Cleanup(cancel)
	o.SetContext(ctx)
	return o
}

// collectStreamedContents drains the orchestrator's buffered event channel
// without blocking and returns the streaming (non-Completed) ContentEvent
// contents in emission order plus the RetryEvent and RecoveryEvent counts.
// Safe to call only after the producing goroutine has finished (Execute) or
// after the collector below saw a terminal event (run).
func collectStreamedContents(o *BaseOrchestrator) (contents []string, retries, recoveries int) {
	for {
		select {
		case ev := <-o.Events():
			switch e := ev.(type) {
			case common.ContentEvent:
				if !e.Completed {
					contents = append(contents, e.Content)
				}
			case common.RetryEvent:
				retries++
			case common.RecoveryEvent:
				recoveries++
			}
		default:
			return contents, retries, recoveries
		}
	}
}

// TestOnRetryResetsAccumulator covers the Execute path: a stream that dies
// mid-response after emitting "par", then succeeds with "ok" on the retry.
// The failed attempt's partial is streamed live (first ContentEvent "par"),
// but the post-retry ContentEvent must carry only the retry's output ("ok"),
// never the spliced "parok" — the onRetry callback resets the shared
// accumulator before the retry's deltas are appended.
func TestOnRetryResetsAccumulator(t *testing.T) {
	ts := newAccumulatorRetryServer(t)
	o := newAccumulatorRetryOrchestrator(t, ts.URL)

	res, err := o.Execute("hello")
	if err != nil {
		t.Fatalf("Execute returned error after a retryable mid-stream disconnect: %v", err)
	}
	if res != "ok" {
		t.Errorf("Execute result = %q, want %q", res, "ok")
	}

	contents, retries, recoveries := collectStreamedContents(o)
	if retries != 1 {
		t.Fatalf("got %d RetryEvents, want exactly 1", retries)
	}
	// The retry produced a response: the dedicated recovery event must be
	// emitted exactly once, so the UI can toast immediately instead of
	// guessing on the next turn's thinking event.
	if recoveries != 1 {
		t.Errorf("got %d RecoveryEvents, want exactly 1 (retry actually succeeded)", recoveries)
	}
	if len(contents) != 2 {
		t.Fatalf("got %d streaming ContentEvents (%q), want exactly 2: the failed attempt's partial and the retry's output", len(contents), contents)
	}
	if contents[0] != "par" {
		t.Errorf("first streaming ContentEvent = %q, want the failed attempt's partial %q", contents[0], "par")
	}
	if contents[1] != "ok" {
		t.Errorf("post-retry streaming ContentEvent = %q, want %q — the retry starts a fresh accumulator, not the spliced %q", contents[1], "ok", "parok")
	}
}

// TestOnRetryResetsAccumulatorRunLoop covers the run path (Submit's
// background loop), whose onRetry callback is a separate closure from the
// Execute one and must reset the shared accumulator the same way.
func TestOnRetryResetsAccumulatorRunLoop(t *testing.T) {
	ts := newAccumulatorRetryServer(t)
	o := newAccumulatorRetryOrchestrator(t, ts.URL)

	var mu sync.Mutex
	var contents []string
	retries := 0
	recoveries := 0
	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		for {
			ev := <-o.Events()
			switch e := ev.(type) {
			case common.ContentEvent:
				if !e.Completed {
					mu.Lock()
					contents = append(contents, e.Content)
					mu.Unlock()
				}
			case common.RetryEvent:
				mu.Lock()
				retries++
				mu.Unlock()
			case common.RecoveryEvent:
				mu.Lock()
				recoveries++
				mu.Unlock()
			case common.StatusEvent:
				// run() always ends the loop with a terminal status event;
				// everything it emits before that has already been read.
				if e.Status == "idle" || e.Status == "closed" || e.Status == "error" {
					return
				}
			}
		}
	}()

	if err := o.Submit("hello", nil); err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}
	select {
	case <-collectorDone:
	case <-time.After(30 * time.Second):
		t.Fatal("run loop did not reach a terminal status within 30s")
	}

	mu.Lock()
	defer mu.Unlock()
	if retries != 1 {
		t.Fatalf("got %d RetryEvents, want exactly 1", retries)
	}
	// The run path's separate onRecover closure must emit the dedicated
	// recovery event exactly once when the retried attempt succeeds.
	if recoveries != 1 {
		t.Errorf("got %d RecoveryEvents, want exactly 1 (retry actually succeeded)", recoveries)
	}
	if len(contents) != 2 {
		t.Fatalf("got %d streaming ContentEvents (%q), want exactly 2: the failed attempt's partial and the retry's output", len(contents), contents)
	}
	if contents[0] != "par" {
		t.Errorf("first streaming ContentEvent = %q, want the failed attempt's partial %q", contents[0], "par")
	}
	if contents[1] != "ok" {
		t.Errorf("post-retry streaming ContentEvent = %q, want %q — the retry starts a fresh accumulator, not the spliced %q", contents[1], "ok", "parok")
	}
}

// --- Recovery must never fire on retry exhaustion ---
//
// There was no orchestrator-level exhaustion fixture, so this small one pins
// the exhaustion side of the recovery contract end-to-end: with the bad-body
// budget drained, the run terminates with a terminal error StatusEvent and
// ZERO RecoveryEvents — exhaustion is not recovery, because no attempt ever
// produced a response.

// TestBadBodyBudgetExhaustedEmitsNoRecovery drives the Execute path against a
// server that rejects every request with a transient-looking HTTP 400 (the
// bad-body tier) and a ctx with a tiny bad-body budget. The budget drains,
// RunLoop fails, and the orchestrator emits the terminal error StatusEvent —
// without ever emitting a RecoveryEvent.
func TestBadBodyBudgetExhaustedEmitsNoRecovery(t *testing.T) {
	var posts atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		posts.Add(1)
		// Every attempt: the transient-looking 400 body-parse rejection the
		// bad-body retry tier exists for.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"The request is invalid: read body failed.","type":"server_error"}}`)
	}))
	t.Cleanup(ts.Close)

	c := client.NewClient(client.Config{BaseURL: ts.URL})
	sess := session.New(c, "", nil, "", false)
	o := NewBaseOrchestrator("test-orch-exhaust", sess, nil, 5)
	ctx, cancel := context.WithTimeout(
		context.WithValue(context.Background(), common.MaxBadBodyRetriesKey, 1),
		15*time.Second,
	)
	t.Cleanup(cancel)
	o.SetContext(ctx)

	res, err := o.Execute("hello")
	if err == nil {
		t.Fatalf("Execute returned %q with nil error, want the bad-body budget exhaustion error", res)
	}
	var statusErr *client.StatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("Execute error = %v, want it to wrap *client.StatusError with 400", err)
	}

	// The budget drained: initial attempt + exactly one retry.
	if got := posts.Load(); got != 2 {
		t.Errorf("server got %d POSTs, want 2 (initial attempt + one bad-body retry)", got)
	}

	// Drain the buffered events: zero RecoveryEvents, and the terminal error
	// status must have arrived. All events are emitted synchronously before
	// Execute returns, so a non-blocking drain cannot race the producer.
	sawErrorStatus := false
	recoveries := 0
	for {
		select {
		case ev := <-o.Events():
			switch e := ev.(type) {
			case common.RecoveryEvent:
				recoveries++
			case common.StatusEvent:
				if e.Status == "error" {
					sawErrorStatus = true
				}
			}
		default:
			if recoveries != 0 {
				t.Errorf("got %d RecoveryEvents, want 0 (exhaustion is not recovery)", recoveries)
			}
			if !sawErrorStatus {
				t.Error("no terminal error StatusEvent, want one after the bad-body budget drained")
			}
			return
		}
	}
}
