package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"late/internal/client"
	"late/internal/common"
)

// TestRetryEventKeepsAgentThinking covers the RetryEvent dispatch: the status
// bar announces the retry, the agent stays thinking (spinner keeps running),
// the failed attempt's partial output and render caches are dropped, and a
// pinned error is left untouched until a successful turn clears it.
func TestRetryEventKeepsAgentThinking(t *testing.T) {
	m, s := newViewportBenchmarkModel(nil)

	sentinel := errors.New("previous failure")
	s.Error = sentinel
	s.State = StateStreaming
	s.StreamingState = common.ContentEvent{ID: m.Focused.ID(), Content: "partial attempt output"}
	s.StreamingStyledCache = "styled cache"
	s.StreamingChunkCount = 7

	updated, _ := m.Update(OrchestratorEventMsg{Event: common.RetryEvent{
		ID:          m.Focused.ID(),
		Attempt:     2,
		MaxAttempts: 5,
		Delay:       1500 * time.Millisecond,
		Err:         errors.New("connection reset by peer"),
	}})
	*m = updated.(Model)
	s = m.GetAgentState(m.Focused.ID())

	if !strings.Contains(s.StatusText, "retry 2/5") {
		t.Fatalf("StatusText = %q, want it to contain retry 2/5", s.StatusText)
	}
	if !strings.Contains(s.StatusText, "after 1.5s backoff") {
		t.Fatalf("StatusText = %q, want the delay truncated to 1.5s in the past-tense backoff phrasing", s.StatusText)
	}
	if strings.Contains(s.StatusText, "retrying in") {
		t.Fatalf("StatusText = %q, must not imply a live countdown (the line is rendered once and never updated)", s.StatusText)
	}
	if s.State != StateThinking {
		t.Fatalf("State = %v, want StateThinking", s.State)
	}
	if s.StreamingStyledCache != "" || s.StreamingChunkCount != 0 {
		t.Fatalf("streaming render cache not cleared (cache=%q, chunks=%d)", s.StreamingStyledCache, s.StreamingChunkCount)
	}
	if s.StreamingState.Content != "partial attempt output" {
		t.Fatalf("failed attempt's partial text was lost during backoff: %q", s.StreamingState.Content)
	}
	if s.RetryVerb != retryVerbConnectionLost {
		t.Fatalf("RetryVerb = %q, want %q after an infra failure", s.RetryVerb, retryVerbConnectionLost)
	}
	if s.Error == nil || s.Error != sentinel {
		t.Fatalf("Error = %v, want the sentinel %v to survive the retry", s.Error, sentinel)
	}
}

// TestRetryEventHTTP400NamesTheRejection covers failure-class honesty: when the
// underlying stream error is an HTTP 400 from the API (the request body was
// rejected, not the connection lost), the status bar says so instead of
// claiming the connection dropped.
func TestRetryEventHTTP400NamesTheRejection(t *testing.T) {
	m, _ := newViewportBenchmarkModel(nil)

	updated, _ := m.Update(OrchestratorEventMsg{Event: common.RetryEvent{
		ID:          m.Focused.ID(),
		Attempt:     1,
		MaxAttempts: 3,
		Delay:       750 * time.Millisecond,
		Err:         fmt.Errorf("stream error: %w", &client.StatusError{StatusCode: 400, Body: "read body failed"}),
	}})
	*m = updated.(Model)
	s := m.GetAgentState(m.Focused.ID())

	if !strings.Contains(s.StatusText, "request rejected by the API") {
		t.Fatalf("StatusText = %q, want it to name the API rejection", s.StatusText)
	}
	if strings.Contains(s.StatusText, "connection lost") {
		t.Fatalf("StatusText = %q, must not claim a lost connection for an HTTP 400", s.StatusText)
	}
	if !strings.Contains(s.StatusText, "retry 1/3 after 700ms backoff") {
		t.Fatalf("StatusText = %q, want it to contain retry 1/3 after 700ms backoff", s.StatusText)
	}
	if strings.Contains(s.StatusText, "attempt 1/3") {
		t.Fatalf("StatusText = %q, must not use the old attempt-in-parens countdown format", s.StatusText)
	}
}

// TestThinkingClearsRetryVerbSilently covers the thinking branch after recovery
// moved to the dedicated RecoveryEvent: a new turn still clears a pinned error
// box, and the retry verb is only silently cleared here as a safety net for a
// dropped RecoveryEvent — the "thinking" status must NOT fire any toast
// (recovery is announced immediately by the RecoveryEvent, if it arrives).
func TestThinkingClearsRetryVerbSilently(t *testing.T) {
	m, _ := newViewportBenchmarkModel(nil)

	updated, _ := m.Update(OrchestratorEventMsg{Event: common.RetryEvent{
		ID:          m.Focused.ID(),
		Attempt:     1,
		MaxAttempts: 3,
		Delay:       750 * time.Millisecond,
		Err:         errors.New("connection reset by peer"),
	}})
	*m = updated.(Model)
	s := m.GetAgentState(m.Focused.ID())

	if s.RetryVerb != retryVerbConnectionLost {
		t.Fatalf("RetryVerb = %q, want %q after an infra failure", s.RetryVerb, retryVerbConnectionLost)
	}

	s.Error = errors.New("stale failure")
	m.ToastMessage = ""
	m.ToastWarning = true

	updated, cmd := m.Update(OrchestratorEventMsg{Event: common.StatusEvent{ID: m.Focused.ID(), Status: "thinking"}})
	*m = updated.(Model)
	s = m.GetAgentState(m.Focused.ID())

	if s.Error != nil {
		t.Fatalf("Error = %v, want nil once a new turn starts", s.Error)
	}
	if s.RetryVerb != "" {
		t.Fatalf("RetryVerb = %q, want it silently cleared on the new turn", s.RetryVerb)
	}

	// Run any returned command and feed its messages back through Update,
	// exactly as Bubble Tea would, so a stale recovery toast cannot hide
	// behind a deferred command. The frame tick only coalesces presentation.
	if cmd != nil {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, child := range batch {
				childMsg := child()
				if _, isFrame := childMsg.(transcriptFrameMsg); isFrame {
					continue
				}
				updated, _ := m.Update(childMsg)
				*m = updated.(Model)
			}
		} else {
			updated, _ := m.Update(msg)
			*m = updated.(Model)
		}
	}

	if m.ToastMessage != "" {
		t.Fatalf("ToastMessage = %q, want no recovery toast from the thinking status", m.ToastMessage)
	}
}

// TestRecoveryEventToastsImmediately covers the dedicated RecoveryEvent
// sequence: the orchestrator emits it exactly once when the retried attempt
// actually produces a response. The toast ("connection restored") fires
// immediately — not speculatively on the next turn's "thinking" status — and
// only once: neither the final response's content events nor the next turn's
// thinking status may repeat it.
func TestRecoveryEventToastsImmediately(t *testing.T) {
	m, _ := newViewportBenchmarkModel(nil)

	updated, _ := m.Update(OrchestratorEventMsg{Event: common.RetryEvent{
		ID:          m.Focused.ID(),
		Attempt:     1,
		MaxAttempts: 3,
		Delay:       750 * time.Millisecond,
		Err:         errors.New("connection reset by peer"),
	}})
	*m = updated.(Model)
	s := m.GetAgentState(m.Focused.ID())

	if s.RetryVerb != retryVerbConnectionLost {
		t.Fatalf("RetryVerb = %q, want %q after an infra failure", s.RetryVerb, retryVerbConnectionLost)
	}

	m.ToastMessage = ""
	updated, cmd := m.Update(OrchestratorEventMsg{Event: common.RecoveryEvent{ID: m.Focused.ID()}})
	*m = updated.(Model)
	s = m.GetAgentState(m.Focused.ID())

	if s.StatusText != "" {
		t.Fatalf("StatusText = %q, want empty so timer message is cleared", s.StatusText)
	}
	if s.RetryVerb != "" {
		t.Fatalf("RetryVerb = %q, want it cleared once recovery is announced", s.RetryVerb)
	}
	if cmd == nil {
		t.Fatal("expected a command delivering the restored toast")
	}

	// Run the returned command and feed every produced message back through
	// Update, exactly as Bubble Tea would. The frame tick message is skipped:
	// it only coalesces presentation.
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			childMsg := child()
			if _, isFrame := childMsg.(transcriptFrameMsg); isFrame {
				continue
			}
			updated, _ := m.Update(childMsg)
			*m = updated.(Model)
		}
	} else {
		updated, _ := m.Update(msg)
		*m = updated.(Model)
	}

	if m.ToastMessage != "connection regained" {
		t.Fatalf("ToastMessage = %q, want %q", m.ToastMessage, "connection regained")
	}
	if m.ToastWarning {
		t.Fatal("restored toast must be success-style, not warning")
	}
	if m.ToastExpireTime <= time.Now().UnixMilli() {
		t.Fatalf("ToastExpireTime = %d, want a future expiry", m.ToastExpireTime)
	}

	// The retried attempt streams its final response: no second toast.
	updated, _ = m.Update(OrchestratorEventMsg{Event: common.ContentEvent{
		ID:        m.Focused.ID(),
		Content:   "final response after the retry",
		Completed: true,
	}})
	*m = updated.(Model)

	if m.ToastMessage != "connection regained" {
		t.Fatalf("ToastMessage = %q after the final response, want it unchanged (no second toast)", m.ToastMessage)
	}

	// The next turn's thinking status must not repeat the recovery toast.
	updated, cmd = m.Update(OrchestratorEventMsg{Event: common.StatusEvent{ID: m.Focused.ID(), Status: "thinking"}})
	*m = updated.(Model)

	// Pump any returned command; the frame tick only coalesces presentation.
	if cmd != nil {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, child := range batch {
				childMsg := child()
				if _, isFrame := childMsg.(transcriptFrameMsg); isFrame {
					continue
				}
				updated, _ := m.Update(childMsg)
				*m = updated.(Model)
			}
		} else {
			updated, _ := m.Update(msg)
			*m = updated.(Model)
		}
	}
	if m.ToastMessage != "connection regained" {
		t.Fatalf("ToastMessage = %q after the next turn's thinking, want it unchanged (no second toast)", m.ToastMessage)
	}
}

// TestRecoveryEventToastMatchesFailureClass covers the 400-class recovery: when
// the retried failure was an HTTP 400 from the API (the request body was
// rejected, not the connection lost), the immediate recovery toast must
// announce the request was accepted after the retry instead of claiming the
// connection was restored.
func TestRecoveryEventToastMatchesFailureClass(t *testing.T) {
	m, _ := newViewportBenchmarkModel(nil)

	updated, _ := m.Update(OrchestratorEventMsg{Event: common.RetryEvent{
		ID:          m.Focused.ID(),
		Attempt:     1,
		MaxAttempts: 3,
		Delay:       750 * time.Millisecond,
		Err:         fmt.Errorf("stream error: %w", &client.StatusError{StatusCode: 400, Body: "read body failed"}),
	}})
	*m = updated.(Model)
	s := m.GetAgentState(m.Focused.ID())

	if s.RetryVerb != retryVerbRejectedByAPI {
		t.Fatalf("RetryVerb = %q, want %q after an HTTP 400", s.RetryVerb, retryVerbRejectedByAPI)
	}

	m.ToastMessage = ""
	updated, cmd := m.Update(OrchestratorEventMsg{Event: common.RecoveryEvent{ID: m.Focused.ID()}})
	*m = updated.(Model)
	s = m.GetAgentState(m.Focused.ID())

	if s.StatusText != "" {
		t.Fatalf("StatusText = %q, want empty so timer message is cleared", s.StatusText)
	}
	if s.RetryVerb != "" {
		t.Fatalf("RetryVerb = %q, want it cleared once recovery is announced", s.RetryVerb)
	}
	if cmd == nil {
		t.Fatal("expected a command delivering the recovered toast")
	}

	// Run the returned command and feed every produced message back through
	// Update, exactly as Bubble Tea would. The frame tick message is skipped:
	// it only coalesces presentation.
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			childMsg := child()
			if _, isFrame := childMsg.(transcriptFrameMsg); isFrame {
				continue
			}
			updated, _ := m.Update(childMsg)
			*m = updated.(Model)
		}
	} else {
		updated, _ := m.Update(msg)
		*m = updated.(Model)
	}

	if !strings.Contains(m.ToastMessage, "request accepted after retry") {
		t.Fatalf("ToastMessage = %q, want it to mention the accepted retry", m.ToastMessage)
	}
	if strings.Contains(m.ToastMessage, "connection regained") || strings.Contains(m.ToastMessage, "connection restored") {
		t.Fatalf("ToastMessage = %q, must not claim connection was regained for an HTTP 400", m.ToastMessage)
	}
}

// TestErrorStatusClearsStaleRetryVerb covers the stale-verb bug: a 400 that
// exhausts the bad-body budget ends the turn in an error box, but RetryVerb
// used to survive the error, so the resubmitted turn's first "thinking" fired
// a bogus "request accepted after retry" toast for a request that was never
// accepted. The error branch must clear the stored verb.
func TestErrorStatusClearsStaleRetryVerb(t *testing.T) {
	m, _ := newViewportBenchmarkModel(nil)

	updated, _ := m.Update(OrchestratorEventMsg{Event: common.RetryEvent{
		ID:          m.Focused.ID(),
		Attempt:     3,
		MaxAttempts: 3,
		Delay:       750 * time.Millisecond,
		Err:         fmt.Errorf("stream error: %w", &client.StatusError{StatusCode: 400, Body: "read body failed"}),
	}})
	*m = updated.(Model)
	s := m.GetAgentState(m.Focused.ID())

	if s.RetryVerb != retryVerbRejectedByAPI {
		t.Fatalf("RetryVerb = %q, want %q after an HTTP 400", s.RetryVerb, retryVerbRejectedByAPI)
	}

	updated, _ = m.Update(OrchestratorEventMsg{Event: common.StatusEvent{
		ID:     m.Focused.ID(),
		Status: "error",
		Error:  errors.New("request rejected by the API after retries"),
	}})
	*m = updated.(Model)
	s = m.GetAgentState(m.Focused.ID())

	if s.RetryVerb != "" {
		t.Fatalf("RetryVerb = %q, want it cleared when the turn ends in error", s.RetryVerb)
	}

	m.ToastMessage = ""
	updated, cmd := m.Update(OrchestratorEventMsg{Event: common.StatusEvent{ID: m.Focused.ID(), Status: "thinking"}})
	*m = updated.(Model)
	s = m.GetAgentState(m.Focused.ID())

	if s.RetryVerb != "" {
		t.Fatalf("RetryVerb = %q, want it to stay clear on the next turn", s.RetryVerb)
	}

	// Run any returned command and feed its messages back through Update,
	// exactly as Bubble Tea would, so a stale recovery toast cannot hide
	// behind a deferred command. The frame tick only coalesces presentation.
	if cmd != nil {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, child := range batch {
				childMsg := child()
				if _, isFrame := childMsg.(transcriptFrameMsg); isFrame {
					continue
				}
				updated, _ := m.Update(childMsg)
				*m = updated.(Model)
			}
		} else {
			updated, _ := m.Update(msg)
			*m = updated.(Model)
		}
	}

	if m.ToastMessage != "" {
		t.Fatalf("ToastMessage = %q, want no stale recovery toast after an errored turn", m.ToastMessage)
	}
	if strings.Contains(m.ToastMessage, "request accepted after retry") {
		t.Fatalf("ToastMessage = %q, must not claim the request was accepted", m.ToastMessage)
	}
}

// TestStopRequestedClearsStaleRetryVerb covers the stop path: a user stop
// during retry backoff ends the turn, and the stored verb must not survive
// into the next turn's "thinking" as a recovery toast.
func TestStopRequestedClearsStaleRetryVerb(t *testing.T) {
	m, _ := newViewportBenchmarkModel(nil)

	updated, _ := m.Update(OrchestratorEventMsg{Event: common.RetryEvent{
		ID:          m.Focused.ID(),
		Attempt:     1,
		MaxAttempts: 3,
		Delay:       750 * time.Millisecond,
		Err:         fmt.Errorf("stream error: %w", &client.StatusError{StatusCode: 400, Body: "read body failed"}),
	}})
	*m = updated.(Model)
	s := m.GetAgentState(m.Focused.ID())

	if s.RetryVerb != retryVerbRejectedByAPI {
		t.Fatalf("RetryVerb = %q, want %q after an HTTP 400", s.RetryVerb, retryVerbRejectedByAPI)
	}

	updated, _ = m.Update(OrchestratorEventMsg{Event: common.StopRequestedEvent{ID: m.Focused.ID()}})
	*m = updated.(Model)
	s = m.GetAgentState(m.Focused.ID())

	if s.RetryVerb != "" {
		t.Fatalf("RetryVerb = %q, want it cleared when the user stops the turn", s.RetryVerb)
	}

	m.ToastMessage = ""
	updated, cmd := m.Update(OrchestratorEventMsg{Event: common.StatusEvent{ID: m.Focused.ID(), Status: "thinking"}})
	*m = updated.(Model)
	s = m.GetAgentState(m.Focused.ID())

	if s.RetryVerb != "" {
		t.Fatalf("RetryVerb = %q, want it to stay clear on the next turn", s.RetryVerb)
	}

	// Run any returned command and feed its messages back through Update,
	// exactly as Bubble Tea would, so a stale recovery toast cannot hide
	// behind a deferred command. The frame tick only coalesces presentation.
	if cmd != nil {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, child := range batch {
				childMsg := child()
				if _, isFrame := childMsg.(transcriptFrameMsg); isFrame {
					continue
				}
				updated, _ := m.Update(childMsg)
				*m = updated.(Model)
			}
		} else {
			updated, _ := m.Update(msg)
			*m = updated.(Model)
		}
	}

	if m.ToastMessage != "" {
		t.Fatalf("ToastMessage = %q, want no stale recovery toast after a stop", m.ToastMessage)
	}
	if strings.Contains(m.ToastMessage, "request accepted after retry") {
		t.Fatalf("ToastMessage = %q, must not claim the request was accepted", m.ToastMessage)
	}
}

// TestThinkingWithoutRetryNoToast: a plain new turn (no retry in flight)
// still clears a pinned error box but must not fire the restored toast.
func TestThinkingWithoutRetryNoToast(t *testing.T) {
	m, s := newViewportBenchmarkModel(nil)

	s.Error = errors.New("stale failure")
	s.RetryVerb = ""
	m.ToastMessage = ""

	updated, _ := m.Update(OrchestratorEventMsg{Event: common.StatusEvent{ID: m.Focused.ID(), Status: "thinking"}})
	*m = updated.(Model)
	s = m.GetAgentState(m.Focused.ID())

	if s.Error != nil {
		t.Fatalf("Error = %v, want nil once a new turn starts", s.Error)
	}
	if s.RetryVerb != "" {
		t.Fatal("RetryVerb must stay clear")
	}
	if m.ToastMessage != "" {
		t.Fatalf("ToastMessage = %q, want no toast without a retry", m.ToastMessage)
	}
}

// TestRecoveryEventWithoutRetryVerbKeepsStatusAccurate covers the documented
// race: a user stop (or an error) can clear RetryVerb while the retried
// attempt is still in flight, and the orchestrator's RecoveryEvent then
// arrives with no retry verb stored. The branch must not panic, must not
// toast a recovery nobody was waiting for, and must keep the status line
// accurate ("streaming response").
func TestRecoveryEventWithoutRetryVerbKeepsStatusAccurate(t *testing.T) {
	m, s := newViewportBenchmarkModel(nil)

	// A stop raced the recovery: the verb was already cleared (the user
	// stopped during backoff, StopRequestedEvent reset it), yet the retried
	// attempt went on to succeed and the orchestrator still emits recovery.
	s.RetryVerb = ""
	s.State = StateThinking
	m.ToastMessage = ""

	updated, cmd := m.Update(OrchestratorEventMsg{Event: common.RecoveryEvent{ID: m.Focused.ID()}})
	*m = updated.(Model)
	s = m.GetAgentState(m.Focused.ID())

	if s.StatusText != "" {
		t.Fatalf("StatusText = %q, want empty", s.StatusText)
	}
	if s.RetryVerb != "" {
		t.Fatalf("RetryVerb = %q, want it to stay clear", s.RetryVerb)
	}

	// Run any returned command and feed its messages back through Update,
	// exactly as Bubble Tea would, so a stray recovery toast cannot hide
	// behind a deferred command. The frame tick only coalesces presentation.
	if cmd != nil {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, child := range batch {
				childMsg := child()
				if _, isFrame := childMsg.(transcriptFrameMsg); isFrame {
					continue
				}
				updated, _ := m.Update(childMsg)
				*m = updated.(Model)
			}
		} else {
			updated, _ := m.Update(msg)
			*m = updated.(Model)
		}
	}

	if m.ToastMessage != "" {
		t.Fatalf("ToastMessage = %q, want no recovery toast", m.ToastMessage)
	}
	if m.ToastWarning {
		t.Fatal("no toast of any kind must fire for a stop-raced recovery")
	}
}

// TestContentEventDuringRetryClearsTimerAndToasts covers recovery via incoming
// content: when the retried attempt sends its first chunk, the timer status text
// clears immediately, "connection regained" is toasted, and the new chunk
// replaces the old partial output.
func TestContentEventDuringRetryClearsTimerAndToasts(t *testing.T) {
	m, s := newViewportBenchmarkModel(nil)
	s.State = StateStreaming
	s.StreamingState = common.ContentEvent{ID: m.Focused.ID(), Content: "partial from attempt 1"}

	// Retry kicks in: backoff status is set, partial content is kept
	updated, _ := m.Update(OrchestratorEventMsg{Event: common.RetryEvent{
		ID:          m.Focused.ID(),
		Attempt:     1,
		MaxAttempts: 3,
		Delay:       1500 * time.Millisecond,
		Err:         errors.New("connection reset by peer"),
	}})
	*m = updated.(Model)
	s = m.GetAgentState(m.Focused.ID())

	if !strings.Contains(s.StatusText, "retry 1/3") {
		t.Fatalf("StatusText = %q, want it to announce retry", s.StatusText)
	}
	if s.StreamingState.Content != "partial from attempt 1" {
		t.Fatalf("StreamingState = %q, want partial output kept during backoff", s.StreamingState.Content)
	}

	// First chunk of the retried stream arrives
	m.ToastMessage = ""
	updated, cmd := m.Update(OrchestratorEventMsg{Event: common.ContentEvent{
		ID:      m.Focused.ID(),
		Content: "fresh chunk from attempt 2",
	}})
	*m = updated.(Model)
	s = m.GetAgentState(m.Focused.ID())

	if s.StatusText != "" {
		t.Fatalf("StatusText = %q, want empty so timer status clears immediately", s.StatusText)
	}
	if s.RetryVerb != "" {
		t.Fatalf("RetryVerb = %q, want empty once content arrives", s.RetryVerb)
	}
	if s.StreamingState.Content != "fresh chunk from attempt 2" {
		t.Fatalf("StreamingState = %q, want fresh chunk to replace partial output", s.StreamingState.Content)
	}
	if cmd == nil {
		t.Fatal("expected command delivering restored toast")
	}

	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			childMsg := child()
			if _, isFrame := childMsg.(transcriptFrameMsg); isFrame {
				continue
			}
			updated, _ := m.Update(childMsg)
			*m = updated.(Model)
		}
	} else {
		updated, _ := m.Update(msg)
		*m = updated.(Model)
	}

	if m.ToastMessage != "connection regained" {
		t.Fatalf("ToastMessage = %q, want %q", m.ToastMessage, "connection regained")
	}
	if m.ToastWarning {
		t.Fatal("restored toast must be success-style, not warning")
	}
}

// TestRetryPreservesPartialContentInTranscriptWithWarningLabel verifies that during
// retry backoff, the transcript displays the partial message with a retrying label.
func TestRetryPreservesPartialContentInTranscriptWithWarningLabel(t *testing.T) {
	m, s := newViewportBenchmarkModel(nil)
	s.State = StateStreaming
	s.StreamingState = common.ContentEvent{ID: m.Focused.ID(), Content: "important partial response"}

	updated, _ := m.Update(OrchestratorEventMsg{Event: common.RetryEvent{
		ID:          m.Focused.ID(),
		Attempt:     1,
		MaxAttempts: 3,
		Delay:       1500 * time.Millisecond,
		Err:         errors.New("connection reset by peer"),
	}})
	*m = updated.(Model)

	content := ansi.Strip(testTranscriptContent(m))
	if !strings.Contains(content, "important partial response") {
		t.Fatalf("transcript = %q, want it to retain partial response", content)
	}
	if !strings.Contains(content, "interrupted · retrying...") {
		t.Fatalf("transcript = %q, want it to show interrupted retrying label", content)
	}
	if strings.Contains(content, "connection lost") {
		t.Fatalf("transcript = %q, must not duplicate connection lost inside chat bubble", content)
	}
}

// TestRetryAnimatesInterruptedLabel verifies that during retry backoff, the
// "interrupted · retrying" transcript label registers an activity that animates
// dynamic moving dots (. -> .. -> ...) across clock ticks.
func TestRetryAnimatesInterruptedLabel(t *testing.T) {
	m, s := newViewportBenchmarkModel(nil)
	s.State = StateStreaming
	s.StreamingState = common.ContentEvent{ID: m.Focused.ID(), Content: "partial streamed answer"}

	updated, _ := m.Update(OrchestratorEventMsg{Event: common.RetryEvent{
		ID:          m.Focused.ID(),
		Attempt:     1,
		MaxAttempts: 3,
		Delay:       1500 * time.Millisecond,
		Err:         errors.New("connection reset by peer"),
	}})
	*m = updated.(Model)
	renderTestTranscript(m)
	s = m.GetAgentState(m.Focused.ID())

	// An activity should be registered for the retry label
	foundActivity := false
	for _, act := range s.Transcript.activities {
		if strings.Contains(act, "interrupted · retrying") {
			foundActivity = true
			break
		}
	}
	if !foundActivity {
		t.Fatalf("expected an activity registered for 'interrupted · retrying', got: %v", s.Transcript.activities)
	}

	// Verify animated dots cycle through ., .., ...
	f1 := ansi.Strip(m.renderActivityAt("interrupted · retrying...", 80, time.UnixMilli(0)))
	f2 := ansi.Strip(m.renderActivityAt("interrupted · retrying...", 80, time.UnixMilli(350)))
	f3 := ansi.Strip(m.renderActivityAt("interrupted · retrying...", 80, time.UnixMilli(700)))

	if !strings.Contains(f1, "interrupted · retrying.") || strings.Contains(f1, "interrupted · retrying..") {
		t.Fatalf("frame 1 = %q, want exactly 1 dot", f1)
	}
	if !strings.Contains(f2, "interrupted · retrying..") || strings.Contains(f2, "interrupted · retrying...") {
		t.Fatalf("frame 2 = %q, want exactly 2 dots", f2)
	}
	if !strings.Contains(f3, "interrupted · retrying...") {
		t.Fatalf("frame 3 = %q, want 3 dots", f3)
	}

	// Live transcript view should include the animated label
	liveView := ansi.Strip(m.transcriptView())
	if !strings.Contains(liveView, "interrupted · retrying") {
		t.Fatalf("live transcript view = %q, want it to contain interrupted retrying label", liveView)
	}
}
