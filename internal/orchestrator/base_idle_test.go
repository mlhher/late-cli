package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"late/internal/client"
	"late/internal/common"
	"late/internal/executor"
	"late/internal/session"
)

// The idle watchdog is driven deterministically by injecting a tiny idle
// threshold and tick interval (both unexported fields, same package). The
// production defaults — a 30s tick and a threshold supplied by the
// --subagent-idle-timeout flag (15m, 0 = off) — are untouched.

// newIdleTestOrchestrator returns an orchestrator with the given idle policy
// and tick interval, seeded with the provided history. lastActivity is
// stamped at construction, matching NewBaseOrchestrator.
func newIdleTestOrchestrator(t *testing.T, idle, killAfter, tick time.Duration, history []client.ChatMessage) *BaseOrchestrator {
	t.Helper()
	sess := session.New(nil, "", history, "", false)
	o := NewBaseOrchestrator("idle-test", sess, nil, 10)
	o.SetIdlePolicy(idle, killAfter)
	o.idleTickInterval = tick
	return o
}

// collectIdleEvents drains the orchestrator's event channel for the given
// duration and returns every SubagentIdleEvent received in that window.
func collectIdleEvents(t *testing.T, o *BaseOrchestrator, wait time.Duration) []common.SubagentIdleEvent {
	t.Helper()
	var events []common.SubagentIdleEvent
	deadline := time.After(wait)
	for {
		select {
		case ev := <-o.eventCh:
			if idle, ok := ev.(common.SubagentIdleEvent); ok {
				events = append(events, idle)
			}
		case <-deadline:
			return events
		}
	}
}

// waitFor polls cond until it holds or the timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not reached within timeout")
}

// TestIdleWatchdogFiresAfterSilence verifies the watchdog emits exactly one
// SubagentIdleEvent per idle episode: one event once the silence passes the
// threshold, no duplicates while the silence persists, no event right after
// activity resumes, and a fresh event once the agent goes idle again. The
// probe must carry the last transcript entries only.
func TestIdleWatchdogFiresAfterSilence(t *testing.T) {
	history := []client.ChatMessage{
		{Role: "user", Content: client.TextContent("first task")},
		{Role: "assistant", Content: client.TextContent("working on it")},
		{Role: "assistant", Content: client.TextContent("thinking deeply\nabout options")},
		{Role: "user", Content: client.TextContent("continue")},
	}
	o := newIdleTestOrchestrator(t, 60*time.Millisecond, 0, 10*time.Millisecond, history)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o.startIdleWatchdog(ctx, cancel)

	// First idle episode: exactly one event once the threshold passes.
	events := collectIdleEvents(t, o, 250*time.Millisecond)
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 idle event per episode, got %d", len(events))
	}
	if events[0].ID != o.id {
		t.Errorf("event ID = %q, want %q", events[0].ID, o.id)
	}
	if events[0].IdleFor <= 0 {
		t.Errorf("IdleFor = %v, want > 0", events[0].IdleFor)
	}

	// Probe: the last 3 history entries, rendered as "role: first line".
	wantProbe := []string{
		"assistant: working on it",
		"assistant: thinking deeply", // first line only
		"user: continue",
	}
	if len(events[0].Probe) != len(wantProbe) {
		t.Fatalf("probe length = %d (%v), want %d", len(events[0].Probe), events[0].Probe, len(wantProbe))
	}
	for i, want := range wantProbe {
		if events[0].Probe[i] != want {
			t.Errorf("probe[%d] = %q, want %q", i, events[0].Probe[i], want)
		}
	}
	if strings.Contains(strings.Join(events[0].Probe, "\n"), "first task") {
		t.Errorf("probe must only contain the last entries, got %v", events[0].Probe)
	}

	// Idle persists: once-per-episode means no further events.
	if again := collectIdleEvents(t, o, 60*time.Millisecond); len(again) != 0 {
		t.Fatalf("idle event re-emitted while the episode persisted: %v", again)
	}

	// Re-arm: activity resets the episode; no event while still fresh.
	o.MarkActivity()
	if again := collectIdleEvents(t, o, 30*time.Millisecond); len(again) != 0 {
		t.Fatalf("idle event fired right after activity resumed: %v", again)
	}

	// Idle again: a new episode produces exactly one more event.
	if again := collectIdleEvents(t, o, 250*time.Millisecond); len(again) != 1 {
		t.Fatalf("expected exactly 1 re-armed idle event, got %d", len(again))
	}
}

// TestIdleWatchdogSuppressedWhileToolInFlight verifies that an in-flight tool
// that completes just before the idle threshold still counts as progress: the
// idle event is suppressed while it runs, and fires once the accumulated
// silence (measured from the tool's start) passes the threshold. A tool that
// outlives the threshold behaves differently — see
// TestIdleWatchdogKillsHungToolBeforeAgent.
func TestIdleWatchdogSuppressedWhileToolInFlight(t *testing.T) {
	o := newIdleTestOrchestrator(t, 100*time.Millisecond, 0, 10*time.Millisecond, []client.ChatMessage{
		{Role: "user", Content: client.TextContent("goal")},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o.startIdleWatchdog(ctx, cancel)

	started := make(chan struct{})
	finished := make(chan struct{})
	base := func(ctx context.Context, tc client.ToolCall) (string, error) {
		close(started)
		time.Sleep(60 * time.Millisecond) // completes before the 100ms threshold
		return "done", nil
	}
	toolRunner := o.activityMiddleware()(base)

	go func() {
		defer close(finished)
		_, _ = toolRunner(ctx, client.ToolCall{
			Type:     "function",
			Function: client.FunctionCall{Name: "fake_slow_tool", Arguments: "{}"},
		})
	}()

	<-started
	waitFor(t, 2*time.Second, func() bool { return o.inFlightTools.Load() == 1 })

	// The young in-flight tool suppresses the idle event.
	if events := collectIdleEvents(t, o, 40*time.Millisecond); len(events) != 0 {
		t.Fatalf("idle event fired while a young tool was in flight: %v", events)
	}

	<-finished
	waitFor(t, 2*time.Second, func() bool { return o.inFlightTools.Load() == 0 })
	waitFor(t, 2*time.Second, func() bool { return o.oldestToolStartAt.Load() == 0 })

	// Watchdog is alive: with the tool finished and no new activity, the
	// episode fires exactly once.
	if events := collectIdleEvents(t, o, 250*time.Millisecond); len(events) != 1 {
		t.Fatalf("expected exactly 1 idle event after the tool finished, got %d", len(events))
	}
}

// fakeIdleTool is a minimal common.Tool implementation for idle-watchdog
// tests, registered on the test session so executor.ExecuteToolCalls can run
// it through the orchestrator's activity middleware.
type fakeIdleTool struct {
	name string
	exec func(ctx context.Context, args json.RawMessage) (string, error)
}

func (f *fakeIdleTool) Name() string        { return f.name }
func (f *fakeIdleTool) Description() string { return "fake tool for idle tests" }
func (f *fakeIdleTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (f *fakeIdleTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return f.exec(ctx, args)
}
func (f *fakeIdleTool) RequiresConfirmation(json.RawMessage) bool { return false }
func (f *fakeIdleTool) CallString(json.RawMessage) string         { return f.name }

// TestIdleWatchdogKillsHungToolBeforeAgent verifies the two-stage escalation
// end to end. A tool hung past the idle threshold stops masking idleness, so
// the idle event fires despite the in-flight call; on the tick that exceeds
// the kill threshold the watchdog kills the TOOL first (its cancellable
// context, registered by executor.ExecuteToolCalls, makes the call return a
// "tool cancelled" result) and the agent recovers — the next tool call runs on
// a still-alive parent context and the agent-level self-cancel never fires.
func TestIdleWatchdogKillsHungToolBeforeAgent(t *testing.T) {
	o := newIdleTestOrchestrator(t, 60*time.Millisecond, 150*time.Millisecond, 50*time.Millisecond, []client.ChatMessage{
		{Role: "user", Content: client.TextContent("goal")},
	})

	started := make(chan struct{})
	sawCancel := make(chan error, 1)
	o.sess.Registry.Register(&fakeIdleTool{
		name: "hung_tool",
		exec: func(ctx context.Context, args json.RawMessage) (string, error) {
			close(started)
			<-ctx.Done()
			sawCancel <- ctx.Err()
			return "", fmt.Errorf("tool cancelled: %v", ctx.Err())
		},
	})
	o.sess.Registry.Register(&fakeIdleTool{
		name: "quick_tool",
		exec: func(ctx context.Context, args json.RawMessage) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", fmt.Errorf("inherited a cancelled context: %v", err)
			}
			return "recovered", nil
		},
	})

	toolCalls := []client.ToolCall{
		{ID: "tc_1", Function: client.FunctionCall{Name: "hung_tool", Arguments: "{}"}},
		{ID: "tc_2", Function: client.FunctionCall{Name: "quick_tool", Arguments: "{}"}},
	}

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	o.startIdleWatchdog(runCtx, cancelRun)

	done := make(chan error, 1)
	go func() {
		done <- executor.ExecuteToolCalls(runCtx, o.sess, toolCalls, o.withActivityMiddleware())
	}()

	<-started
	waitFor(t, 2*time.Second, func() bool { return o.inFlightTools.Load() == 1 })

	// Nothing but the watchdog's stage-1 tool kill can unblock the hung call:
	// once ExecuteToolCalls returns, the escalation has happened.
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ExecuteToolCalls returned an error after the tool kill: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the watchdog did not kill the hung tool")
	}

	if err := <-sawCancel; err != context.Canceled {
		t.Fatalf("the hung tool's context error = %v, want context.Canceled", err)
	}

	// The agent continued: the killed call recorded the cancellation result
	// and the next call ran with a fresh context.
	if len(o.sess.History) != 3 {
		t.Fatalf("expected 3 history entries (user + 2 tool results), got %d", len(o.sess.History))
	}
	if first := o.sess.History[1].Content.String(); first != "tool cancelled by the harness idle watchdog" {
		t.Errorf("killed call result = %q, want the tool-cancellation message", first)
	}
	if second := o.sess.History[2].Content.String(); second != "recovered" {
		t.Errorf("post-kill tool result = %q, want %q", second, "recovered")
	}

	waitFor(t, 2*time.Second, func() bool { return o.inFlightTools.Load() == 0 })
	waitFor(t, 2*time.Second, func() bool { return o.oldestToolStartAt.Load() == 0 })

	// Stage 2 must not fire: activity resumed with the next tool call, so this
	// window ends well before the re-armed idle age could reach the kill
	// threshold again (~killAfter after the recovery). Blocking on the event
	// channel for the window lets any re-armed idle episode (harmless) pass.
	windowEvents := collectIdleEvents(t, o, 80*time.Millisecond)
	if runCtx.Err() != nil {
		t.Errorf("agent run context was cancelled after the tool kill: %v", runCtx.Err())
	}
	if reason := o.IdleKillReason(); reason != "" {
		t.Errorf("idle kill reason recorded without an agent kill: %q", reason)
	}

	// The idle event fired despite the in-flight (stalled) tool — the first
	// episode fired before the kill; a re-armed follow-up episode may have
	// fired since. Count what the window above collected plus whatever is
	// still buffered.
	idleEvents := len(windowEvents)
drainEvents:
	for {
		select {
		case ev := <-o.eventCh:
			if _, ok := ev.(common.SubagentIdleEvent); ok {
				idleEvents++
			}
		default:
			break drainEvents
		}
	}
	if idleEvents < 1 {
		t.Fatal("expected at least one idle event despite the in-flight tool")
	}
}

// TestIdleKillSelfCancels verifies that sustained idle past the kill
// threshold cancels the orchestrator's own run context exactly once, with the
// probe lines recorded in the kill reason.
func TestIdleKillSelfCancels(t *testing.T) {
	history := []client.ChatMessage{
		{Role: "user", Content: client.TextContent("goal")},
		{Role: "assistant", Content: client.TextContent("Reply 2")},
	}
	o := newIdleTestOrchestrator(t, 40*time.Millisecond, 80*time.Millisecond, 10*time.Millisecond, history)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o.startIdleWatchdog(ctx, cancel)

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("run context was not cancelled by the idle watchdog")
	}
	if ctx.Err() != context.Canceled {
		t.Fatalf("ctx.Err() = %v, want context.Canceled", ctx.Err())
	}

	reason := o.IdleKillReason()
	if reason == "" {
		t.Fatal("expected a recorded kill reason after the idle kill")
	}
	if !strings.Contains(reason, "assistant: Reply 2") {
		t.Errorf("kill reason must include the probe lines, got %q", reason)
	}
	if !strings.Contains(reason, "idle for") {
		t.Errorf("kill reason must report the idle duration, got %q", reason)
	}

	// The kill happens on the same once-per-episode tick as the notification.
	if events := collectIdleEvents(t, o, 50*time.Millisecond); len(events) != 1 {
		t.Fatalf("expected exactly 1 idle event alongside the kill, got %d", len(events))
	}
}

// TestMarkActivityThroughMiddleware verifies the activity middleware bumps
// lastActivity at entry and keeps inFlightTools > 0 for the whole execution.
func TestMarkActivityThroughMiddleware(t *testing.T) {
	o := newIdleTestOrchestrator(t, 0, 0, 0, nil)

	before := time.Unix(0, o.lastActivity.Load())
	done := make(chan struct{})
	base := func(ctx context.Context, tc client.ToolCall) (string, error) {
		time.Sleep(100 * time.Millisecond)
		return "ok", nil
	}
	toolRunner := o.activityMiddleware()(base)

	go func() {
		defer close(done)
		_, _ = toolRunner(context.Background(), client.ToolCall{
			Type:     "function",
			Function: client.FunctionCall{Name: "fake_tool", Arguments: "{}"},
		})
	}()

	waitFor(t, 2*time.Second, func() bool { return o.inFlightTools.Load() > 0 })

	if after := time.Unix(0, o.lastActivity.Load()); !after.After(before) {
		t.Fatalf("lastActivity was not bumped by the middleware: before=%v after=%v", before, after)
	}
	// MarkActivity re-arms the idle notification for a fresh episode.
	if o.idleNotified.Load() {
		t.Fatal("MarkActivity must reset idleNotified")
	}
	if inFlight := o.inFlightTools.Load(); inFlight != 1 {
		t.Fatalf("inFlightTools during execution = %d, want 1", inFlight)
	}

	<-done
	if inFlight := o.inFlightTools.Load(); inFlight != 0 {
		t.Fatalf("inFlightTools after execution = %d, want 0", inFlight)
	}
}

// TestLastTranscriptLines covers the probe renderer: last-n window, role
// prefix, first-line-only content, tool-call fallback for content-less
// assistant messages, skipping fully empty messages, and length capping.
func TestLastTranscriptLines(t *testing.T) {
	long := strings.Repeat("x", 300)
	msgs := []client.ChatMessage{
		{Role: "user", Content: client.TextContent("dropped: outside the window")},
		{Role: "assistant", Content: client.TextContent("with tools")},
		{Role: "assistant", ToolCalls: []client.ToolCall{{Function: client.FunctionCall{Name: "bash"}}}},
		{Role: "tool", Content: client.TextContent("command output\nsecond line")},
		{Role: "assistant", Content: client.TextContent(long)},
	}

	lines := lastTranscriptLines(msgs, 3)
	if len(lines) != 3 {
		t.Fatalf("expected 3 probe lines, got %d: %v", len(lines), lines)
	}
	want := []string{
		"assistant: called bash",
		"tool: command output",
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("probe[%d] = %q, want %q", i, lines[i], w)
		}
	}
	if !strings.HasPrefix(lines[2], "assistant: ") || !strings.HasSuffix(lines[2], "...") {
		t.Errorf("long line must be role-prefixed and truncated, got %q", lines[2])
	}
	if len([]rune(lines[2])) > idleProbeLineMaxLen {
		t.Errorf("probe line exceeds the cap: %d runes", len([]rune(lines[2])))
	}
	if strings.Join(lines, "\n") == "" || strings.Contains(strings.Join(lines, "\n"), "dropped") {
		t.Errorf("probe must only contain the last entries, got %v", lines)
	}

	// A message with neither content nor tool calls contributes no line.
	empty := []client.ChatMessage{{Role: "assistant"}}
	if got := lastTranscriptLines(empty, 3); len(got) != 0 {
		t.Errorf("expected no lines for empty messages, got %v", got)
	}
	if got := lastTranscriptLines(msgs, 0); got != nil {
		t.Errorf("expected no lines for n=0, got %v", got)
	}
}

// TestIdleWatchdogDisabledWithoutThreshold pins the off switch: no policy, no
// watchdog activity, no events.
func TestIdleWatchdogDisabledWithoutThreshold(t *testing.T) {
	o := newIdleTestOrchestrator(t, 0, 0, 10*time.Millisecond, []client.ChatMessage{
		{Role: "user", Content: client.TextContent("goal")},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o.startIdleWatchdog(ctx, cancel)

	if events := collectIdleEvents(t, o, 100*time.Millisecond); len(events) != 0 {
		t.Fatalf("watchdog must stay silent when idleTimeout is 0, got %v", events)
	}
}
