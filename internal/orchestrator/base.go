package orchestrator

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"late/internal/client"
	"late/internal/common"
	"late/internal/executor"
	"late/internal/session"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// BaseOrchestrator implements common.Orchestrator and manages an agent's run loop.
type BaseOrchestrator struct {
	id          string
	sess        *session.Session
	middlewares []common.ToolMiddleware
	eventCh     chan common.Event

	mu       sync.RWMutex
	parent   common.Orchestrator
	children []common.Orchestrator

	// childSeq is a monotonic counter for minting child IDs; guarded by mu
	childSeq int

	// Running state tracker
	isRunning   bool
	pendingMsgs []client.ChatMessage
	acc         executor.StreamAccumulator
	ctx         context.Context
	cancel      context.CancelFunc

	// Stop mechanism
	stopCh chan struct{}

	// Max turns configuration
	maxTurns int

	// Activity tracking for the idle watchdog. lastActivity holds unix-nano
	// of the last observed sign of life (stream chunk, tool execution,
	// nested-spawn heartbeat); inFlightTools and nestedSpawns count
	// outstanding tool executions and nested subagent runs. All three are
	// atomic; idleNotified enforces the once-per-idle-episode notification.
	// oldestToolStartAt holds the unix-nano start time of the oldest
	// in-flight tool (0 when none) so the busy check can tell a progressing
	// tool (younger than the idle threshold) from one that has stalled past
	// it — a stalled tool must not mask idleness forever, or the watchdog
	// could never kill the hung tool and rescue the agent.
	lastActivity      atomic.Int64
	inFlightTools     atomic.Int64
	oldestToolStartAt atomic.Int64
	nestedSpawns      atomic.Int64
	idleNotified      atomic.Bool

	// toolKillDone records that the idle watchdog already used its first
	// escalation stage for this run (killed the hung in-flight tool), so the
	// next sustained-idle tick goes straight to the agent-level kill.
	// Once-per-stage semantics: stage 1 fires at most once per run, stage 2
	// (the agent kill) at most once because cancel() readies ctx.Done and
	// stops the watchdog.
	toolKillDone atomic.Bool

	// Idle-watchdog policy, guarded by mu and set via SetIdlePolicy before a
	// run starts. idleTimeout <= 0 disables the watchdog; idleKillAfter <= 0
	// means notify only. idleTickInterval defaults to defaultIdleTickInterval
	// and is overridable (same package) so tests can drive the watchdog
	// deterministically.
	idleTimeout      time.Duration
	idleKillAfter    time.Duration
	idleTickInterval time.Duration

	// idleKillReason records why the idle watchdog cancelled this run; guarded
	// by mu. Empty unless the watchdog killed the run.
	idleKillReason string
}

// Idle-watchdog tuning. defaultIdleTickInterval is how often the watchdog
// re-checks for idleness; idleProbeLines is how many transcript entries are
// rendered into a SubagentIdleEvent probe; idleProbeLineMaxLen caps each
// rendered probe line.
const (
	defaultIdleTickInterval = 30 * time.Second
	idleProbeLines          = 3
	idleProbeLineMaxLen     = 160
)

func NewBaseOrchestrator(id string, sess *session.Session, middlewares []common.ToolMiddleware, maxTurns int) *BaseOrchestrator {
	childSeq := 0
	if sess != nil {
		childSeq = sess.SubagentSeq()
	}
	o := &BaseOrchestrator{
		id:          id,
		sess:        sess,
		middlewares: middlewares,
		eventCh:     make(chan common.Event, 100),
		ctx:         context.Background(),
		stopCh:      make(chan struct{}),
		maxTurns:    maxTurns,
		childSeq:    childSeq,
	}
	// Construction counts as activity so an immediate run starts with a
	// fresh idle episode.
	o.lastActivity.Store(time.Now().UnixNano())
	return o
}

func (o *BaseOrchestrator) SetMiddlewares(middlewares []common.ToolMiddleware) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.middlewares = middlewares
}

func (o *BaseOrchestrator) SetContext(ctx context.Context) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ctx = ctx
}

// resetContextIfCancelled makes a completed run context usable again without
// discarding configuration values attached to it (for example, the
// unsupervised-execution flag and TUI input provider).
func (o *BaseOrchestrator) resetContextIfCancelled() {
	if o.ctx.Err() != nil {
		o.ctx = context.WithoutCancel(o.ctx)
	}
}

func (o *BaseOrchestrator) SetMaxTurns(maxTurns int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.maxTurns = maxTurns
}

// SetIdlePolicy configures the idle watchdog for this orchestrator: idle is
// the "truly idle" threshold (0 = watchdog off) and killAfter is the
// sustained-idle point at which the orchestrator cancels its own run
// (0 = notify only). Must be called before Execute/Submit to apply to a run.
func (o *BaseOrchestrator) SetIdlePolicy(idle, killAfter time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.idleTimeout = idle
	o.idleKillAfter = killAfter
}

// MarkActivity records a sign of life — a streamed chunk, a tool execution,
// or a nested-spawn heartbeat — and re-arms the idle notification so a new
// idle episode can be reported after activity resumes. It implements
// common.ActivityMarker.
func (o *BaseOrchestrator) MarkActivity() {
	o.lastActivity.Store(time.Now().UnixNano())
	o.idleNotified.Store(false)
}

// BeginNestedSpawn marks a nested subagent run as in flight so the idle
// watchdog treats this orchestrator as busy for the child's duration.
func (o *BaseOrchestrator) BeginNestedSpawn() { o.nestedSpawns.Add(1) }

// EndNestedSpawn marks a previously begun nested subagent run as finished.
func (o *BaseOrchestrator) EndNestedSpawn() { o.nestedSpawns.Add(-1) }

// IdleKillReason returns the recorded kill reason when the idle watchdog
// self-cancelled this run, or "" otherwise.
func (o *BaseOrchestrator) IdleKillReason() string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.idleKillReason
}

// activityMiddleware is the internal, always-present outermost middleware:
// every tool execution bumps activity and the in-flight tool counter, and
// stamps the in-flight start time used by the idle watchdog's busy check. It
// is inserted OUTERMOST (before user middlewares), so the confirmation
// middleware runs INSIDE this wrapper — a tool waiting for user approval
// still counts as in-flight, i.e. awaiting-approval counts as active. That
// is by design: a paused-for-approval agent is not idle.
func (o *BaseOrchestrator) activityMiddleware() common.ToolMiddleware {
	return func(next common.ToolRunner) common.ToolRunner {
		return func(ctx context.Context, tc client.ToolCall) (string, error) {
			o.MarkActivity()
			o.beginToolInFlight()
			defer o.endToolInFlight()
			return next(ctx, tc)
		}
	}
}

// beginToolInFlight records the start of a tool execution. With concurrent
// tools it keeps the OLDEST start time — the busy check only needs the
// longest-running call — and the stamp is cleared again once no tool is
// in flight.
func (o *BaseOrchestrator) beginToolInFlight() {
	o.inFlightTools.Add(1)
	now := time.Now().UnixNano()
	for {
		prev := o.oldestToolStartAt.Load()
		if prev != 0 && prev <= now {
			return
		}
		if o.oldestToolStartAt.CompareAndSwap(prev, now) {
			return
		}
	}
}

// endToolInFlight marks a tool execution as finished and clears the oldest
// in-flight timestamp when the in-flight count drops back to zero.
func (o *BaseOrchestrator) endToolInFlight() {
	if o.inFlightTools.Add(-1) == 0 {
		o.oldestToolStartAt.Store(0)
	}
}

// withActivityMiddleware returns the middleware chain passed to RunLoop with
// the internal activity middleware prepended as the outermost layer. The
// chain is rebuilt fresh so the stored o.middlewares slice is never mutated.
func (o *BaseOrchestrator) withActivityMiddleware() []common.ToolMiddleware {
	chain := make([]common.ToolMiddleware, 0, len(o.middlewares)+1)
	chain = append(chain, o.activityMiddleware())
	chain = append(chain, o.middlewares...)
	return chain
}

// startIdleWatchdog launches the idle watchdog for one run; it stops when the
// run context is cancelled. On every tick it checks whether the orchestrator
// has been truly idle — no stream progress, no in-flight nested spawn, and no
// tool call younger than the idle threshold — for longer than the configured
// idle threshold. An in-flight tool counts as progress only while it is
// YOUNGER than the idle threshold: a tool stuck for longer than that stops
// masking idleness, which is what makes the tool-first kill (stage 1 of the
// escalation below) reachable for hung tools. The first qualifying tick emits
// one SubagentIdleEvent (once per idle episode; MarkActivity re-arms). When a
// kill threshold is configured, the kill escalates in two stages (see the
// kill branch below for why that check is not gated on the once-per-episode
// flag).
func (o *BaseOrchestrator) startIdleWatchdog(ctx context.Context, cancel context.CancelFunc) {
	o.mu.RLock()
	idleTimeout := o.idleTimeout
	idleKillAfter := o.idleKillAfter
	tickInterval := o.idleTickInterval
	o.mu.RUnlock()

	if idleTimeout <= 0 {
		return
	}
	if tickInterval <= 0 {
		tickInterval = defaultIdleTickInterval
	}

	go func() {
		ticker := time.NewTicker(tickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}

			idle := time.Since(time.Unix(0, o.lastActivity.Load()))
			// Busy means real progress is still possible: a nested subagent
			// run, or a tool call in flight for LESS than the idle threshold.
			// A tool that outlives idleTimeout is considered stalled — it no
			// longer suppresses idleness, so a hung tool cannot hide from the
			// watchdog forever.
			busy := o.nestedSpawns.Load() > 0
			if start := o.oldestToolStartAt.Load(); start != 0 &&
				time.Since(time.Unix(0, start)) < idleTimeout {
				busy = true
			}
			if idle < idleTimeout || busy {
				continue
			}

			// Once per idle episode: Swap(false→true) fires only on the
			// first qualifying tick after the last MarkActivity.
			if !o.idleNotified.Swap(true) {
				select {
				case o.eventCh <- common.SubagentIdleEvent{ID: o.id, IdleFor: idle, Probe: o.idleProbe()}:
				default:
				}
			}

			// Two-stage sustained-idle kill. Like the notification above, the
			// threshold check runs on EVERY qualifying tick: the kill
			// threshold normally sits well past the notify threshold (e.g.
			// notify at 15m, kill at 30m), and gating it on the
			// once-per-episode flag would swallow it — the notify tick fires
			// long before idle reaches the kill threshold.
			//
			// Stage 1 (first qualifying tick that finds a stalled tool in
			// flight): kill the TOOL, not the agent. executor.ExecuteToolCalls
			// registers each call's cancellable context on the session, so
			// CancelInFlightTool makes the hung call return a "tool cancelled"
			// result and the run continues — the model sees the cancellation
			// and can recover. The watchdog then re-checks on the NEXT tick:
			// if activity resumed (next tool call, streamed chunk), nothing
			// else happens; if idleness persists past the kill threshold,
			// stage 2 fires. Once-per-stage: toolKillDone gates stage 1 to a
			// single shot per run.
			//
			// Stage 2 (next qualifying tick, or the same one when no tool was
			// in flight to kill): cancel our own run context — the
			// orchestrator probed the transcript and decided this agent is
			// stuck. cancel() readies ctx.Done, so stage 2 records and
			// cancels at most once.
			if idleKillAfter > 0 && idle >= idleKillAfter {
				if start := o.oldestToolStartAt.Load(); start != 0 && !o.toolKillDone.Load() {
					if o.sess != nil && o.sess.CancelInFlightTool() {
						// Stage 1 done. Skip the agent kill this tick and
						// re-evaluate on the next one.
						o.toolKillDone.Store(true)
						continue
					}
					// The tool finished between the busy check and the kill
					// attempt — nothing left to cancel; fall through to
					// stage 2.
				}

				// Record the probe lines in the kill reason for the crash
				// classification, noting an earlier tool kill that failed to
				// rescue the run.
				probe := o.idleProbe()
				reason := fmt.Sprintf(
					"idle for %s (kill threshold %s); last transcript entries: %s",
					idle.Truncate(time.Second), idleKillAfter, strings.Join(probe, " | "),
				)
				if o.toolKillDone.Load() {
					reason = "in-flight tool cancelled; " + reason
				}
				o.mu.Lock()
				o.idleKillReason = reason
				o.mu.Unlock()
				if cancel != nil {
					cancel()
				}
			}
		}
	}()
}

// idleProbe renders the current transcript tail for idle events and kill
// reasons. Best-effort: history is read without locking, matching
// BaseOrchestrator.History(); the run loop may append concurrently.
func (o *BaseOrchestrator) idleProbe() []string {
	if o.sess == nil {
		return nil
	}
	return lastTranscriptLines(o.sess.History, idleProbeLines)
}

// lastTranscriptLines renders the last n history entries as short single-line
// strings ("role: first line") — the transcript probe carried by idle events
// so the recipient can judge whether the agent is stuck. Assistant messages
// that only contain tool calls fall back to the tool names.
func lastTranscriptLines(msgs []client.ChatMessage, n int) []string {
	if n <= 0 || len(msgs) == 0 {
		return nil
	}
	start := len(msgs) - n
	if start < 0 {
		start = 0
	}
	lines := make([]string, 0, len(msgs)-start)
	for _, msg := range msgs[start:] {
		summary := firstLine(strings.TrimSpace(msg.Content.String()))
		if summary == "" && len(msg.ToolCalls) > 0 {
			names := make([]string, 0, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				names = append(names, tc.Function.Name)
			}
			summary = "called " + strings.Join(names, ", ")
		}
		if summary == "" {
			continue
		}
		lines = append(lines, truncateRunes(fmt.Sprintf("%s: %s", msg.Role, summary), idleProbeLineMaxLen))
	}
	return lines
}

// firstLine returns s up to the first newline.
func firstLine(s string) string {
	if idx := strings.IndexAny(s, "\r\n"); idx >= 0 {
		return s[:idx]
	}
	return s
}

// truncateRunes shortens s to max runes, appending "..." when truncated.
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}

func (o *BaseOrchestrator) MaxTokens() int {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.sess.Client().ContextSize()
}

func (o *BaseOrchestrator) SupportsVision() bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.sess.Client().SupportsVision()
}

func (o *BaseOrchestrator) RefreshContextSize(ctx context.Context) {
	o.sess.Client().RefreshContextSize(ctx)
}

func (o *BaseOrchestrator) QueuedMessages() []string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	var msgs []string
	for _, m := range o.pendingMsgs {
		text := m.Content.UIString()
		if text == "" {
			text = m.Content.String()
		}
		msgs = append(msgs, text)
	}
	return msgs
}

func (o *BaseOrchestrator) DrainQueuedMessages() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.pendingMsgs) == 0 {
		return nil
	}
	msgs := make([]string, 0, len(o.pendingMsgs))
	for _, m := range o.pendingMsgs {
		text := m.Content.UIString()
		if text == "" {
			text = m.Content.String()
		}
		msgs = append(msgs, text)
	}
	o.pendingMsgs = nil
	return msgs
}

func (o *BaseOrchestrator) ID() string { return o.id }

func (o *BaseOrchestrator) Submit(text string, images []string) error {
	msg := client.ChatMessage{Role: "user", AttachedFiles: images}

	if len(images) == 0 {
		msg.Content = client.TextContent(text)
	} else {
		parts := []client.ContentPart{
			{Type: client.ContentPartText, Text: text},
		}
		supportsVision := o.SupportsVision()
		for _, imgPath := range images {
			data, err := os.ReadFile(imgPath)
			if err != nil {
				return fmt.Errorf("failed to read file %s: %w", imgPath, err)
			}

			mimeType := http.DetectContentType(data)
			// Only attach to LLM content if it's an image AND the model supports vision
			if strings.HasPrefix(mimeType, "image/") && supportsVision {
				encoded := base64.StdEncoding.EncodeToString(data)
				parts = append(parts, client.ContentPart{
					Type: client.ContentPartImageURL,
					ImageURL: &client.ImageURL{
						URL: fmt.Sprintf("data:%s;base64,%s", mimeType, encoded),
					},
				})
			} else {
				// Treat as text if it looks like text or has a common extension.
				// If it's an image but the model doesn't support vision, just include a note.
				content := ""
				isAttachment := true
				if strings.HasPrefix(mimeType, "image/") {
					content = fmt.Sprintf("\nAttached Image: %s (Vision not supported by current model)\n", filepath.Base(imgPath))
				} else {
					content = fmt.Sprintf("\nFilename: %s\nContent: ```\n%s\n```\n", filepath.Base(imgPath), string(data))
				}

				parts = append(parts, client.ContentPart{
					Type:         client.ContentPartText,
					Text:         content,
					IsAttachment: isAttachment,
				})
			}
		}
		msg.Content = client.MessageContent{Parts: parts}
	}

	o.mu.Lock()
	if o.isRunning {
		o.pendingMsgs = append(o.pendingMsgs, msg)
		o.mu.Unlock()
		o.eventCh <- common.MessageQueuedEvent{ID: o.id, Text: text}
		return nil
	}

	o.isRunning = true
	// Clear any old cancellation state so a new run isn't instantly aborted
	o.cancel = nil
	// Reset the base context if it was already cancelled.
	o.resetContextIfCancelled()
	o.mu.Unlock()

	if err := o.sess.AddMessage(msg); err != nil {
		o.mu.Lock()
		o.isRunning = false
		o.mu.Unlock()
		return err
	}

	o.eventCh <- common.StatusEvent{ID: o.id, Status: "thinking"}
	// Start the run loop in a background goroutine
	go o.run()
	return nil
}

func (o *BaseOrchestrator) Execute(text string) (string, error) {
	o.mu.Lock()
	if o.isRunning {
		o.mu.Unlock()
		return "", fmt.Errorf("orchestrator is already running")
	}
	o.isRunning = true
	o.resetContextIfCancelled()
	ctx, cancel := context.WithCancel(o.ctx)
	o.cancel = cancel
	o.ctx = ctx // Set the Context for this execution
	o.mu.Unlock()

	defer cancel()

	// Fresh idle episode for this run: construction or the previous run's
	// last activity must not leak into the watchdog's baseline. The watchdog
	// stops when the deferred cancel above fires.
	o.MarkActivity()
	o.startIdleWatchdog(ctx, cancel)

	// Inject orchestrator ID into context for tool interactions
	ctx = context.WithValue(ctx, common.OrchestratorIDKey, o.id)

	if strings.TrimSpace(text) != "" {
		if err := o.sess.AddUserMessage(text); err != nil {
			return "", err
		}
	}

	o.eventCh <- common.StatusEvent{ID: o.id, Status: "thinking"}
	defer func() {
		o.mu.Lock()
		o.isRunning = false
		o.pendingMsgs = nil
		o.mu.Unlock()
		o.eventCh <- common.StatusEvent{ID: o.id, Status: "idle"}
	}()

	// Build extra body
	var extraBody map[string]any

	onStartTurn := func() {
		if ctx.Err() != nil {
			return
		}
		o.RefreshContextSize(ctx)
		o.mu.Lock()
		msgs := o.pendingMsgs
		o.pendingMsgs = nil
		o.acc.Reset()
		o.mu.Unlock()

		for _, msg := range msgs {
			_ = o.sess.AddMessage(msg)
		}

		o.eventCh <- common.StatusEvent{ID: o.id, Status: "thinking"}
	}

	onEndTurn := func() {
		o.RefreshContextSize(ctx)
		o.mu.Lock()
		usage := o.acc.Usage
		o.acc.Reset()
		o.mu.Unlock()
		o.eventCh <- common.ContentEvent{ID: o.id, Usage: usage, Completed: true}
	}

	res, err := executor.RunLoop(
		ctx,
		o.sess,
		o.maxTurns,
		extraBody,
		onStartTurn,
		onEndTurn,
		func(res common.StreamResult) {
			// Stream progress counts as activity for the idle watchdog.
			o.MarkActivity()
			o.mu.Lock()
			o.acc.Append(res)
			accCopy := o.acc
			o.mu.Unlock()

			o.eventCh <- common.ContentEvent{
				ID:               o.id,
				Content:          accCopy.Content,
				ReasoningContent: accCopy.Reasoning,
				ToolCalls:        accCopy.ToolCalls,
				Usage:            accCopy.Usage,
			}
		},
		func(ev common.RetryEvent) {
			// The retry starts a fresh stream; reset the shared accumulator so
			// the failed attempt's partial deltas do not prefix the retry's
			// output in the TUI (the executor's local accumulator is already
			// per-attempt; this one is per-turn).
			o.mu.Lock()
			o.acc.Reset()
			o.mu.Unlock()

			ev.ID = o.id // Route to this agent's AppState even if ctx lost the ID
			// Non-blocking emit: a slow or stalled TUI must never delay the
			// retry backoff loop. The buffered(100) eventCh may be full if the
			// consumer lags; dropping a retry notice is acceptable, blocking
			// the agent is not.
			select {
			case o.eventCh <- ev:
			default:
			}
		},
		func() {
			// A retried attempt produced a response: emit the dedicated
			// recovery event so the UI can toast immediately instead of
			// guessing on the next turn's thinking event. Non-blocking:
			// a dropped recovery notice is acceptable, blocking the
			// agent is not.
			select {
			case o.eventCh <- common.RecoveryEvent{ID: o.id}:
			default:
			}
		},
		o.withActivityMiddleware(),
	)

	if err != nil {
		// Canceled runs follow the stop path, not the error path (no error
		// box): a stop can surface here as the underlying stream error, e.g.
		// when the retry backoff sleep is interrupted by ctx.Done(). We check
		// ctx.Err() instead of IsStopRequested() because IsStopRequested()
		// consumes the one-shot stopCh token. Emitting "closed" mirrors how a
		// mid-stream cancel (nil error) is routed below.
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
			o.eventCh <- common.StatusEvent{ID: o.id, Status: "closed"}
			return res, err
		}
		o.eventCh <- common.StatusEvent{ID: o.id, Status: "error", Error: err}
	} else {
		o.eventCh <- common.StatusEvent{ID: o.id, Status: "closed"}
	}
	return res, err
}

func (o *BaseOrchestrator) run() {
	o.mu.Lock()
	o.resetContextIfCancelled()
	ctx, cancel := context.WithCancel(o.ctx)
	o.cancel = cancel
	o.ctx = ctx // Set the context so Execute/RunLoop can share the cancelable context safely
	o.mu.Unlock()

	defer cancel() // Ensure we don't leak the context when run() finishes
	defer func() {
		o.mu.Lock()
		o.isRunning = false
		o.pendingMsgs = nil
		o.mu.Unlock()
	}()

	// Fresh idle episode for this run; the watchdog stops when the deferred
	// cancel above fires.
	o.MarkActivity()
	o.startIdleWatchdog(ctx, cancel)

	// Inject orchestrator ID into context for tool interactions
	ctx = context.WithValue(ctx, common.OrchestratorIDKey, o.id)

	for {
		if ctx.Err() != nil {
			o.eventCh <- common.StatusEvent{ID: o.id, Status: "idle"}
			break
		}

		onStartTurn := func() {
			if ctx.Err() != nil {
				return
			}
			o.RefreshContextSize(ctx)
			o.mu.Lock()
			msgs := o.pendingMsgs
			o.pendingMsgs = nil
			o.acc.Reset()
			o.mu.Unlock()

			for _, msg := range msgs {
				_ = o.sess.AddMessage(msg)
			}

			o.eventCh <- common.StatusEvent{ID: o.id, Status: "thinking"}
		}

		onEndTurn := func() {
			o.RefreshContextSize(ctx)
			o.mu.Lock()
			usage := o.acc.Usage
			o.acc.Reset()
			o.mu.Unlock()
			o.eventCh <- common.ContentEvent{ID: o.id, Usage: usage, Completed: true}
		}

		// Build extra body
		var extraBody map[string]any

		_, err := executor.RunLoop(
			ctx,
			o.sess,
			o.maxTurns,
			extraBody,
			onStartTurn,
			onEndTurn,
			func(res common.StreamResult) {
				// Stream progress counts as activity for the idle watchdog.
				o.MarkActivity()
				o.mu.Lock()
				o.acc.Append(res)
				accCopy := o.acc // Copy for event
				o.mu.Unlock()

				o.eventCh <- common.ContentEvent{
					ID:               o.id,
					Content:          accCopy.Content,
					ReasoningContent: accCopy.Reasoning,
					ToolCalls:        accCopy.ToolCalls,
					Usage:            accCopy.Usage,
				}
			},
			func(ev common.RetryEvent) {
				// The retry starts a fresh stream; reset the shared accumulator
				// so the failed attempt's partial deltas do not prefix the
				// retry's output in the TUI (the executor's local accumulator
				// is already per-attempt; this one is per-turn).
				o.mu.Lock()
				o.acc.Reset()
				o.mu.Unlock()

				ev.ID = o.id // Route to this agent's AppState even if ctx lost the ID
				// Non-blocking emit: a slow or stalled TUI must never delay the
				// retry backoff loop. The buffered(100) eventCh may be full if
				// the consumer lags; dropping a retry notice is acceptable,
				// blocking the agent is not.
				select {
				case o.eventCh <- ev:
				default:
				}
			},
			func() {
				// A retried attempt produced a response: emit the dedicated
				// recovery event so the UI can toast immediately instead of
				// guessing on the next turn's thinking event. Non-blocking:
				// a dropped recovery notice is acceptable, blocking the
				// agent is not.
				select {
				case o.eventCh <- common.RecoveryEvent{ID: o.id}:
				default:
				}
			},
			o.withActivityMiddleware(),
		)

		// Reset accumulator after finished or ready for next turn
		o.mu.Lock()
		o.acc.Reset()
		hasPending := len(o.pendingMsgs) > 0
		if !hasPending {
			o.isRunning = false
		}
		o.mu.Unlock()

		if err != nil {
			// Canceled runs follow the stop path, not the error path (no error
			// box): a stop can surface here as the underlying stream error,
			// e.g. when the retry backoff sleep is interrupted by ctx.Done().
			// We check ctx.Err() instead of IsStopRequested() because
			// IsStopRequested() consumes the one-shot stopCh token that the
			// StopRequestedEvent emission at the end of run() depends on.
			// Emitting "idle" mirrors how a mid-stream cancel (nil error) is
			// routed, so the TUI resolves out of its "Stopping..." state; if
			// the stopCh token landed, the StopRequestedEvent below still
			// turns it into "Stopped".
			if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
				o.eventCh <- common.StatusEvent{ID: o.id, Status: "idle"}
				break
			}
			// If the error is about unsupported image input, roll back the user message
			// so it doesn't poison the context for future requests.
			errStr := err.Error()
			if strings.Contains(errStr, "image input is not supported") ||
				strings.Contains(errStr, "image_input") ||
				strings.Contains(errStr, "does not support image") {
				// Remove the last user message from history
				if len(o.sess.History) > 0 && o.sess.History[len(o.sess.History)-1].Role == "user" {
					o.sess.History = o.sess.History[:len(o.sess.History)-1]
				}
				o.eventCh <- common.StatusEvent{ID: o.id, Status: "error", Error: fmt.Errorf("image_unsupported")}
			} else if isBadRequestStatusError(err) {
				// The API rejected the request body even after the executor's bad-body
				// retries. Roll the turn back so the session returns to its pre-submit
				// state: the user can edit and resend instead of every retry rebuilding
				// the same rejected request. Persisted via PopLastUserMessage (unlike
				// the image rollback above, this must survive a restart).
				var se *client.StatusError
				if errors.As(err, &se) {
					rolled, saveErr := o.sess.PopLastUserMessage()
					msg := fmt.Sprintf("API rejected the request (400) after retries: %s — ", se.Body)
					switch {
					case rolled && saveErr == nil:
						msg += "your last message was rolled back; edit it and resend"
					case rolled:
						msg += "your last message was rolled back in memory, but saving the rollback to disk failed"
					default:
						msg += "nothing was rolled back (the turn had no unanswered user message); use /rewind if history needs repair"
					}
					o.eventCh <- common.StatusEvent{ID: o.id, Status: "error", Error: errors.New(msg)}
				}
			} else {
				o.eventCh <- common.StatusEvent{ID: o.id, Status: "error", Error: err}
			}
			break
		}

		if !hasPending {
			o.eventCh <- common.StatusEvent{ID: o.id, Status: "idle"}
			break
		}
	}

	// Check if stop was requested and send StopRequestedEvent
	if o.IsStopRequested() {
		o.eventCh <- common.StopRequestedEvent{ID: o.id}
	}
}

// isBadRequestStatusError reports whether err carries an HTTP 400 from the
// LLM API, even through the executor's "stream error: ..." wrapping.
func isBadRequestStatusError(err error) bool {
	var se *client.StatusError
	return errors.As(err, &se) && se.StatusCode == http.StatusBadRequest
}

func (o *BaseOrchestrator) Events() <-chan common.Event {
	return o.eventCh
}

func (o *BaseOrchestrator) Cancel() {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.pendingMsgs = nil

	if o.cancel != nil {
		o.cancel()
	}

	select {
	case o.stopCh <- struct{}{}:
		// Signal sent
	default:
		// Already signaled, ignore
	}
}

func (o *BaseOrchestrator) IsStopRequested() bool {
	select {
	case <-o.stopCh:
		return true
	default:
		return false
	}
}

func (o *BaseOrchestrator) History() []client.ChatMessage {
	return o.sess.History
}

func (o *BaseOrchestrator) Session() *session.Session {
	return o.sess
}

func (o *BaseOrchestrator) SystemPrompt() string {
	return o.sess.SystemPrompt()
}

func (o *BaseOrchestrator) ToolDefinitions() []client.ToolDefinition {
	return o.sess.GetToolDefinitions()
}

func (o *BaseOrchestrator) Context() context.Context {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.ctx
}

func (o *BaseOrchestrator) Middlewares() []common.ToolMiddleware {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.middlewares
}

func (o *BaseOrchestrator) Registry() *common.ToolRegistry {
	return o.sess.Registry
}

func (o *BaseOrchestrator) Children() []common.Orchestrator {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make([]common.Orchestrator, len(o.children))
	copy(out, o.children)
	return out
}

func (o *BaseOrchestrator) Parent() common.Orchestrator {
	return o.parent
}

func (o *BaseOrchestrator) Reset() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.sess.StartNewConversation(); err != nil {
		return err
	}
	for _, registeredTool := range o.sess.Registry.All() {
		if resetter, ok := registeredTool.(common.ConversationResetter); ok {
			resetter.ResetConversationState()
		}
	}
	return nil
}

func (o *BaseOrchestrator) Rewind(index int) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if index < 0 || index >= len(o.sess.History) {
		return fmt.Errorf("invalid history index")
	}
	o.sess.History = o.sess.History[:index]
	if o.sess.HistoryPath != "" {
		if err := session.SaveHistory(o.sess.HistoryPath, o.sess.History); err != nil {
			return err
		}
		return o.sess.UpdateSessionMetadata()
	}
	return nil
}

// NextChildID atomically reserves and mints the next child ID under o.mu. The
// counter is shared across agent types (e.g., `researcher-subagent-0`, then
// `coder-subagent-1`), matching the legacy `len(children)` numbering scheme.
func (o *BaseOrchestrator) NextChildID(agentType string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	id := fmt.Sprintf("%s-subagent-%d", agentType, o.childSeq)
	if err := o.sess.UpdateSubagentSeq(o.childSeq + 1); err != nil {
		return "", fmt.Errorf("failed to reserve child ID: %w", err)
	}
	o.childSeq++
	return id, nil
}

func (o *BaseOrchestrator) AddChild(child common.Orchestrator) {
	o.mu.Lock()
	o.children = append(o.children, child)
	o.mu.Unlock()

	o.eventCh <- common.ChildAddedEvent{
		ParentID: o.id,
		Child:    child,
	}
}
