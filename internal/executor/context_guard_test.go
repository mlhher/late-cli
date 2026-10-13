package executor

// Phase B context-exhaustion safeguard: end-to-end RunLoop tests driven
// against an in-process httptest SSE server (same harness as
// stream_retry_integration_test.go):
//
//	RunLoop -> session.StartStream -> client.ChatCompletionStream -> httptest
//
// Reactive (B4): a 400 whose body names the token limit must fail the
// attempt, run the installed compactor, and retry through the same attempt
// machinery — exactly two POSTs when compaction frees something, one when
// it cannot, and zero bad-body-retry events either way.
// Predictive (B5): with a known context window and an oversized history,
// the compactor must run BEFORE the first stream request.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"late/internal/client"
	"late/internal/common"
)

// context400Body is the OpenAI context-exhaustion error body.
const context400Body = `{"error":{"message":"This model's maximum context length is 8192 tokens. However, your messages resulted in 9000 tokens.","type":"invalid_request_error","code":"context_length_exceeded"}}`

// contextLengthChunk is a terminal chunk carrying finish_reason "length"
// plus a usage total at (or past) the window — the truncating-provider
// shape (usage.chunks are decoded by the client's SSE scan).
const contextLengthChunk = `{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":90,"completion_tokens":10,"total_tokens":100}}`

// lengthSSEBody serves one chunk announcing context-exhausting truncation,
// then [DONE].
func lengthSSEBody() string {
	return "data: " + contextLengthChunk + "\n\ndata: [DONE]\n\n"
}

// countingCompactor records compaction invocations and returns a fixed
// outcome. Runs off RunLoop's goroutine; the counter is atomic.
type countingCompactor struct {
	runs atomic.Int64
	free bool
}

func (c *countingCompactor) compact(ctx context.Context) (CompactionReport, error) {
	c.runs.Add(1)
	if !c.free {
		return CompactionReport{}, nil
	}
	return CompactionReport{TokensSaved: 500, MessagesCompacted: 2}, nil
}

// mustContextClient builds a client whose context window is explicitly
// known (as llama.cpp discovery or an explicit context-size-tokens would
// provide) — RunLoop reads it through sess.Client().ContextSize().
func mustContextClient(t *testing.T, baseURL string, ctxSize int) *client.Client {
	t.Helper()
	c := client.NewClient(client.Config{BaseURL: baseURL})
	c.SetContextSize(ctxSize)
	return c
}

// installCompactor sets the process-wide context compactor and restores the
// previous installation at test end (the guard is process-wide; other tests
// in the package must not observe this one's hook).
func installCompactor(t *testing.T, c ContextCompactor) {
	t.Helper()
	prev := getContextCompactor()
	SetContextCompactor(c)
	t.Cleanup(func() { SetContextCompactor(prev) })
}

// noRetryCtx disables the classic retry tiers entirely: any RetryEvent the
// tests observe comes from the context guard, not a backoff tier.
func noRetryCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(
		context.WithValue(context.Background(), common.MaxStreamRetriesKey, -1),
		15*time.Second,
	)
	t.Cleanup(cancel)
	return ctx
}

// TestRunLoopContextGuardCompactsAndRetries: a 400 naming the token limit
// with a working compactor produces exactly 2 requests (1 fail + 1 retry
// after compaction), the compaction ran between them, and the successful
// retry commits normally. No bad-body retry fires (retries disabled
// globally AND the sentinel class fails fast regardless of budget).
func TestRunLoopContextGuardCompactsAndRetries(t *testing.T) {
	var failureServed atomic.Bool
	var compactedBeforeSecond atomic.Bool
	compactor := &countingCompactor{free: true}
	installCompactor(t, compactor.compact)
	rs := newRetryServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !failureServed.CompareAndSwap(false, true) {
			compactedBeforeSecond.Store(compactor.runs.Load() > 0)
			serveSSE(w)
			return
		}
		serveStatus(w, http.StatusBadRequest, "This model's maximum context length is 8192 tokens")
	})

	sess := newRetryTestSession(t, rs.server.URL)
	onRetry, retryEvents := retryCollector(t)
	onRecover, _ := recoveryCollector(t)

	res, err := RunLoop(noRetryCtx(t), sess, 1, nil, nil, nil, nil, onRetry, onRecover, nil)
	if err != nil {
		t.Fatalf("RunLoop returned error: %v", err)
	}
	if res != "Hello world" {
		t.Errorf("result = %q, want %q", res, "Hello world")
	}
	if got := rs.postCount(); got != 2 {
		t.Errorf("server got %d POSTs, want 2 (failed attempt + retry after compaction)", got)
	}
	if got := compactor.runs.Load(); got != 1 {
		t.Errorf("compactor ran %d times, want exactly 1", got)
	}
	if !compactedBeforeSecond.Load() {
		t.Error("the second request must be served only after the compaction ran")
	}
	// The failed attempt committed nothing; the retry committed the reply.
	if len(sess.History) != 2 {
		t.Fatalf("history length = %d, want 2 (seeded user msg + committed assistant msg)", len(sess.History))
	}
	if sess.History[len(sess.History)-1].Content.String() != "Hello world" {
		t.Errorf("last history content = %q, want %q", sess.History[len(sess.History)-1].Content.String(), "Hello world")
	}
	// Exactly one guard event surfaced, carrying the sentinel and the
	// compaction outcome; nothing from a bad-body tier.
	events := retryEvents()
	if len(events) != 1 {
		t.Fatalf("got %d RetryEvents, want exactly 1 (the guard round): %+v", len(events), events)
	}
	if !errors.Is(events[0].Err, client.ErrContextExceeded) {
		t.Errorf("RetryEvent.Err = %v, want it to wrap ErrContextExceeded", events[0].Err)
	}
	if !strings.Contains(events[0].Err.Error(), "saved ~500 tokens") {
		t.Errorf("RetryEvent.Err = %q, want the compaction outcome in the text", events[0].Err.Error())
	}
	if events[0].Delay != 0 {
		t.Errorf("RetryEvent.Delay = %v, want 0 (no backoff for a deterministic failure)", events[0].Delay)
	}
	// onRecover is deliberately NOT expected here: it is defined as
	// "recovery after a RETRY TIER backoff" and the guard's connect-hook
	// arming checks (infra+badBody+throttleAttempts > 0) are tier-scoped
	// by design — the guard's retry is a separate, compaction-scoped path.
}

// TestRunLoopContextGuardWithoutCompactorFailsFast: no compactor installed
// → the typed error with guidance surfaces after exactly ONE request, zero
// retries of any tier, nothing committed.
func TestRunLoopContextGuardWithoutCompactorFailsFast(t *testing.T) {
	rs := newRetryServer(t, func(w http.ResponseWriter, r *http.Request) {
		serveStatus(w, http.StatusBadRequest, "This model's maximum context length is 8192 tokens")
	})
	installCompactor(t, nil)

	sess := newRetryTestSession(t, rs.server.URL)
	onRetry, retryEvents := retryCollector(t)
	onRecover, recoveries := recoveryCollector(t)

	_, err := RunLoop(noRetryCtx(t), sess, 1, nil, nil, nil, nil, onRetry, onRecover, nil)
	if err == nil {
		t.Fatal("RunLoop returned nil error, want the typed context-exhaustion failure")
	}
	if !errors.Is(err, client.ErrContextExceeded) {
		t.Fatalf("error %v does not wrap ErrContextExceeded", err)
	}
	if !strings.Contains(err.Error(), client.ContextExceededGuidance) {
		t.Errorf("error = %q, want the guidance text", err.Error())
	}
	if !strings.Contains(err.Error(), "auto-compaction unavailable") {
		t.Errorf("error = %q, want it to name the unavailable compaction outcome", err.Error())
	}
	if got := rs.postCount(); got != 1 {
		t.Errorf("server got %d POSTs, want exactly 1 (deterministic failure, no retries)", got)
	}
	if events := retryEvents(); len(events) != 0 {
		t.Errorf("got %d RetryEvents, want 0", len(events))
	}
	if got := recoveries(); got != 0 {
		t.Errorf("onRecover fired %d times, want 0", got)
	}
	if len(sess.History) != 1 {
		t.Errorf("history length = %d, want 1 (nothing committed)", len(sess.History))
	}
}

// TestRunLoopContextGuardGivesUpAfterTwoRounds: the guard's two rounds are
// RECOVERY-SCOPED — a round is only spent (compactor run + retry) when the
// previous run actually freed something, so a compactor that never frees
// anything is consulted exactly once before the typed guidance surfaces.
// A freed-then-stuck sequence (freed on round 1, nothing on round 2) burns
// both rounds and then gives up.
func TestRunLoopContextGuardGivesUpAfterTwoRounds(t *testing.T) {
	t.Run("never frees: one compaction, no retry, typed guidance", func(t *testing.T) {
		rs := newRetryServer(t, func(w http.ResponseWriter, r *http.Request) {
			serveStatus(w, http.StatusBadRequest, "This model's maximum context length is 8192 tokens")
		})
		compactor := &countingCompactor{free: false}
		installCompactor(t, compactor.compact)

		sess := newRetryTestSession(t, rs.server.URL)
		onRetry, retryEvents := retryCollector(t)

		_, err := RunLoop(noRetryCtx(t), sess, 1, nil, nil, nil, nil, onRetry, nil, nil)
		if err == nil {
			t.Fatal("RunLoop returned nil error, want the typed failure")
		}
		if !errors.Is(err, client.ErrContextExceeded) {
			t.Fatalf("error %v does not wrap ErrContextExceeded", err)
		}
		if got := compactor.runs.Load(); got != 1 {
			t.Errorf("compactor ran %d times, want 1 (a round is only spent when something was freed)", got)
		}
		if got := rs.postCount(); got != 1 {
			t.Errorf("server got %d POSTs, want 1 (nothing freed → no retry)", got)
		}
		if !strings.Contains(err.Error(), "freed nothing") {
			t.Errorf("error = %q, want it to state the compaction freed nothing", err.Error())
		}
		if events := retryEvents(); len(events) != 0 {
			t.Errorf("got %d RetryEvents, want 0", len(events))
		}
	})

	t.Run("frees once then nothing: both rounds spent, then give up", func(t *testing.T) {
		rs := newRetryServer(t, func(w http.ResponseWriter, r *http.Request) {
			serveStatus(w, http.StatusBadRequest, "This model's maximum context length is 8192 tokens")
		})
		compactor := &countingCompactor{free: true}
		installCompactor(t, compactor.compact)

		sess := newRetryTestSession(t, rs.server.URL)
		_, err := RunLoop(noRetryCtx(t), sess, 1, nil, nil, nil, nil, nil, nil, nil)
		if err == nil {
			t.Fatal("RunLoop returned nil error, want the typed failure after both rounds")
		}
		if !errors.Is(err, client.ErrContextExceeded) {
			t.Fatalf("error %v does not wrap ErrContextExceeded", err)
		}
		if got := compactor.runs.Load(); got != int64(maxContextCompactionRounds) {
			t.Errorf("compactor ran %d times, want %d (both rounds, then give up)", got, maxContextCompactionRounds)
		}
		if got := rs.postCount(); got != maxContextCompactionRounds+1 {
			t.Errorf("server got %d POSTs, want %d (initial + one retry per freed round)", got, maxContextCompactionRounds+1)
		}
		if len(sess.History) != 1 {
			t.Errorf("history length = %d, want 1 (failed attempts commit nothing)", len(sess.History))
		}
	})
}

// TestRunLoopFinishReasonLengthIsContextExceeded: a truncating provider ends
// the stream with finish_reason=length and usage at the window → the error
// surfaces as the typed sentinel (same safeguard path), not the legacy
// "exceeds the available context size" prose.
func TestRunLoopFinishReasonLengthIsContextExceeded(t *testing.T) {
	rs := newRetryServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, lengthSSEBody())
	})
	installCompactor(t, nil)

	sess := newRetryTestSession(t, rs.server.URL)
	// Known window: 100 total tokens reported ≥ 95% of 100.
	sess.SetClient(mustContextClient(t, rs.server.URL, 100))

	_, err := RunLoop(noRetryCtx(t), sess, 1, nil, nil, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("RunLoop returned nil error, want the typed context-exhaustion failure")
	}
	if !errors.Is(err, client.ErrContextExceeded) {
		t.Fatalf("error %v does not wrap ErrContextExceeded (finish_reason=length must classify)", err)
	}
	var ce *client.ContextExceededError
	if !errors.As(err, &ce) || ce.Reason != "finish_reason=length" {
		t.Errorf("error = %v, want a *ContextExceededError with Reason finish_reason=length", err)
	}
	if got := rs.postCount(); got != 1 {
		t.Errorf("server got %d POSTs, want 1", got)
	}
	// Output truncation on a NON-full context keeps the legacy continuation:
	// pin that path stays reachable by checking the discriminant — same
	// stream, unknown window (0) → not exhaustion → continuation commits.
	sess2 := newRetryTestSession(t, rs.server.URL)
	res, err := RunLoop(noRetryCtx(t), sess2, 2, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("RunLoop with unknown window returned error: %v", err)
	}
	if res == "" {
		t.Error("RunLoop with unknown window should continue past output truncation, not fail")
	}
}

// TestRunLoopPredictiveCompactionBeforeFirstRequest: a known window plus an
// estimated history over 95% → the compactor runs BEFORE any stream request
// (the first POST already sees the compacted history), at most once.
func TestRunLoopPredictiveCompactionBeforeFirstRequest(t *testing.T) {
	var firstServed atomic.Bool
	rs := newRetryServer(t, func(w http.ResponseWriter, r *http.Request) {
		firstServed.Store(true)
		serveSSE(w)
	})
	compactor := &countingCompactor{free: true}
	installCompactor(t, compactor.compact)

	sess := newRetryTestSession(t, rs.server.URL)
	sess.SetClient(mustContextClient(t, rs.server.URL, 100)) // tiny window
	// Oversized history: a compactable assistant dump far over 95 tokens.
	sess.History = append(sess.History, client.ChatMessage{
		Role:      "assistant",
		Content:   client.TextContent(strings.Repeat("verbose work output ", 200)),
		ToolCalls: []client.ToolCall{{Index: 0, ID: "call_1", Type: "function", Function: client.FunctionCall{Name: "Bash", Arguments: `{}`}}},
	})

	res, err := RunLoop(noRetryCtx(t), sess, 1, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("RunLoop returned error: %v", err)
	}
	if res != "Hello world" {
		t.Errorf("result = %q, want %q", res, "Hello world")
	}
	if got := compactor.runs.Load(); got != 1 {
		t.Errorf("compactor ran %d times, want exactly 1 (predictive, capped)", got)
	}
	if got := rs.postCount(); got != 1 {
		t.Errorf("server got %d POSTs, want 1 (the predictive run avoids the doomed request)", got)
	}
}

// TestRunLoopPredictiveCompactionSkippedWithoutWindowOrHook pins the gate:
// no heuristic without a known ctxSize, and none without a compactor.
func TestRunLoopPredictiveCompactionSkippedWithoutWindowOrHook(t *testing.T) {
	t.Run("unknown window skips", func(t *testing.T) {
		rs := newRetryServer(t, func(w http.ResponseWriter, r *http.Request) {
			serveSSE(w)
		})
		compactor := &countingCompactor{free: true}
		installCompactor(t, compactor.compact)

		sess := newRetryTestSession(t, rs.server.URL) // ctxSize -1
		if _, err := RunLoop(noRetryCtx(t), sess, 1, nil, nil, nil, nil, nil, nil, nil); err != nil {
			t.Fatalf("RunLoop returned error: %v", err)
		}
		if got := compactor.runs.Load(); got != 0 {
			t.Errorf("compactor ran %d times, want 0 (window unknown)", got)
		}
	})

	t.Run("no compactor installed skips", func(t *testing.T) {
		rs := newRetryServer(t, func(w http.ResponseWriter, r *http.Request) {
			serveSSE(w)
		})
		installCompactor(t, nil)

		sess := newRetryTestSession(t, rs.server.URL)
		sess.SetClient(mustContextClient(t, rs.server.URL, 100))
		sess.History = append(sess.History, client.ChatMessage{
			Role:      "assistant",
			Content:   client.TextContent(strings.Repeat("verbose work output ", 200)),
			ToolCalls: []client.ToolCall{{Index: 0, ID: "call_1", Type: "function", Function: client.FunctionCall{Name: "Bash", Arguments: `{}`}}},
		})
		if _, err := RunLoop(noRetryCtx(t), sess, 1, nil, nil, nil, nil, nil, nil, nil); err != nil {
			t.Fatalf("RunLoop returned error: %v", err)
		}
		if got := rs.postCount(); got != 1 {
			t.Errorf("server got %d POSTs, want 1 (no heuristic → the request proceeds as always)", got)
		}
	})
}
