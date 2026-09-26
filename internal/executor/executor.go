package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"late/internal/client"
	"late/internal/common"
	"late/internal/pathutil"
	"late/internal/session"
	"late/internal/skill"
	"late/internal/tool"
)

// --- Stream Accumulator ---

// StreamAccumulator collects streaming deltas into coherent content.
// This replaces the duplicated accumulation logic in tui/state.go (GenerationState.Append)
// and agent/agent.go (manual accumulation loop).
type StreamAccumulator struct {
	Content      string
	Reasoning    string
	ToolCalls    []client.ToolCall
	Usage        client.Usage
	FinishReason string
}

// Append merges a single streaming delta into the accumulated state.
func (a *StreamAccumulator) Append(res common.StreamResult) {
	a.Content += res.Content
	a.Reasoning += res.ReasoningContent

	if res.Usage.TotalTokens > 0 {
		a.Usage = res.Usage
	}

	if res.FinishReason != "" {
		a.FinishReason = res.FinishReason
	}

	for _, delta := range res.ToolCalls {
		index := delta.Index
		if index < len(a.ToolCalls) {
			a.ToolCalls[index].Function.Arguments += delta.Function.Arguments
			if delta.Function.Name != "" {
				a.ToolCalls[index].Function.Name = delta.Function.Name
			}
			if delta.ID != "" {
				a.ToolCalls[index].ID = delta.ID
			}
		} else {
			a.ToolCalls = append(a.ToolCalls, delta)
		}
	}
}

// Reset clears all accumulated state.
func (a *StreamAccumulator) Reset() {
	a.Content = ""
	a.Reasoning = ""
	a.ToolCalls = nil
	a.FinishReason = ""
}

// --- Tool Execution ---

// ExecuteToolCalls runs a slice of tool calls against the session.
// It uses the provided middlewares to wrap the base tool execution.
// Results are added to the session history.
func ExecuteToolCalls(ctx context.Context, sess *session.Session, toolCalls []client.ToolCall, middlewares []common.ToolMiddleware) error {
	// Base execution logic
	baseRunner := func(ctx context.Context, tc client.ToolCall) (string, error) {
		t := sess.Registry.Get(tc.Function.Name)
		if t == nil {
			return fmt.Sprintf("Error: tool '%s' not found", tc.Function.Name), nil
		}
		return sess.ExecuteTool(ctx, tc)
	}

	// Wrap with middlewares (in reverse order so first middleware is outermost)
	runner := baseRunner
	for i := len(middlewares) - 1; i >= 0; i-- {
		runner = middlewares[i](common.ToolRunner(runner))
	}

	for _, tc := range toolCalls {
		// Fail-closed: if no confirmation middleware is provided, do not
		// execute shell commands (they must be explicitly approved by a
		// middleware such as the TUI confirm middleware).
		if len(middlewares) == 0 {
			if t := sess.Registry.Get(tc.Function.Name); t != nil {
				if _, ok := t.(*tool.ShellTool); ok {
					result := "shell command requires explicit approval before execution"
					if err := sess.AddToolResultMessage(tc.ID, result); err != nil {
						return err
					}
					continue
				}
			}
		}

		// Each tool call runs in its own cancellable context so a hung tool
		// can be killed individually (via sess.CancelInFlightTool — used by
		// the orchestrator's idle watchdog, or the user) without aborting the
		// whole run. The cancel func is registered on the session for the
		// duration of the call; every iteration derives a FRESH toolCtx from
		// the parent ctx, so a cancelled call never poisons its successors.
		toolCtx, toolCancel := context.WithCancel(ctx)
		sess.SetInFlightToolCancel(toolCancel)
		result, err := runner(toolCtx, tc)
		sess.ClearInFlightToolCancel()
		toolCancel() // the call is done; release the derived context

		if err != nil {
			if ctx.Err() == nil && toolCtx.Err() != nil {
				// The tool call's own context was cancelled while the parent
				// run is still alive: this was an in-flight-tool kill (harness
				// idle watchdog or user), not a stop request. Surface the
				// cancellation as a normal tool result — the model sees it and
				// can recover — and keep processing the remaining calls.
				// (A per-call timeout would land here too, as
				// toolCtx.Err() == context.DeadlineExceeded; none exists on
				// this branch yet — the shell timeout is enforced inside the
				// tool itself and returns a normal result.)
				result = "tool cancelled by the harness idle watchdog"
			} else {
				// Parent cancelled (or a plain tool failure): keep today's
				// behaviour of noting the error; the run loop's subsequent
				// ctx checks unwind the run.
				result = fmt.Sprintf("Error executing tool %s: %v", tc.Function.Name, err)
			}
		}
		if err := sess.AddToolResultMessage(tc.ID, result); err != nil {
			return err
		}
	}
	return nil
}

// --- Tool Registration ---

// RegisterTools registers the common tool set on a session's registry.
// If isPlanning is true, it only registers read-only tools and the planning tool.
// Otherwise, it registers the full set of coding tools.
func RegisterTools(reg *tool.Registry, enabledTools map[string]bool) {
	if enabledTools == nil {
		enabledTools = make(map[string]bool)
	}

	if enabledTools["read_file"] {
		reg.Register(tool.NewReadFileTool())
	}
	if enabledTools["search_content"] || enabledTools["search_tool"] {
		reg.Register(&tool.SearchContentTool{})
	}
	if enabledTools["find_files"] || enabledTools["search_tool"] {
		reg.Register(&tool.FindFilesTool{})
	}
	if enabledTools["bash"] {
		reg.Register(&tool.ShellTool{})
	}
	if enabledTools["write_implementation_plan"] {
		reg.Register(tool.WriteImplementationPlanTool{})
	}
	if enabledTools["write_file"] {
		reg.Register(tool.WriteFileTool{})
	}
	if enabledTools["target_edit"] {
		reg.Register(tool.NewTargetEditTool())
	}

	// Register Todo planning tools (orchestrator-only, not inherited by subagents)
	if enabledTools["create_todos"] || enabledTools["list_todos"] || enabledTools["finish_todo"] {
		var todos []tool.Todo
		var mu sync.Mutex
		if enabledTools["create_todos"] {
			reg.Register(tool.CreateTodosTool{Todos: &todos, Mu: &mu})
		}
		if enabledTools["list_todos"] {
			reg.Register(tool.ListTodosTool{Todos: &todos, Mu: &mu})
		}
		if enabledTools["finish_todo"] {
			reg.Register(tool.FinishTodoTool{Todos: &todos, Mu: &mu})
		}
	}

	// Register Skills
	skillDirs := []string{}
	if userSkillsDir, err := pathutil.LateSkillsDir(); err == nil {
		skillDirs = append(skillDirs, userSkillsDir)
	}
	skillDirs = append(skillDirs, pathutil.LateProjectSkillsDir())

	skills, err := skill.DiscoverSkills(skillDirs)
	if err == nil && len(skills) > 0 {
		skillMap := make(map[string]*skill.Skill)
		for _, s := range skills {
			name := s.ID
			if name == "" {
				name = s.Metadata.Name
			}
			skillMap[name] = s
		}
		reg.Register(tool.ActivateSkillTool{
			Skills: skillMap,
			Reg:    reg,
		})
		reg.Register(tool.SkillReadReferenceTool{
			Skills: skillMap,
		})
	}
}

// --- Consume Stream ---

// ConsumeStream drains a stream channel pair into a StreamAccumulator.
// It calls onChunk (if non-nil) for each delta, enabling real-time UI updates.
// Returns the final accumulated state or an error.
func ConsumeStream(
	ctx context.Context,
	outCh <-chan common.StreamResult,
	errCh <-chan error,
	onChunk func(common.StreamResult),
) (*StreamAccumulator, error) {
	acc := &StreamAccumulator{}

	for res := range outCh {
		acc.Append(res)
		if onChunk != nil {
			onChunk(res)
		}

		// Check for context cancellation (stop request)
		select {
		case <-ctx.Done():
			// Context cancelled - stop streaming but return accumulated data
			return acc, nil
		default:
			// Continue streaming
		}
	}

	// Check for stream error
	select {
	case err, ok := <-errCh:
		if ok && err != nil {
			return acc, fmt.Errorf("stream error: %w", err)
		}
	default:
	}

	return acc, nil
}

// --- Full Run Loop (Blocking) ---

// RunLoop handles the core, blocking event loop for autonomous agents.
// It forces the sequence: inference stream -> verifiable accumulation -> history commit -> safe tool execution.
// If the deterministic tool extraction yields zero calls, the loop securely collapses and returns execution control.
// onRetry fires per failed stream attempt that will be retried; onRecover
// fires exactly once per turn whose retries ended in a successful stream
// (i.e. the retry actually produced a response).

func RunLoop(
	ctx context.Context,
	sess *session.Session,
	maxTurns int,
	extraBody map[string]any,
	onStartTurn func(),
	onEndTurn func(),
	onStreamChunk func(common.StreamResult),
	onRetry func(event common.RetryEvent),
	onRecover func(),
	middlewares []common.ToolMiddleware,
) (string, error) {
	var lastContent string

	// Retry budgets for failing LLM stream calls, resolved once per run.
	// Two independent tiers: infrastructure failures (transport errors,
	// 408/429/5xx) draw from the classic maxRetries budget, while HTTP 400
	// bad-body rejections draw from the much smaller, dedicated badBodyBudget.
	maxRetries := maxStreamRetriesFromContext(ctx)
	badBodyBudget := maxBadBodyRetriesFromContext(ctx)

	// A global disable (--max-stream-retries=0 / negative, or the ctx key)
	// must silence BOTH tiers: the bad-body tier has its own default
	// budget, which would otherwise keep retrying HTTP 400s despite the
	// advertised "retries disabled" contract. An explicit bad-body budget
	// still applies whenever the global budget is positive.
	if maxRetries <= 0 {
		badBodyBudget = 0
	}

	for i := 0; maxTurns <= 0 || i < maxTurns; i++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if onStartTurn != nil {
			onStartTurn()
		}

		// Inner attempt loop around the stream call only: retries never
		// consume a turn (the turn counter above is untouched). Each attempt
		// starts a fresh stream and ConsumeStream builds a fresh accumulator;
		// a failed attempt commits nothing to history. Failures tier into two
		// independent retry budgets: infrastructure failures (transport
		// errors, 408/429/5xx) share the classic maxRetries budget, while
		// HTTP 400 body-parse rejections get their own small dedicated
		// badBodyBudget, because strict OpenAI-compatible gateways often fail
		// transiently while reading the request body. The two budgets use
		// independent counters, so 400 retries never consume infrastructure
		// retry budget and vice versa.
		var acc *StreamAccumulator
		var err error
		infraAttempts, badBodyAttempts := 0, 0
		var recoveryFired bool
		for {
			// Pre-attempt guard (retries only): if the context died while we
			// were waiting in a previous backoff (both select cases below can
			// be ready and the timer may win), do not call StartStream with a
			// dead ctx. Handle it as a cancel, not a new attempt.
			if infraAttempts+badBodyAttempts > 0 && ctx.Err() != nil {
				return "", err
			}

			var onConnect func()
			if (infraAttempts+badBodyAttempts) > 0 && onRecover != nil {
				onConnect = func() {
					if !recoveryFired {
						recoveryFired = true
						onRecover()
					}
				}
			}

			streamCh, errCh := sess.StartStream(ctx, extraBody, onConnect)
			acc, err = ConsumeStream(ctx, streamCh, errCh, onStreamChunk)
			if err == nil {
				break
			}

			// Terminal per tier: budget exhausted for this failure's class or
			// a non-retryable failure. Propagates byte-identically to the
			// pre-retry behavior.
			//
			// Server-requested Retry-After: both retry tiers can carry a
			// *client.StatusError (429/408/5xx in the infra tier, 400 in the
			// bad-body tier), so the error chain is inspected once here and
			// the requested delay — 0 when absent or invalid — is combined
			// with the local jittered backoff below. effectiveRetryDelay
			// guarantees the wait is never shorter than the server asked
			// (capped at retryAfterCeiling) and the existing timer select
			// keeps it cancelable.
			var retryAfter time.Duration
			var se *client.StatusError
			if errors.As(err, &se) {
				retryAfter = se.RetryAfter
			}
			var delay time.Duration
			switch classifyStreamError(err) {
			case retryClassNone:
				// Non-retryable failure, same as before.
				return "", err
			case retryClassInfra:
				if infraAttempts >= maxRetries {
					return "", err
				}
				infraAttempts++
				delay = effectiveRetryDelay(streamRetryDelay(infraAttempts), retryAfter)
				if onRetry != nil {
					onRetry(common.RetryEvent{
						ID:          common.GetOrchestratorID(ctx),
						Attempt:     infraAttempts,
						MaxAttempts: maxRetries,
						// Effective delay: max(local jittered backoff,
						// server-requested Retry-After, capped).
						Delay: delay,
						Err:   err,
					})
				}
			case retryClassBadBody:
				if badBodyAttempts >= badBodyBudget {
					return "", err
				}
				badBodyAttempts++
				delay = effectiveRetryDelay(streamRetryDelay(badBodyAttempts), retryAfter)
				if onRetry != nil {
					onRetry(common.RetryEvent{
						ID:          common.GetOrchestratorID(ctx),
						Attempt:     badBodyAttempts,
						MaxAttempts: badBodyBudget,
						// Effective delay, same combination as the infra tier.
						Delay: delay,
						Err:   err,
					})
				}
			}

			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
				// Backoff elapsed: loop around for a fresh StartStream and a
				// fresh accumulator via ConsumeStream.
			case <-ctx.Done():
				timer.Stop()
				// CANCEL SEMANTICS: a stop during the backoff sleep must land
				// on the same path as a mid-stream cancel (TUI "Stopped", no
				// error box). Returning the underlying stream error is safe
				// because BaseOrchestrator's error branch checks ctx.Err()
				// and routes canceled runs to the stop path instead of
				// emitting StatusEvent{error}.
				return "", err
			}
		}

		// The attempt loop above exits only via break-on-success or an early
		// return, so reaching here means an attempt finally produced a
		// response. If at least one retry happened in this turn, signal
		// recovery exactly once: the turn-start callback fired before the
		// retries, so no thinking event will announce it.
		if (infraAttempts+badBodyAttempts) > 0 && onRecover != nil && !recoveryFired {
			// Signal recovery if not already fired upon connect (e.g. mock session).
			onRecover()
		}

		if acc.FinishReason == "length" {
			// Determine if this is real context exhaustion or just output truncation
			// (e.g. max_tokens cap set on the server side).
			ctxSize := sess.Client().ContextSize()
			isContextExhausted := ctxSize > 0 && acc.Usage.TotalTokens > 0 &&
				float64(acc.Usage.TotalTokens) >= float64(ctxSize)*0.95

			if isContextExhausted {
				return "", fmt.Errorf("exceeds the available context size")
			}

			// Output was truncated but context is not full — save partial
			// response and ask the model to continue more concisely.
			if err := sess.AddAssistantMessageWithTools(acc.Content, acc.Reasoning, nil); err != nil {
				return "", fmt.Errorf("failed to save history: %w", err)
			}
			if err := sess.AddUserMessage("[Output truncated due to length limit. Please continue, but be more concise.]"); err != nil {
				return "", fmt.Errorf("failed to save history: %w", err)
			}
			continue
		}

		// If stopped, the last tool call might be partially streamed and thus invalid JSON.
		// We shouldn't save corrupted tool calls to the session history.
		if ctx.Err() != nil {
			var validCalls []client.ToolCall
			for _, tc := range acc.ToolCalls {
				// A simple check: if the arguments are valid JSON, keeping it is probably safe.
				// Otherwise, it was cut off mid-stream.
				if json.Valid([]byte(tc.Function.Arguments)) {
					validCalls = append(validCalls, tc)
				}
			}
			acc.ToolCalls = validCalls
		}

		if err := sess.AddAssistantMessageWithTools(acc.Content, acc.Reasoning, acc.ToolCalls); err != nil {
			return "", fmt.Errorf("failed to save history: %w", err)
		}

		if onEndTurn != nil {
			onEndTurn()
		}

		if len(acc.ToolCalls) == 0 {
			return acc.Content, nil
		}

		lastContent = acc.Content

		// If a stop was requested, break the loop before executing tools
		select {
		case <-ctx.Done():
			return lastContent + "\n\n(Stopped by user)", nil
		default:
		}

		if err := ExecuteToolCalls(ctx, sess, acc.ToolCalls, middlewares); err != nil {
			return "", err
		}

		// Also check after tool execution in case user requested stop during a long tool
		select {
		case <-ctx.Done():
			return lastContent + "\n\n(Stopped by user)", nil
		default:
		}
	}

	return lastContent + "\n\n(Terminated due to max turns limit)", nil
}
