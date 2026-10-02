package orchestrator

import (
	"errors"
	"fmt"
	"testing"

	"late/internal/client"
)

// TestContextExceededChainClassification pins the error-chain facts the run
// loop's context-exhaustion branch depends on: a *client.ContextExceededError
// delivered through the executor's "stream error" wrapping still answers
// errors.Is(ErrContextExceeded) (so the no-rollback branch fires), and it
// still exposes the underlying *StatusError via errors.As (so a provider that
// reports exhaustion as a 400 reaches that branch BEFORE the plain-400
// rollback — the ordering bug this guards: popping the user's message on a
// size-only failure).
func TestContextExceededChainClassification(t *testing.T) {
	// The HTTP-classified variant: a 400 whose body names the token limit.
	cee := &client.ContextExceededError{
		Status: &client.StatusError{StatusCode: 400, Body: "maximum context length is 8192 tokens"},
		Reason: "http 400",
	}
	wrapped := fmt.Errorf("stream error: %w", cee)

	if !errors.Is(wrapped, client.ErrContextExceeded) {
		t.Fatal("context-exceeded sentinel lost through the stream-error wrap: the run loop would classify it as a plain 400 and roll the user's message back")
	}

	var se *client.StatusError
	if !errors.As(wrapped, &se) || se.StatusCode != 400 {
		t.Fatal("underlying 400 StatusError no longer recoverable via errors.As; status-gated handling would break")
	}

	// The finish_reason=length variant carries no StatusError: it must be
	// recognized by the sentinel alone.
	lengthOnly := fmt.Errorf("stream error: %w", &client.ContextExceededError{Reason: "finish_reason=length"})
	if !errors.Is(lengthOnly, client.ErrContextExceeded) {
		t.Fatal("finish_reason=length exhaustion sentinel lost through the wrap")
	}
}
