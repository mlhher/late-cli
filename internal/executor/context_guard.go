package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"late/internal/client"
)

// Context-exhaustion safeguard (Phase B): the executor's programmatic hook
// into session history compaction, mirroring the process-wide registration
// pattern of the other executor hooks.
//
// Detection lives in internal/client (formatError classifies a provider
// response as *client.ContextExceededError when the status AND body name the
// token limit) and in the executor's finish_reason=length discriminator. The
// retry tiers treat the sentinel as fail-fast (stream_retry.go): resending
// the identical request cannot succeed. Recovery instead runs through the
// compactor installed here: RunLoop calls it, and when the run actually
// freed something it retries the request through the normal attempt
// machinery — a failed attempt commits nothing to history, so the retry is
// a clean re-request over the shrunken conversation.
//
// Upstream main has no history-compaction pipeline yet (see the compaction
// PR): nothing installs a compactor, so the safeguard today degrades to
// detection + no-retry-waste + the typed guidance below. The compaction PR
// installs a closure wrapping session.CompactContext here and the recovery
// path lights up unchanged.

// ContextCompactor runs one full-history context compaction pass. It is the
// executor-facing face of session history compaction — the compaction PR's
// main.go installs a closure wrapping the history runner so the executor
// layer never imports the compaction pipeline's scorer or store wiring. The
// returned report tells the caller whether the pass actually freed
// something (TokensSaved > 0 or MessagesCompacted > 0): a report-only
// (shadow-mode) run cannot recover an over-limit request, and the executor
// must know.
type ContextCompactor func(ctx context.Context) (CompactionReport, error)

// CompactionReport summarizes one compaction pass for the guard: it is the
// subset of the compaction PR's session.CompactionReport the executor
// needs. Keeping it executor-local keeps this branch buildable standalone;
// the compaction PR's integration maps its report onto it.
type CompactionReport struct {
	// TokensSaved is the estimated token delta the pass achieved.
	TokensSaved int
	// MessagesCompacted counts messages rewritten by the pass.
	MessagesCompacted int
	// ShadowOnly marks a report-only run: it computed would-save numbers
	// and rewrote nothing, so it cannot recover an over-limit request.
	ShadowOnly bool
}

// ErrCompactionUnavailable is returned by the installed compactor closure
// when recovery is impossible: compaction is off (no pipeline), or the
// pipeline has no scorer. The executor's guard distinguishes it from a
// compaction ATTEMPT that failed mid-walk (those can still have freed
// something — a partially rewritten history stays consistent) so the
// surfaced guidance is accurate.
var ErrCompactionUnavailable = errors.New("context compaction unavailable")

// maxContextCompactionRounds caps the reactive compaction+retry rounds per
// RunLoop invocation (B4): one failed request → compaction → retry is the
// recovery; a second is the safety margin for a provider that reports the
// limit at a level the first pass could not clear (the frozen prefix keeps
// ~25% of history byte-identical, so a first pass may free too little).
// Two compactions that still leave the request over the limit mean the
// window is simply gone: fail with the typed guidance instead of spinning.
const maxContextCompactionRounds = 2

var (
	contextCompactorMu sync.RWMutex
	contextCompactor   ContextCompactor
)

// SetContextCompactor installs c as the process-wide context-exhaustion
// compactor consulted by RunLoop's safeguard. The root agent and every
// subagent share it, mirroring SetToolResultCompactor: a single session
// compactor closure, installed once at startup (cmd/late) with the root
// session's history. Pass nil to disable the safeguard — RunLoop then
// surfaces ErrContextExceeded with its recovery guidance untouched.
func SetContextCompactor(c ContextCompactor) {
	contextCompactorMu.Lock()
	defer contextCompactorMu.Unlock()
	contextCompactor = c
}

// getContextCompactor returns the installed compactor (nil when none).
func getContextCompactor() ContextCompactor {
	contextCompactorMu.RLock()
	defer contextCompactorMu.RUnlock()
	return contextCompactor
}

// contextCompactionOutcome summarizes one guard-mediated compaction run for
// the TUI status surface and the run's durable error log.
type contextCompactionOutcome struct {
	report CompactionReport
	err    error
}

// summary renders the one-line status for the compaction the guard ran:
// the token delta and the message count, plus the failure reason when the
// run did not complete. The wording matches the TUI's compaction status
// style ("saved ~N tokens") so the two surfaces read consistently.
func (o contextCompactionOutcome) summary() string {
	if o.err != nil {
		if errors.Is(o.err, ErrCompactionUnavailable) {
			return "context limit hit — auto-compaction unavailable (compaction-mode off or no scorer)"
		}
		return fmt.Sprintf("context limit hit — auto-compaction failed: %v", o.err)
	}
	if o.report.ShadowOnly {
		// A shadow run reports honestly and rewrites nothing: it cannot
		// recover the request, and the summary must not pretend it did.
		return fmt.Sprintf("context limit hit — compaction is report-only (would save ~%d tokens); enable compaction-mode to apply", o.report.TokensSaved)
	}
	return fmt.Sprintf("context limit hit — auto-compacted (saved ~%d tokens, %d messages compacted)", o.report.TokensSaved, o.report.MessagesCompacted)
}

// freedNothingSummary is the outcome wording for a run that completed but
// released nothing — the give-up branch's "why" line.
const freedNothingSummary = "auto-compaction ran but freed nothing"

// freedSomething reports whether a completed run actually shrank the
// conversation: the executor may only spend a retry on a request that got
// smaller. Shadow-mode reports (would-save numbers) never free anything.
// A zero-token delta with messages compacted still counts — segment
// re-wrapping can trade bytes for identical estimated tokens while
// genuinely restructuring the prompt.
func (o contextCompactionOutcome) freedSomething() bool {
	return o.err == nil && !o.report.ShadowOnly &&
		(o.report.TokensSaved > 0 || o.report.MessagesCompacted > 0)
}

// runContextCompaction invokes the installed compactor. A nil compactor
// yields an outcome carrying ErrCompactionUnavailable, so callers handle
// "no hook installed" and "hook ran but cannot recover" through one path.
// Failures are not logged here: the executor layer has no diagnostics sink
// (raw stderr would paint over the TUI's alt-screen), and the outcome is
// always surfaced to the caller — both the retry status line and the
// terminal error carry the compaction summary verbatim.
func runContextCompaction(ctx context.Context) contextCompactionOutcome {
	c := getContextCompactor()
	if c == nil {
		return contextCompactionOutcome{err: ErrCompactionUnavailable}
	}
	report, err := c(ctx)
	return contextCompactionOutcome{report: report, err: err}
}

// contextExceededGuidance renders the terminal error surfaced when the
// guard cannot recover: the typed guidance (what happened, the manual
// recovery steps) plus the observed compaction outcome so the user knows
// WHY the automatic path gave up.
func contextExceededGuidance(err error, outcome contextCompactionOutcome) error {
	summary := outcome.summary()
	if outcome.err == nil && !outcome.freedSomething() {
		summary = freedNothingSummary
	}
	return fmt.Errorf("%w: %s", err, summary)
}

// isContextExceeded reports whether err is (or wraps) the typed sentinel.
func isContextExceeded(err error) bool {
	return errors.Is(err, client.ErrContextExceeded)
}

// normalizeContextGuidance collapses whitespace runs in a summary so the
// one-line status stays one line.
func normalizeContextGuidance(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
