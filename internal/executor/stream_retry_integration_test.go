package executor

// Integration tests for the RunLoop stream retry loop (executor.go), driven
// end-to-end against an in-process httptest SSE server:
//
//	RunLoop -> session.StartStream -> client.ChatCompletionStream -> httptest
//
// The retry budget is kept small via common.MaxStreamRetriesKey so the
// jittered backoff (full jitter over [0, base*2^(attempt-1)], base 500ms)
// stays well under the test deadline in every interleaving. Assertions only
// pin counts and ordering — never exact timings.

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// SSE chunk payloads used by the success fixture (same shape the client
// tests use): two content deltas, then a terminal chunk with
// finish_reason "stop", then the [DONE] sentinel.
const (
	retryChunkHello = `{"id":"c1","choices":[{"index":0,"delta":{"content":"Hello"}}]}`
	retryChunkWorld = `{"id":"c1","choices":[{"index":0,"delta":{"content":" world"}}]}`
	retryChunkStop  = `{"id":"c1","choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}]}`
)

// successSSEBody renders the complete, well-formed SSE stream served on the
// successful attempt of the retry scenarios.
func successSSEBody() string {
	var b strings.Builder
	for _, payload := range []string{retryChunkHello, retryChunkWorld, retryChunkStop} {
		fmt.Fprintf(&b, "data: %s\n", payload)
	}
	b.WriteString("data: [DONE]\n")
	return b.String()
}

// errorJSONBody is the JSON error payload served alongside non-200 status
// codes; the client decodes it into client.StatusError.Body.
func errorJSONBody(message string) string {
	return fmt.Sprintf(`{"error":{"message":%q,"type":"server_error"}}`, message)
}

// retryServer wraps an httptest.Server. Only POSTs to */chat/completions are
// routed to the test handler and counted; the client's DiscoverBackend probes
// (GET /props, GET /v1/models) answer 404 immediately and stay uncounted.
type retryServer struct {
	server *httptest.Server
	posts  atomic.Int64
}

func newRetryServer(t *testing.T, handlePost func(w http.ResponseWriter, r *http.Request)) *retryServer {
	t.Helper()
	rs := &retryServer{}
	rs.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		rs.posts.Add(1)
		handlePost(w, r)
	}))
	t.Cleanup(rs.server.Close)
	return rs
}

func (rs *retryServer) postCount() int {
	return int(rs.posts.Load())
}

// newRetryTestSession builds an in-memory session bound to the test server.
// An empty HistoryPath keeps every write in memory (saveAndNotify skips
// persistence), so the seeded user message is the only history entry until a
// turn commits its assistant reply.
func newRetryTestSession(t *testing.T, baseURL string) *session.Session {
	t.Helper()
	c := client.NewClient(client.Config{BaseURL: baseURL})
	return session.New(c, "", []client.ChatMessage{
		{Role: "user", Content: client.TextContent("hello")},
	}, "", false)
}

// retryCollector returns an onRetry callback that records RetryEvents plus a
// snapshot accessor. RunLoop invokes the callback from its own goroutine and
// the test reads the events after RunLoop returns; the mutex keeps the race
// detector happy regardless of interleaving.
func retryCollector(t *testing.T) (onRetry func(common.RetryEvent), events func() []common.RetryEvent) {
	t.Helper()
	var mu sync.Mutex
	var recorded []common.RetryEvent
	return func(ev common.RetryEvent) {
			mu.Lock()
			defer mu.Unlock()
			recorded = append(recorded, ev)
		}, func() []common.RetryEvent {
			mu.Lock()
			defer mu.Unlock()
			return append([]common.RetryEvent(nil), recorded...)
		}
}

// recoveryCollector returns an onRecover callback that counts invocations plus
// a snapshot accessor, mirroring retryCollector's goroutine-safety notes:
// RunLoop invokes the callback from its own goroutine and the test reads the
// count after RunLoop returns; the mutex keeps the race detector happy
// regardless of interleaving.
func recoveryCollector(t *testing.T) (onRecover func(), count func() int) {
	t.Helper()
	var mu sync.Mutex
	var calls int
	return func() {
			mu.Lock()
			defer mu.Unlock()
			calls++
		}, func() int {
			mu.Lock()
			defer mu.Unlock()
			return calls
		}
}

// runLoopCtx returns a ctx carrying a small retry budget and a generous
// deadline so a bug can never hang a test until the global -timeout.
func runLoopCtx(budget int, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(
		context.WithValue(context.Background(), common.MaxStreamRetriesKey, budget),
		timeout,
	)
}

// serveStatus writes a non-200 JSON error response, which the client turns
// into a *client.StatusError.
func serveStatus(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprint(w, errorJSONBody(message))
}

// serveStatusWithHeaders is serveStatus with extra response headers (e.g.
// Retry-After) set before the status is written; the client captures them on
// the resulting *client.StatusError.
func serveStatusWithHeaders(w http.ResponseWriter, code int, message string, headers map[string]string) {
	for k, v := range headers {
		w.Header().Set(k, v)
	}
	serveStatus(w, code, message)
}

// serveSSE writes the success fixture as a complete SSE stream.
func serveSSE(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, successSSEBody())
}

func TestRunLoopRetriesThenSucceeds(t *testing.T) {
	var failureServed atomic.Bool
	rs := newRetryServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !failureServed.CompareAndSwap(false, true) {
			// Every attempt after the first: complete SSE stream.
			serveSSE(w)
			return
		}
		// First attempt: transient 500 with a JSON error body.
		serveStatus(w, http.StatusInternalServerError, "upstream exploded")
	})

	sess := newRetryTestSession(t, rs.server.URL)
	onRetry, retryEvents := retryCollector(t)
	onRecover, recoveries := recoveryCollector(t)

	ctx, cancel := runLoopCtx(3, 15*time.Second)
	defer cancel()

	start := time.Now()
	res, err := RunLoop(ctx, sess, 1, nil, nil, nil, nil, onRetry, onRecover, nil)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("RunLoop returned error after a retryable 500: %v", err)
	}
	if res != "Hello world" {
		t.Errorf("RunLoop result = %q, want %q", res, "Hello world")
	}
	// One jittered backoff, capped at the 500ms base delay. Generous bound.
	if elapsed > 5*time.Second {
		t.Errorf("RunLoop took %v with a single retry, want well under that", elapsed)
	}

	events := retryEvents()
	if len(events) != 1 {
		t.Fatalf("got %d RetryEvents, want exactly 1: %+v", len(events), events)
	}
	ev := events[0]
	if ev.Attempt != 1 {
		t.Errorf("RetryEvent.Attempt = %d, want 1", ev.Attempt)
	}
	if ev.MaxAttempts != 3 {
		t.Errorf("RetryEvent.MaxAttempts = %d, want 3 (ctx budget)", ev.MaxAttempts)
	}
	if ev.Delay <= 0 {
		t.Errorf("RetryEvent.Delay = %v, want > 0", ev.Delay)
	}
	if ev.Err == nil {
		t.Fatal("RetryEvent.Err is nil, want the underlying stream error")
	}
	var statusErr *client.StatusError
	if !errors.As(ev.Err, &statusErr) {
		t.Fatalf("RetryEvent.Err = %v (%T), want it to wrap *client.StatusError", ev.Err, ev.Err)
	}
	if statusErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("RetryEvent.Err status = %d, want 500", statusErr.StatusCode)
	}

	// The retried attempt actually produced a response: recovery fires
	// exactly once for this turn — the turn-start callback ran before the
	// retries, so this is the only signal that the attempt recovered.
	if got := recoveries(); got != 1 {
		t.Errorf("onRecover fired %d times, want exactly 1 (once per retried turn)", got)
	}

	if got := rs.postCount(); got != 2 {
		t.Errorf("server got %d POSTs, want 2 (failed attempt + successful retry)", got)
	}

	// The failed attempt must not have committed anything; only the
	// successful turn appends its assistant message to the seeded history.
	if len(sess.History) != 2 {
		t.Fatalf("history length = %d, want 2 (seeded user msg + committed assistant msg)", len(sess.History))
	}
	last := sess.History[len(sess.History)-1]
	if last.Role != "assistant" {
		t.Errorf("last history role = %q, want assistant", last.Role)
	}
	if last.Content.String() != "Hello world" {
		t.Errorf("last history content = %q, want %q", last.Content.String(), "Hello world")
	}
}

// TestRunLoopHonorsRetryAfter proves the executor honors a server-requested
// Retry-After end-to-end: a 429 carrying Retry-After: 2 must make RunLoop
// wait at least the requested 2s before retrying — the RetryEvent's Delay is
// the effective (combined) delay and the wall clock confirms the wait really
// happened. Bounded assertions only: no upper timing pin beyond sanity.
func TestRunLoopHonorsRetryAfter(t *testing.T) {
	var failureServed atomic.Bool
	rs := newRetryServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !failureServed.CompareAndSwap(false, true) {
			// Second attempt: complete SSE stream.
			serveSSE(w)
			return
		}
		// First attempt: transient 429 with a server-requested 2s wait.
		serveStatusWithHeaders(w, http.StatusTooManyRequests, "slow down", map[string]string{
			"Retry-After": "2",
		})
	})

	sess := newRetryTestSession(t, rs.server.URL)
	onRetry, retryEvents := retryCollector(t)
	onRecover, recoveries := recoveryCollector(t)

	ctx, cancel := runLoopCtx(3, 15*time.Second)
	defer cancel()

	start := time.Now()
	res, err := RunLoop(ctx, sess, 1, nil, nil, nil, nil, onRetry, onRecover, nil)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("RunLoop returned error after a retryable 429 with Retry-After: %v", err)
	}
	if res != "Hello world" {
		t.Errorf("RunLoop result = %q, want %q", res, "Hello world")
	}

	events := retryEvents()
	if len(events) != 1 {
		t.Fatalf("got %d RetryEvents, want exactly 1: %+v", len(events), events)
	}
	ev := events[0]
	if ev.Attempt != 1 {
		t.Errorf("RetryEvent.Attempt = %d, want 1", ev.Attempt)
	}
	if ev.MaxAttempts != 3 {
		t.Errorf("RetryEvent.MaxAttempts = %d, want 3 (ctx budget)", ev.MaxAttempts)
	}
	if ev.Err == nil {
		t.Fatal("RetryEvent.Err is nil, want the underlying stream error")
	}
	var statusErr *client.StatusError
	if !errors.As(ev.Err, &statusErr) {
		t.Fatalf("RetryEvent.Err = %v (%T), want it to wrap *client.StatusError", ev.Err, ev.Err)
	}
	if statusErr.StatusCode != http.StatusTooManyRequests {
		t.Errorf("RetryEvent.Err status = %d, want 429", statusErr.StatusCode)
	}
	if statusErr.RetryAfter != 2*time.Second {
		t.Errorf("StatusError.RetryAfter = %v, want 2s (server-requested delay)", statusErr.RetryAfter)
	}

	// The retried attempt actually produced a response: recovery fires
	// exactly once for this turn, independent of the retry tier.
	if got := recoveries(); got != 1 {
		t.Errorf("onRecover fired %d times, want exactly 1 (once per retried turn)", got)
	}

	// CORE assertion: the effective delay reported (and slept) is never
	// shorter than the server-requested 2s — the executor must not retry
	// before the requested delay even when the jittered local backoff
	// (max 500ms on attempt 1) is smaller.
	if ev.Delay < 2*time.Second {
		t.Errorf("RetryEvent.Delay = %v, want >= 2s (the server-requested Retry-After)", ev.Delay)
	}
	if ev.Delay > retryAfterCeiling {
		t.Errorf("RetryEvent.Delay = %v, want <= %v (the ceiling)", ev.Delay, retryAfterCeiling)
	}

	// The wall clock must reflect the honored wait too: the run cannot
	// finish before the requested delay elapsed. time.NewTimer fires no
	// earlier than its duration, so this is deterministic, not a flake risk.
	if elapsed < 2*time.Second {
		t.Errorf("RunLoop finished in %v, want >= 2s (Retry-After honored)", elapsed)
	}
	// Bounded: a single ~2s wait plus network overhead; 10s is generous.
	if elapsed > 10*time.Second {
		t.Errorf("RunLoop took %v with a single ~2s wait, want well under that", elapsed)
	}

	if got := rs.postCount(); got != 2 {
		t.Errorf("server got %d POSTs, want 2 (429 attempt + successful retry)", got)
	}

	// The failed attempt must not have committed anything; only the
	// successful turn appends its assistant message to the seeded history.
	if len(sess.History) != 2 {
		t.Fatalf("history length = %d, want 2 (seeded user msg + committed assistant msg)", len(sess.History))
	}
	last := sess.History[len(sess.History)-1]
	if last.Role != "assistant" {
		t.Errorf("last history role = %q, want assistant", last.Role)
	}
	if last.Content.String() != "Hello world" {
		t.Errorf("last history content = %q, want %q", last.Content.String(), "Hello world")
	}
}

func TestRunLoopStopsAfterRetryBudgetExhausted(t *testing.T) {
	rs := newRetryServer(t, func(w http.ResponseWriter, r *http.Request) {
		serveStatus(w, http.StatusInternalServerError, "still down")
	})

	sess := newRetryTestSession(t, rs.server.URL)
	onRetry, retryEvents := retryCollector(t)
	onRecover, recoveries := recoveryCollector(t)

	// Budget 2 => initial attempt + 2 retries, then terminal failure.
	ctx, cancel := runLoopCtx(2, 15*time.Second)
	defer cancel()

	start := time.Now()
	_, err := RunLoop(ctx, sess, 1, nil, nil, nil, nil, onRetry, onRecover, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("RunLoop returned nil error, want failure after retry budget exhausted")
	}
	var statusErr *client.StatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("RunLoop error = %v, want it to wrap *client.StatusError with 500", err)
	}
	// Worst-case jitter for two backoffs is 500ms + 1s; 10s is a sanity bound.
	if elapsed > 10*time.Second {
		t.Errorf("RunLoop took %v to exhaust a budget of 2, want well under that", elapsed)
	}

	events := retryEvents()
	if len(events) != 2 {
		t.Fatalf("got %d RetryEvents, want exactly 2: %+v", len(events), events)
	}
	for i, ev := range events {
		if want := i + 1; ev.Attempt != want {
			t.Errorf("events[%d].Attempt = %d, want %d", i, ev.Attempt, want)
		}
		if ev.MaxAttempts != 2 {
			t.Errorf("events[%d].MaxAttempts = %d, want 2", i, ev.MaxAttempts)
		}
		if ev.Delay <= 0 {
			t.Errorf("events[%d].Delay = %v, want > 0", i, ev.Delay)
		}
		if ev.Err == nil {
			t.Errorf("events[%d].Err is nil, want the underlying stream error", i)
		}
	}

	if got := rs.postCount(); got != 3 {
		t.Errorf("server got %d POSTs, want 3 (initial attempt + 2 retries)", got)
	}

	// Failed attempts commit nothing to history.
	if len(sess.History) != 1 {
		t.Errorf("history length = %d, want 1 (only the seeded user msg)", len(sess.History))
	}

	// Exhaustion is not recovery: no attempt ever produced a response.
	if got := recoveries(); got != 0 {
		t.Errorf("onRecover fired %d times, want 0 (retry exhaustion is not recovery)", got)
	}
}

func TestRunLoopDoesNotRetryNonRetryable(t *testing.T) {
	rs := newRetryServer(t, func(w http.ResponseWriter, r *http.Request) {
		serveStatus(w, http.StatusUnauthorized, "invalid api key")
	})

	sess := newRetryTestSession(t, rs.server.URL)
	onRetry, retryEvents := retryCollector(t)
	onRecover, recoveries := recoveryCollector(t)

	// A generous budget that must never be touched by a 401.
	ctx, cancel := runLoopCtx(5, 15*time.Second)
	defer cancel()

	start := time.Now()
	_, err := RunLoop(ctx, sess, 1, nil, nil, nil, nil, onRetry, onRecover, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("RunLoop returned nil error, want the 401 to fail the run")
	}
	var statusErr *client.StatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("RunLoop error = %v, want it to wrap *client.StatusError with 401", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("non-retryable 401 took %v to fail, want a fast failure", elapsed)
	}

	if events := retryEvents(); len(events) != 0 {
		t.Errorf("got %d RetryEvents, want 0 for a non-retryable error: %+v", len(events), events)
	}
	// No retries, no recovery: the flag must never fire on a clean failure.
	if got := recoveries(); got != 0 {
		t.Errorf("onRecover fired %d times, want 0 (no retries happened)", got)
	}
	if got := rs.postCount(); got != 1 {
		t.Errorf("server got %d POSTs, want exactly 1 (no retry after 401)", got)
	}
	if len(sess.History) != 1 {
		t.Errorf("history length = %d, want 1 (nothing committed)", len(sess.History))
	}
}

func TestRunLoopDoesNotRetry413(t *testing.T) {
	// A 413 (payload too large) must fail the run on the FIRST attempt with
	// zero retries on every tier: the provider rejected the request BODY, so
	// resending the identical body can never succeed. The client classifies
	// it as *client.PayloadTooLargeError (sentinel ErrPayloadTooLarge) with
	// actionable recovery guidance in the error text.
	rs := newRetryServer(t, func(w http.ResponseWriter, r *http.Request) {
		serveStatus(w, http.StatusRequestEntityTooLarge, "Request body too large")
	})

	sess := newRetryTestSession(t, rs.server.URL)
	onRetry, retryEvents := retryCollector(t)
	onRecover, recoveries := recoveryCollector(t)

	// A generous budget that must never be touched by a 413.
	ctx, cancel := runLoopCtx(5, 15*time.Second)
	defer cancel()

	start := time.Now()
	_, err := RunLoop(ctx, sess, 1, nil, nil, nil, nil, onRetry, onRecover, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("RunLoop returned nil error, want the 413 to fail the run")
	}
	if !errors.Is(err, client.ErrPayloadTooLarge) {
		t.Fatalf("RunLoop error = %v, want it to carry the ErrPayloadTooLarge sentinel", err)
	}
	var statusErr *client.StatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("RunLoop error = %v, want it to wrap *client.StatusError with 413", err)
	}
	if !strings.Contains(err.Error(), client.PayloadTooLargeGuidance) {
		t.Errorf("RunLoop error = %q, want it to carry the recovery guidance", err.Error())
	}
	if elapsed > 2*time.Second {
		t.Errorf("non-retryable 413 took %v to fail, want a fast failure", elapsed)
	}

	if events := retryEvents(); len(events) != 0 {
		t.Errorf("got %d RetryEvents, want 0 for a non-retryable 413: %+v", len(events), events)
	}
	// No retries, no recovery: the flag must never fire on a clean failure.
	if got := recoveries(); got != 0 {
		t.Errorf("onRecover fired %d times, want 0 (no retries happened)", got)
	}
	if got := rs.postCount(); got != 1 {
		t.Errorf("server got %d POSTs, want exactly 1 (no retry after 413)", got)
	}
	if len(sess.History) != 1 {
		t.Errorf("history length = %d, want 1 (nothing committed)", len(sess.History))
	}
}

func TestRunLoopCancelDuringBackoffStops(t *testing.T) {
	rs := newRetryServer(t, func(w http.ResponseWriter, r *http.Request) {
		serveStatus(w, http.StatusInternalServerError, "down while user waits")
	})

	sess := newRetryTestSession(t, rs.server.URL)
	collect, retryEvents := retryCollector(t)
	onRecover, recoveries := recoveryCollector(t)

	ctx, cancel := context.WithCancel(
		context.WithValue(context.Background(), common.MaxStreamRetriesKey, 5),
	)
	defer cancel()

	// Mirror the TUI stop path: cancel as soon as the first RetryEvent
	// arrives, so RunLoop aborts before completing the backoff sleep.
	var once sync.Once
	onRetry := func(ev common.RetryEvent) {
		collect(ev)
		once.Do(cancel)
	}

	start := time.Now()
	_, err := RunLoop(ctx, sess, 1, nil, nil, nil, nil, onRetry, onRecover, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("RunLoop returned nil error, want the stream error after cancel during backoff")
	}
	// The cancel lands inside the first (<= 500ms) backoff; anything near the
	// bound means the loop kept backing off instead of stopping.
	if elapsed > 2*time.Second {
		t.Errorf("RunLoop took %v to stop after cancel, want a prompt stop", elapsed)
	}

	if events := retryEvents(); len(events) > 1 {
		t.Errorf("got %d RetryEvents, want at most 1 (no further retry after cancel): %+v", len(events), events)
	}
	// Initial attempt + at most one attempt that raced the cancel window.
	if got := rs.postCount(); got > 2 {
		t.Errorf("server got %d POSTs, want at most 2 after cancel", got)
	}
	if len(sess.History) != 1 {
		t.Errorf("history length = %d, want 1 (a cancelled run commits nothing)", len(sess.History))
	}
	// The cancel landed during backoff, so no attempt ever succeeded: no
	// recovery signal may fire on a cancelled run.
	if got := recoveries(); got != 0 {
		t.Errorf("onRecover fired %d times, want 0 (a cancelled run never recovers)", got)
	}
}

func TestRunLoopMidBodyDisconnectRetries(t *testing.T) {
	var failureServed atomic.Bool
	rs := newRetryServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !failureServed.CompareAndSwap(false, true) {
			// Second attempt: complete SSE stream.
			serveSSE(w)
			return
		}
		// First attempt: a valid 200 whose body is truncated mid-response.
		// Hijack the connection, declare a Content-Length larger than the
		// bytes actually sent, write one valid partial SSE data line, then
		// FIN without the remaining body. net/http surfaces the short body
		// as io.ErrUnexpectedEOF on read, which the client wraps into
		// "stream interrupted: ..." and isRetryableStreamError treats as
		// retryable (verified against this Go version; an RST-style close
		// would surface *net.OpError instead and is deliberately avoided).
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("server ResponseWriter does not support Hijack")
			serveStatus(w, http.StatusInternalServerError, "hijack unsupported")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack failed: %v", err)
			serveStatus(w, http.StatusInternalServerError, "hijack failed")
			return
		}
		defer conn.Close()

		head := "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: 1000\r\n\r\n"
		body := "data: " + `{"choices":[{"delta":{"content":"par"}}]}` + "\n\n"
		if _, err := conn.Write([]byte(head)); err != nil {
			t.Errorf("writing truncated response head: %v", err)
			return
		}
		if _, err := conn.Write([]byte(body)); err != nil {
			t.Errorf("writing truncated response body: %v", err)
			return
		}
		// FIN: the client sees EOF before the declared Content-Length.
		if tcp, ok := conn.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
	})

	sess := newRetryTestSession(t, rs.server.URL)
	onRetry, retryEvents := retryCollector(t)
	onRecover, recoveries := recoveryCollector(t)

	ctx, cancel := runLoopCtx(3, 15*time.Second)
	defer cancel()

	res, err := RunLoop(ctx, sess, 1, nil, nil, nil, nil, onRetry, onRecover, nil)

	events := retryEvents()
	posts := rs.postCount()

	switch {
	case err == nil && posts == 2 && len(events) == 1:
		// Desired path: the disconnect was retryable and attempt 2 succeeded.
	default:
		t.Fatalf("unexpected outcome: err=%v, posts=%d, retryEvents=%d, result=%q", err, posts, len(events), res)
	}

	if res != "Hello world" {
		t.Errorf("RunLoop result = %q, want %q (retry attempt content, not the partial %q)", res, "Hello world", "par")
	}

	// The successful retry produced a response: exactly one recovery.
	if got := recoveries(); got != 1 {
		t.Errorf("onRecover fired %d times, want exactly 1 (once per retried turn)", got)
	}

	ev := events[0]
	if ev.Attempt != 1 {
		t.Errorf("RetryEvent.Attempt = %d, want 1", ev.Attempt)
	}
	if ev.Delay <= 0 {
		t.Errorf("RetryEvent.Delay = %v, want > 0", ev.Delay)
	}
	if ev.Err == nil {
		t.Fatal("RetryEvent.Err is nil, want the disconnect error")
	}
	// The executor wraps the client's "stream interrupted: unexpected EOF",
	// which keeps io.ErrUnexpectedEOF in the chain.
	if !errors.Is(ev.Err, io.ErrUnexpectedEOF) {
		t.Errorf("RetryEvent.Err = %v, want it to wrap io.ErrUnexpectedEOF", ev.Err)
	}

	if got := rs.postCount(); got != 2 {
		t.Errorf("server got %d POSTs, want 2 (truncated attempt + successful retry)", got)
	}

	if len(sess.History) != 2 {
		t.Fatalf("history length = %d, want 2 (seeded user msg + committed assistant msg)", len(sess.History))
	}
	last := sess.History[len(sess.History)-1]
	if last.Role != "assistant" {
		t.Errorf("last history role = %q, want assistant", last.Role)
	}
	if last.Content.String() != "Hello world" {
		t.Errorf("last history content = %q, want %q", last.Content.String(), "Hello world")
	}
}

// TestRunLoopRetriesMidStreamTransportAbort proves the typed mid-stream
// transport failure is retried end-to-end: a 200 whose body dies partway
// surfaces from the client as *client.StreamInterruptedError,
// classifyStreamError maps it to the infrastructure tier, and RunLoop draws
// two infra retries before a complete stream succeeds. The truncated body
// deliberately omits the SSE trailing blank line, so the disconnect is never
// masked as a clean end-of-stream and the retry events are asserted
// unconditionally.
func TestRunLoopRetriesMidStreamTransportAbort(t *testing.T) {
	// Declared before newRetryServer so the handler can branch on the
	// 1-based POST count (the closure only runs once the server is up).
	var rs *retryServer
	rs = newRetryServer(t, func(w http.ResponseWriter, r *http.Request) {
		// The wrapper counts the POST before handlePost runs, so
		// posts.Load() here is the 1-based number of the current request.
		if rs.posts.Load() > 2 {
			// Third attempt onward: complete SSE stream.
			serveOK(w)
			return
		}
		// Attempts 1 and 2: a valid 200 whose body is truncated mid-stream.
		// Hijack the connection, declare a Content-Length larger than the
		// bytes actually sent, write one complete SSE data line, then an
		// unterminated partial line WITHOUT the trailing blank line, then FIN
		// without the remaining body. The unterminated tail makes the client's
		// bufio.Scanner read again (ScanLines would otherwise only emit a
		// final token at EOF), where net/http surfaces the short body as
		// io.ErrUnexpectedEOF, which the client wraps into
		// *client.StreamInterruptedError (verified against this Go version; an
		// RST-style close would surface *net.OpError instead and is
		// deliberately avoided).
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("server ResponseWriter does not support Hijack")
			serveStatus(w, http.StatusInternalServerError, "hijack unsupported")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack failed: %v", err)
			serveStatus(w, http.StatusInternalServerError, "hijack failed")
			return
		}
		defer conn.Close()

		head := "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: 1000\r\n\r\n"
		complete := "data: " + `{"choices":[{"index":0,"delta":{"content":"par"}}]}` + "\n"
		partial := "data: " + `{"choices":[{"index":0,"delta":{"content":"ti"}}` // no trailing newline
		if _, err := conn.Write([]byte(head)); err != nil {
			t.Errorf("writing truncated response head: %v", err)
			return
		}
		if _, err := conn.Write([]byte(complete)); err != nil {
			t.Errorf("writing truncated response body: %v", err)
			return
		}
		if _, err := conn.Write([]byte(partial)); err != nil {
			t.Errorf("writing unterminated partial line: %v", err)
			return
		}
		// FIN: the client sees EOF before the declared Content-Length.
		if tcp, ok := conn.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
	})

	sess := newRetryTestSession(t, rs.server.URL)
	onRetry, retryEvents := retryCollector(t)
	onRecover, recoveries := recoveryCollector(t)

	ctx, cancel := runLoopCtx(3, 15*time.Second)
	defer cancel()

	start := time.Now()
	res, err := RunLoop(ctx, sess, 1, nil, nil, nil, nil, onRetry, onRecover, nil)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("RunLoop returned error after two mid-stream aborts: %v", err)
	}
	if !strings.Contains(res, "ok") {
		t.Errorf("RunLoop result = %q, want it to contain %q", res, "ok")
	}
	// Worst-case jitter for two backoffs is 500ms + 1s; a generous bound like
	// the sibling tests (counts are pinned, never exact timings).
	if elapsed > 10*time.Second {
		t.Errorf("RunLoop took %v with two mid-stream retries, want well under that", elapsed)
	}

	// Two retries happened, but both belong to the SAME turn: recovery is a
	// once-per-recovered-turn signal, not a per-retry one.
	if got := recoveries(); got != 1 {
		t.Errorf("onRecover fired %d times, want exactly 1 (once per retried turn, not per retry)", got)
	}

	// One retry event per aborted attempt: exactly 2, attempts 1 and 2, drawn
	// from the infrastructure budget (MaxAttempts = the ctx budget of 3).
	events := retryEvents()
	if len(events) != 2 {
		t.Fatalf("got %d RetryEvents, want exactly 2 (one per aborted attempt): %+v", len(events), events)
	}
	for i, ev := range events {
		if want := i + 1; ev.Attempt != want {
			t.Errorf("events[%d].Attempt = %d, want %d", i, ev.Attempt, want)
		}
		if ev.MaxAttempts != 3 {
			t.Errorf("events[%d].MaxAttempts = %d, want 3 (ctx infra budget)", i, ev.MaxAttempts)
		}
		if ev.Delay <= 0 {
			t.Errorf("events[%d].Delay = %v, want > 0", i, ev.Delay)
		}
		if ev.Err == nil {
			t.Errorf("events[%d].Err is nil, want the mid-stream abort error", i)
			continue
		}
		// Core assertion: the client's typed mid-stream error is produced AND
		// retried end-to-end through RunLoop.
		var sie *client.StreamInterruptedError
		if !errors.As(ev.Err, &sie) {
			t.Errorf("events[%d].Err = %v (%T), want errors.As to match *client.StreamInterruptedError", i, ev.Err, ev.Err)
			continue
		}
		if !errors.Is(ev.Err, io.ErrUnexpectedEOF) {
			t.Errorf("events[%d].Err = %v, want it to wrap io.ErrUnexpectedEOF", i, ev.Err)
		}
	}

	// Two aborted attempts + one successful retry.
	if got := rs.postCount(); got != 3 {
		t.Errorf("server got %d POSTs, want 3 (2 aborted attempts + successful retry)", got)
	}

	// Failed attempts commit nothing; only the successful turn appends its
	// assistant message to the seeded history.
	if len(sess.History) != 2 {
		t.Fatalf("history length = %d, want 2 (seeded user msg + committed assistant msg)", len(sess.History))
	}
	last := sess.History[len(sess.History)-1]
	if last.Role != "assistant" {
		t.Errorf("last history role = %q, want assistant", last.Role)
	}
	if last.Content.String() != "ok" {
		t.Errorf("last history content = %q, want %q", last.Content.String(), "ok")
	}
}

// --- HTTP 400 bad-body retry tier ---
//
// RunLoop tiers inner-loop failures into two independent retry budgets:
// infrastructure failures draw from the ctx MaxStreamRetries budget, while
// HTTP 400 body-parse rejections draw from the dedicated, much smaller
// bad-body budget (DefaultMaxBadBodyRetries). The tests below pin that
// tiering end-to-end: a 400 fires exactly badBodyBudget RetryEvents whose
// MaxAttempts is the bad-body budget (never the infra budget), and the whole
// run stays bounded at 1 + DefaultMaxBadBodyRetries requests.

// badBodyErrorMessage is the error text strict OpenAI-compatible gateways
// (e.g. z.ai/GLM) return while failing to read the request body — the
// transient HTTP 400 the bad-body retry tier exists for.
const badBodyErrorMessage = "The request is invalid: read body failed. Please check the request body, required fields, and request format."

// retryChunkOK is a minimal success payload: a single delta carrying both the
// content and the terminal finish_reason "stop".
const retryChunkOK = `{"id":"1","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`

// okSSEBody renders a minimal, well-formed SSE stream: one "ok" content delta
// followed by the [DONE] sentinel.
func okSSEBody() string {
	var b strings.Builder
	fmt.Fprintf(&b, "data: %s\n", retryChunkOK)
	b.WriteString("data: [DONE]\n")
	return b.String()
}

// serveOK writes okSSEBody as a complete SSE stream.
func serveOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, okSSEBody())
}

func TestRunLoopRetries400ThenSucceeds(t *testing.T) {
	// Declared before newRetryServer so the handler can branch on the
	// 1-based POST count (the closure only runs once the server is up).
	var rs *retryServer
	rs = newRetryServer(t, func(w http.ResponseWriter, r *http.Request) {
		// The wrapper counts the POST before handlePost runs, so
		// posts.Load() here is the 1-based number of the current request.
		if rs.posts.Load() > 2 {
			// Third attempt onward: complete SSE stream.
			serveOK(w)
			return
		}
		// First two attempts: transient 400 body-parse rejection.
		serveStatus(w, http.StatusBadRequest, badBodyErrorMessage)
	})

	sess := newRetryTestSession(t, rs.server.URL)
	onRetry, retryEvents := retryCollector(t)
	onRecover, recoveries := recoveryCollector(t)

	// The infra budget must stay untouched by a 400 (those draw from the
	// bad-body tier); it is kept small so a tier-wiring regression fails
	// fast here instead of grinding through the default 10-retry budget.
	ctx, cancel := runLoopCtx(3, 15*time.Second)
	defer cancel()

	start := time.Now()
	res, err := RunLoop(ctx, sess, 1, nil, nil, nil, nil, onRetry, onRecover, nil)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("RunLoop returned error after retryable 400s: %v", err)
	}
	if !strings.Contains(res, "ok") {
		t.Errorf("RunLoop result = %q, want it to contain %q", res, "ok")
	}
	// Worst-case jitter for two backoffs is 500ms + 1s; a generous bound
	// like the sibling tests (counts are pinned, never exact timings).
	if elapsed > 10*time.Second {
		t.Errorf("RunLoop took %v with two bad-body retries, want well under that", elapsed)
	}

	// The bad-body tier recovers too: the successful retry produced a
	// response, so exactly one recovery for this turn (two 400 retries,
	// same turn).
	if got := recoveries(); got != 1 {
		t.Errorf("onRecover fired %d times, want exactly 1 (once per retried turn)", got)
	}

	// One retry event per failed attempt: exactly 2 for the two 400s. Each
	// carries the bad-body budget as MaxAttempts — the marker that
	// distinguishes this tier from the infrastructure tier.
	events := retryEvents()
	if len(events) != 2 {
		t.Fatalf("got %d RetryEvents, want exactly 2 (one per failed 400): %+v", len(events), events)
	}
	for i, ev := range events {
		if want := i + 1; ev.Attempt != want {
			t.Errorf("events[%d].Attempt = %d, want %d", i, ev.Attempt, want)
		}
		if ev.MaxAttempts != DefaultMaxBadBodyRetries {
			t.Errorf("events[%d].MaxAttempts = %d, want %d (bad-body tier, not the ctx infra budget)", i, ev.MaxAttempts, DefaultMaxBadBodyRetries)
		}
		if ev.Delay <= 0 {
			t.Errorf("events[%d].Delay = %v, want > 0", i, ev.Delay)
		}
		if ev.Err == nil {
			t.Errorf("events[%d].Err is nil, want the underlying stream error", i)
			continue
		}
		if !strings.Contains(ev.Err.Error(), "API error (400)") {
			t.Errorf("events[%d].Err = %v, want it to contain %q", i, ev.Err, "API error (400)")
		}
		var statusErr *client.StatusError
		if !errors.As(ev.Err, &statusErr) || statusErr.StatusCode != http.StatusBadRequest {
			t.Errorf("events[%d].Err = %v, want it to wrap *client.StatusError with 400", i, ev.Err)
		}
	}

	// Two failed 400 attempts + one successful retry.
	if got := rs.postCount(); got != 3 {
		t.Errorf("server got %d POSTs, want 3 (2 failed 400s + successful retry)", got)
	}

	// Failed attempts commit nothing; only the successful turn appends its
	// assistant message to the seeded history.
	if len(sess.History) != 2 {
		t.Fatalf("history length = %d, want 2 (seeded user msg + committed assistant msg)", len(sess.History))
	}
	last := sess.History[len(sess.History)-1]
	if last.Role != "assistant" {
		t.Errorf("last history role = %q, want assistant", last.Role)
	}
	if last.Content.String() != "ok" {
		t.Errorf("last history content = %q, want %q", last.Content.String(), "ok")
	}
}

func TestRunLoopStopsAfterBadBodyBudgetExhausted(t *testing.T) {
	rs := newRetryServer(t, func(w http.ResponseWriter, r *http.Request) {
		serveStatus(w, http.StatusBadRequest, badBodyErrorMessage)
	})

	sess := newRetryTestSession(t, rs.server.URL)
	onRetry, retryEvents := retryCollector(t)
	onRecover, recoveries := recoveryCollector(t)

	// A small infra budget that must never be touched by a 400. If the tier
	// wiring were wrong and 400s drew from the infra budget instead, the
	// request-count assertion below would fail (or, against the default
	// budget of 100, the run would take minutes and hit the 15s deadline).
	ctx, cancel := runLoopCtx(2, 15*time.Second)
	defer cancel()

	start := time.Now()
	_, err := RunLoop(ctx, sess, 1, nil, nil, nil, nil, onRetry, onRecover, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("RunLoop returned nil error, want failure after the bad-body budget exhausted")
	}
	// The executor wraps the client's StatusError in "stream error: ..." and
	// the gateway's message must propagate for diagnostics.
	if !strings.Contains(err.Error(), "API error (400)") {
		t.Fatalf("RunLoop error = %v, want it to contain %q", err, "API error (400)")
	}
	if !strings.Contains(err.Error(), "read body failed") {
		t.Errorf("RunLoop error = %v, want the provider's message to propagate", err)
	}
	var statusErr *client.StatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("RunLoop error = %v, want it to wrap *client.StatusError with 400", err)
	}
	// Worst-case jitter for three backoffs is 500ms + 1s + 2s; 10s is a
	// generous sanity bound.
	if elapsed > 10*time.Second {
		t.Errorf("RunLoop took %v to exhaust the bad-body budget, want well under that", elapsed)
	}

	events := retryEvents()
	if len(events) != DefaultMaxBadBodyRetries {
		t.Fatalf("got %d RetryEvents, want exactly %d: %+v", len(events), DefaultMaxBadBodyRetries, events)
	}
	for i, ev := range events {
		if want := i + 1; ev.Attempt != want {
			t.Errorf("events[%d].Attempt = %d, want %d", i, ev.Attempt, want)
		}
		if ev.MaxAttempts != DefaultMaxBadBodyRetries {
			t.Errorf("events[%d].MaxAttempts = %d, want %d (bad-body tier, not the infra budget)", i, ev.MaxAttempts, DefaultMaxBadBodyRetries)
		}
		if ev.Delay <= 0 {
			t.Errorf("events[%d].Delay = %v, want > 0", i, ev.Delay)
		}
		if ev.Err == nil {
			t.Errorf("events[%d].Err is nil, want the underlying stream error", i)
		}
	}

	// Initial attempt + exactly DefaultMaxBadBodyRetries retries: pins the
	// bad-body tier at 3 and proves it does NOT consume the default
	// 10-retry infrastructure budget.
	want := 1 + DefaultMaxBadBodyRetries
	if got := rs.postCount(); got != want {
		t.Errorf("server got %d POSTs, want exactly %d (initial attempt + bad-body retries)", got, want)
	}

	if len(sess.History) != 1 {
		t.Errorf("history length = %d, want 1 (nothing committed)", len(sess.History))
	}

	// Exhaustion is not recovery: no attempt ever produced a response.
	if got := recoveries(); got != 0 {
		t.Errorf("onRecover fired %d times, want 0 (bad-body exhaustion is not recovery)", got)
	}
}

// TestRunLoopGlobalDisableAlsoSilencesBadBodyTier pins the global-disable
// contract end-to-end: a global disable (--max-stream-retries=0, negative,
// or the ctx key) must silence BOTH retry tiers. Without the budget clamp at
// the resolution site in RunLoop, the bad-body tier would fall back to its
// own default budget (DefaultMaxBadBodyRetries) and keep retrying HTTP 400s
// despite the advertised "retries disabled" contract. With the disable in
// effect, an always-400 server sees exactly one POST — the initial attempt,
// zero retries, zero backoff sleeps — and the run fails with the terminal
// 400. The positive-budget contrast is pinned by
// TestRunLoopStopsAfterBadBodyBudgetExhausted above: with MaxStreamRetriesKey
// = 2 the bad-body tier still uses its own budget (4 POSTs).
func TestRunLoopGlobalDisableAlsoSilencesBadBodyTier(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget int
	}{
		{name: "zero_budget", budget: 0},
		{name: "negative_budget", budget: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs := newRetryServer(t, func(w http.ResponseWriter, r *http.Request) {
				serveStatus(w, http.StatusBadRequest, badBodyErrorMessage)
			})

			sess := newRetryTestSession(t, rs.server.URL)
			onRetry, retryEvents := retryCollector(t)

			// runLoopCtx passes the budget through as the MaxStreamRetriesKey
			// ctx value and adds only a generous deadline; with both tiers
			// disabled there are no backoff sleeps, so the deadline is inert.
			// It is kept (rather than a bare context.WithValue) so a bug can
			// never hang the test until the global -timeout, same rationale
			// as every sibling test in this file.
			ctx, cancel := runLoopCtx(tc.budget, 15*time.Second)
			defer cancel()

			start := time.Now()
			_, err := RunLoop(ctx, sess, 1, nil, nil, nil, nil, onRetry, nil, nil)
			elapsed := time.Since(start)

			if err == nil {
				t.Fatal("RunLoop returned nil error, want the terminal 400 despite retries being disabled")
			}
			var statusErr *client.StatusError
			if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusBadRequest {
				t.Fatalf("RunLoop error = %v, want it to wrap *client.StatusError with 400", err)
			}

			// Exactly one POST: the initial attempt. Zero retries from either
			// tier — the global disable silences the bad-body tier too.
			if got := rs.postCount(); got != 1 {
				t.Errorf("server got %d POSTs, want exactly 1 (initial attempt, zero retries)", got)
			}
			if events := retryEvents(); len(events) != 0 {
				t.Errorf("got %d RetryEvents, want 0 (retries disabled): %+v", len(events), events)
			}

			// No backoff sleeps: the terminal 400 must surface immediately.
			if elapsed >= 2*time.Second {
				t.Errorf("RunLoop took %v, want well under 2s (no backoff sleeps when retries are disabled)", elapsed)
			}
		})
	}
}

// toolCallSSEBody renders a complete SSE stream that ends in a tool call
// (finish_reason "tool_calls") instead of a final text response: the turn
// commits an assistant tool-call message and the loop advances to the next
// turn. The named tool is deliberately unregistered, so ExecuteToolCalls
// records an error tool result and the run continues.
func toolCallSSEBody() string {
	var b strings.Builder
	fmt.Fprintf(&b, "data: %s\n", `{"id":"t1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"no_such_tool","arguments":"{}"}}]}}]}`)
	fmt.Fprintf(&b, "data: %s\n", `{"id":"t1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
	b.WriteString("data: [DONE]\n")
	return b.String()
}

// serveToolCallSSE writes toolCallSSEBody as a complete SSE stream.
func serveToolCallSSE(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, toolCallSSEBody())
}

// TestRunLoopRecoveryOncePerTurn proves the recovery signal is scoped to a
// TURN, not a RUN: two consecutive turns that each retry — the first recovers
// into a tool call so the loop advances, the second into the final text
// response — must fire onRecover exactly once per turn, two recoveries total.
// This pins the sequencing the recovery event exists for: the turn-start
// callback fires before the retries, so onRecover is the only per-turn
// "the retry actually produced a response" signal, whether or not the turn
// also ends the run.
func TestRunLoopRecoveryOncePerTurn(t *testing.T) {
	// Declared before newRetryServer so the handler can branch on the
	// 1-based POST count (the closure only runs once the server is up).
	var rs *retryServer
	rs = newRetryServer(t, func(w http.ResponseWriter, r *http.Request) {
		// The wrapper counts the POST before handlePost runs, so
		// posts.Load() here is the 1-based number of the current request.
		switch {
		case rs.posts.Load() == 1 || rs.posts.Load() == 3:
			// Turns 1 and 2 each start with a transient 500.
			serveStatus(w, http.StatusInternalServerError, "upstream exploded")
		case rs.posts.Load() == 2:
			// Turn 1 retries into a tool call: the run continues.
			serveToolCallSSE(w)
		default:
			// Turn 2 retries into the final text response.
			serveSSE(w)
		}
	})

	sess := newRetryTestSession(t, rs.server.URL)
	onRetry, retryEvents := retryCollector(t)
	onRecover, recoveries := recoveryCollector(t)

	ctx, cancel := runLoopCtx(3, 15*time.Second)
	defer cancel()

	res, err := RunLoop(ctx, sess, 2, nil, nil, nil, nil, onRetry, onRecover, nil)
	if err != nil {
		t.Fatalf("RunLoop returned error across two retried turns: %v", err)
	}
	if res != "Hello world" {
		t.Errorf("RunLoop result = %q, want %q", res, "Hello world")
	}

	if got := rs.postCount(); got != 4 {
		t.Errorf("server got %d POSTs, want 4 (500+tool-call turn, 500+success turn)", got)
	}
	if events := retryEvents(); len(events) != 2 {
		t.Errorf("got %d RetryEvents, want exactly 2 (one failed attempt per turn): %+v", len(events), events)
	}
	// CORE assertion: one recovery PER TURN — turn 1 (retried into a tool
	// call) and turn 2 (retried into the final response) each recovered.
	if got := recoveries(); got != 2 {
		t.Errorf("onRecover fired %d times, want exactly 2 (once per retried turn)", got)
	}

	// History: seeded user msg + assistant tool call + tool result + the
	// final assistant reply.
	if len(sess.History) != 4 {
		t.Fatalf("history length = %d, want 4 (seeded user, tool call, tool result, final reply)", len(sess.History))
	}
}

// TestRunLoopOnRecoverFiresOnHTTPConnectBeforeChunks verifies that onRecover fires
// upon HTTP 200 connect of the retried attempt, before streaming has finished.
func TestRunLoopOnRecoverFiresOnHTTPConnectBeforeChunks(t *testing.T) {
	var recoveryTime time.Time
	var streamFinishedTime time.Time

	var posts atomic.Int64
	rs := newRetryServer(t, func(w http.ResponseWriter, r *http.Request) {
		p := posts.Add(1)
		if p == 1 {
			serveStatus(w, http.StatusInternalServerError, "first attempt down")
			return
		}
		// Second attempt: send 200 headers, then delay before sending chunks
		// to simulate prompt processing / TTFT
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(100 * time.Millisecond)
		fmt.Fprint(w, "data: "+`{"choices":[{"index":0,"delta":{"content":"delayed chunk"}}]}`+"\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(50 * time.Millisecond)
		fmt.Fprint(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})

	sess := newRetryTestSession(t, rs.server.URL)
	onRecover := func() {
		recoveryTime = time.Now()
	}

	ctx, cancel := runLoopCtx(3, 15*time.Second)
	defer cancel()

	res, err := RunLoop(ctx, sess, 1, nil, nil, nil, nil, nil, onRecover, nil)
	streamFinishedTime = time.Now()

	if err != nil {
		t.Fatalf("RunLoop failed: %v", err)
	}
	if res != "delayed chunk" {
		t.Fatalf("RunLoop result = %q, want 'delayed chunk'", res)
	}
	if recoveryTime.IsZero() {
		t.Fatal("onRecover was never called")
	}
	// recoveryTime should have fired well before streamFinishedTime (at least ~100ms before)
	if streamFinishedTime.Sub(recoveryTime) < 50*time.Millisecond {
		t.Errorf("onRecover fired at %v, but stream finished at %v; difference %v should be >= 50ms",
			recoveryTime, streamFinishedTime, streamFinishedTime.Sub(recoveryTime))
	}
}
