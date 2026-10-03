package compaction

// Private scoring-side LLM concurrency slot, mirroring the process-wide
// client limiter's contract so compaction/scoring calls stay paced instead
// of stampeding the account-level provider limit in parallel with the
// agent's own traffic. It is intentionally package-local: the shared
// process-wide limiter lives with the LLM client feature; when it lands,
// this slot can delegate to it.
//
// A slot covers the provider-visible request lifetime, not just the
// handshake: for a scoring POST that means from just before the HTTP
// request until the response body is fully consumed. The limiter is active
// from package init with the same default capacity the fleet limiter uses;
// tests adjust it via setScoringConcurrency.

import (
	"context"
	"sync"
)

var (
	// scoringMu guards scoringSlots/scoringCapacity.
	scoringMu sync.Mutex
	// scoringSlots is the counting channel; nil means unlimited.
	scoringSlots chan struct{}
	// scoringCapacity mirrors the channel bound; 0 or negative means
	// unlimited. Default matches the fleet limiter's default cap.
	scoringCapacity = 6
)

// setScoringConcurrency sets the cap on concurrent in-flight scoring
// requests. 0 or negative = unlimited. Tests only.
func setScoringConcurrency(n int) {
	scoringMu.Lock()
	defer scoringMu.Unlock()
	scoringCapacity = n
	if n <= 0 {
		scoringSlots = nil
		return
	}
	scoringSlots = make(chan struct{}, n)
}

// acquireScoringSlot blocks until a slot is free or ctx is done. It returns
// a release func that must be called exactly once when the request finishes;
// the returned func is nil when the limiter is unlimited or when ctx ended
// before a slot could be acquired (callers distinguish the two via
// ctx.Err()). Release never blocks.
func acquireScoringSlot(ctx context.Context) func() {
	scoringMu.Lock()
	slots, capacity := scoringSlots, scoringCapacity
	scoringMu.Unlock()
	if capacity <= 0 || slots == nil {
		return nil
	}
	select {
	case slots <- struct{}{}:
		return func() { <-slots }
	case <-ctx.Done():
		// All slots busy and the caller gave up while queued: abandon the
		// wait with no release.
		return nil
	}
}
