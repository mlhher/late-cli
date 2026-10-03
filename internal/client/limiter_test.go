package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestAcquireLLMSlot_BoundsConcurrency proves the limiter actually bounds
// in-flight requests: 8 goroutines race for slots against a cap of 2, and
// the observed peak concurrency must never exceed the cap. Without the
// limiter (unlimited mode hands out nil releases) the same workload would
// peak at 8 and fail this test.
func TestAcquireLLMSlot_BoundsConcurrency(t *testing.T) {
	SetLLMConcurrency(2)
	defer SetLLMConcurrency(0) // restore unlimited for the rest of the package's tests

	const workers = 8
	var (
		cur    atomic.Int64
		maxCur atomic.Int64
		wg     sync.WaitGroup
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := acquireLLMSlot(context.Background())
			if release == nil {
				t.Error("acquireLLMSlot returned nil release under a cap of 2 — limiter inactive")
				return
			}
			defer release()

			now := cur.Add(1)
			for {
				peak := maxCur.Load()
				if now <= peak || maxCur.CompareAndSwap(peak, now) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
			// LIFO defers: cur is decremented BEFORE the slot is released, so
			// cur never over-counts the currently held slots even when a
			// waiter acquires the freed slot between release and decrement.
			defer cur.Add(-1)
		}()
	}
	wg.Wait()

	peak := maxCur.Load()
	t.Logf("peak concurrent slot holders = %d (cap 2, workers %d)", peak, workers)
	if peak > 2 {
		t.Errorf("peak concurrent slot holders = %d, want <= 2", peak)
	}
	if peak < 2 {
		t.Errorf("peak concurrent slot holders = %d, want 2 (cap 2 must allow two in flight)", peak)
	}
}

// TestAcquireLLMSlot_CancelWhileQueued proves a queued acquire is
// cancelable: with the only slot held, an acquire whose context is canceled
// must return promptly with a nil release instead of hanging or stealing a
// slot, and the limiter state must stay intact for later acquires.
func TestAcquireLLMSlot_CancelWhileQueued(t *testing.T) {
	SetLLMConcurrency(1)
	defer SetLLMConcurrency(0)

	// The single slot is held for the duration of the queued attempt.
	release := acquireLLMSlot(context.Background())
	if release == nil {
		t.Fatal("acquireLLMSlot returned nil release with a free slot under cap 1")
	}
	defer func() {
		if release != nil {
			release()
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	var got func()
	go func() {
		defer close(done)
		got = acquireLLMSlot(ctx)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled acquire did not return — queueing is not cancelable")
	}
	if got != nil {
		t.Error("canceled acquire returned a non-nil release; it must fail while the slot is held")
		got() // best-effort: don't leak the slot if the limiter ever hands one out
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("ctx.Err() = %v, want context.Canceled", ctx.Err())
	}

	// The canceled waiter must have taken nothing: release the holder and
	// re-acquire to prove the slot is still usable.
	release()
	release = nil
	if again := acquireLLMSlot(context.Background()); again == nil {
		t.Error("slot unusable after a canceled waiter — limiter state corrupted")
	} else {
		again()
	}
}

// TestSetLLMConcurrency_Unlimited proves the unlimited mode (0 or negative)
// hands out no release funcs and never blocks: concurrent acquires return
// immediately with nil.
func TestSetLLMConcurrency_Unlimited(t *testing.T) {
	defer SetLLMConcurrency(0)

	for _, n := range []int{0, -3} {
		SetLLMConcurrency(n)
		for i := 0; i < 4; i++ {
			done := make(chan struct{})
			go func() {
				defer close(done)
				if release := acquireLLMSlot(context.Background()); release != nil {
					t.Errorf("SetLLMConcurrency(%d): acquire returned non-nil release — limiter still active", n)
					release()
				}
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatalf("SetLLMConcurrency(%d): acquire blocked — unlimited mode must not gate requests", n)
			}
		}
	}
}

// TestChatCompletionStream_LimiterHeld proves the stream path holds its slot
// for the stream's entire lifetime: with a process-wide cap of 1, two
// concurrent ChatCompletionStream calls must never overlap inside the server
// handler — the second request only reaches the server after the first
// stream is fully consumed. A slot released at Do-return (handshake only)
// would let both handlers run concurrently and fail this test.
func TestChatCompletionStream_LimiterHeld(t *testing.T) {
	SetLLMConcurrency(1)
	defer SetLLMConcurrency(0)

	var (
		mu          sync.Mutex
		inFlight    int
		maxInFlight int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// DiscoverBackend probes these paths before the POST; they are not
		// LLM requests and must not count toward the stream bound.
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		mu.Unlock()
		defer func() {
			mu.Lock()
			inFlight--
			mu.Unlock()
		}()

		// Hold the handler open long enough that a second concurrent
		// request would overlap this one without the limiter.
		time.Sleep(150 * time.Millisecond)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseChunks(sampleChunkHello), "\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	c := NewClient(Config{BaseURL: server.URL})

	const calls = 2
	type result struct {
		chunks []ChatCompletionChunk
		err    error
	}
	results := make([]result, calls)

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // fire both requests at once so they race for the slot
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			out, errCh := c.ChatCompletionStream(ctx, defaultRequest())
			for chunk := range out {
				results[i].chunks = append(results[i].chunks, chunk)
			}
			select {
			case err := <-errCh:
				results[i].err = err
			default:
			}
		}(i)
	}
	close(start)

	// Generous ceiling on the whole run: with cap 1 the two 150 ms streams
	// serialize to ~300 ms; anything near the ceiling means the limiter
	// deadlocked a queued request.
	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(15 * time.Second):
		t.Fatal("streams did not finish — the limiter likely deadlocked a queued request")
	}

	mu.Lock()
	peak, remaining := maxInFlight, inFlight
	mu.Unlock()
	t.Logf("max concurrent stream handlers = %d (cap 1)", peak)
	if remaining != 0 {
		t.Errorf("handler in-flight count = %d after both streams finished, want 0", remaining)
	}
	if peak != 1 {
		t.Errorf("max concurrent stream handlers = %d, want exactly 1 — the slot must be held for the stream's lifetime", peak)
	}

	for i := range results {
		if results[i].err != nil {
			t.Errorf("stream %d: unexpected error: %v", i, results[i].err)
			continue
		}
		if got := len(results[i].chunks); got != 1 {
			t.Errorf("stream %d: got %d chunks, want 1", i, got)
			continue
		}
		if content := accumulateContent(results[i].chunks); content != "Hello" {
			t.Errorf("stream %d: content = %q, want %q", i, content, "Hello")
		}
	}
}
