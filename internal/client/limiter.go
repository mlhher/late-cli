package client

import (
	"context"
	"sync"
)

// Process-wide LLM concurrency limiter.
//
// Every client created in this process — the root agent's client and every
// subagent's client — shares the bound below. Providers enforce
// account/model concurrency limits (e.g. Tencent Cloud's 429 "The request
// rate exceeds the current model Concurrency limit 1200"), so parallel
// agents each holding their own unbounded client stampede that limit and
// every one of them gets rejected. Queuing the whole fleet behind one
// process-wide bound turns the stampede into pacing: at most N requests are
// in flight at any moment and the rest wait here instead of being 429'd.
//
// A slot covers the provider-visible request lifetime, not just the
// handshake: for a stream that means from just before the HTTP request until
// the stream body is fully consumed, which is what providers count as
// concurrency. The limiter is inert until SetLLMConcurrency activates it
// (main() does so right after flag parsing), which also keeps it transparent
// to tests and library users that never opt in.

var (
	// llmMu guards llmSlots/llmCapacity. Request paths only take a cheap
	// snapshot under it; SetLLMConcurrency is expected to run once before
	// any agent starts and is not designed to race with in-flight requests
	// (a waiter already queued on the old channel keeps waiting there until
	// it acquires or its context ends).
	llmMu sync.Mutex
	// llmSlots is the counting channel; nil means the limiter is inactive
	// (unlimited) — the default until SetLLMConcurrency installs a channel.
	llmSlots chan struct{}
	// llmCapacity mirrors the channel bound; 0 or negative means unlimited.
	llmCapacity int = 6
)

// SetLLMConcurrency sets the process-wide cap on concurrent in-flight LLM
// requests. 0 or negative = unlimited. Must be called before agents start
// (main() does it right after flag parsing); not goroutine-safe by design.
func SetLLMConcurrency(n int) {
	llmMu.Lock()
	defer llmMu.Unlock()
	llmCapacity = n
	if n <= 0 {
		llmSlots = nil
		return
	}
	llmSlots = make(chan struct{}, n)
}

// acquireLLMSlot blocks until a slot is free or ctx is done. It returns a
// release func that must be called exactly once when the request finishes;
// the returned func is nil when the limiter is unlimited or when ctx ended
// before a slot could be acquired (callers distinguish the two via
// ctx.Err()). Release never blocks: it receives exactly the token this
// acquire sent, and even after SetLLMConcurrency swaps the channel a stale
// release only drains the old channel, which holds that same token.
func acquireLLMSlot(ctx context.Context) func() {
	llmMu.Lock()
	slots, capacity := llmSlots, llmCapacity
	llmMu.Unlock()
	if capacity <= 0 || slots == nil {
		return nil
	}
	select {
	case slots <- struct{}{}:
		return func() { <-slots }
	case <-ctx.Done():
		// All slots busy and the caller gave up while queued: a user stop
		// while queued must not hang, so abandon the wait with no release.
		return nil
	}
}
