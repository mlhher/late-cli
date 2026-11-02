package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"late/internal/client"
	"late/internal/common"
)

func verdicts(d *loopDetector, sigs ...string) []loopVerdict {
	out := make([]loopVerdict, len(sigs))
	for i, s := range sigs {
		out[i] = d.record(s)
	}
	return out
}

func TestLoopDetector(t *testing.T) {
	tests := []struct {
		name      string
		threshold int
		sigs      []string
		want      []loopVerdict
	}{
		{"identical repeats nudge then stop", 3, []string{"A", "A", "A", "A"},
			[]loopVerdict{loopNone, loopNone, loopNudge, loopStop}},
		{"different call in between resets", 3, []string{"A", "A", "B", "A", "A", "B"},
			[]loopVerdict{loopNone, loopNone, loopNone, loopNone, loopNone, loopNone}},
		{"A,B cycle is detected", 2, []string{"A", "B", "A", "B", "A"},
			[]loopVerdict{loopNone, loopNone, loopNone, loopNudge, loopStop}},
		{"recovery after nudge resets", 3, []string{"A", "A", "A", "B", "A", "A", "A"},
			[]loopVerdict{loopNone, loopNone, loopNudge, loopNone, loopNone, loopNone, loopNudge}},
		{"disabled", 0, []string{"A", "A", "A", "A", "A"},
			[]loopVerdict{loopNone, loopNone, loopNone, loopNone, loopNone}},
		{"progress (distinct sigs) never triggers", 3, []string{"A", "B", "C", "D", "E", "F"},
			[]loopVerdict{loopNone, loopNone, loopNone, loopNone, loopNone, loopNone}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := verdicts(newLoopDetector(tt.threshold), tt.sigs...)
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("verdicts = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func call(name, args string) client.ToolCall {
	tc := client.ToolCall{}
	tc.Function.Name = name
	tc.Function.Arguments = args
	return tc
}

func TestTurnSignature(t *testing.T) {
	base := turnSignature([]client.ToolCall{call("bash", `{"command":"ls","cwd":"/"}`)}, []string{"out"})

	if got := turnSignature([]client.ToolCall{call("bash", ` { "cwd": "/", "command": "ls" } `)}, []string{"out"}); got != base {
		t.Error("key order / whitespace must not change the signature")
	}
	if got := turnSignature([]client.ToolCall{call("bash", `{"command":"ls","cwd":"/"}`)}, []string{"different"}); got == base {
		t.Error("a different result must change the signature (progress)")
	}
	if got := turnSignature([]client.ToolCall{call("bash", `{"command":"pwd","cwd":"/"}`)}, []string{"out"}); got == base {
		t.Error("different args must change the signature")
	}
	if got := turnSignature([]client.ToolCall{call("read_file", `{"command":"ls","cwd":"/"}`)}, []string{"out"}); got == base {
		t.Error("different tool must change the signature")
	}
}

func loopCtx(threshold int) (context.Context, context.CancelFunc) {
	return context.WithTimeout(
		context.WithValue(context.WithValue(context.Background(), common.MaxStreamRetriesKey, 0), common.LoopThresholdKey, threshold),
		15*time.Second,
	)
}

// The server always answers with the same tool call, so the run loops until
// the detector nudges (turn 3) and then stops (turn 4).
func TestRunLoopStopsRepeatedToolCalls(t *testing.T) {
	rs := newRetryServer(t, func(w http.ResponseWriter, r *http.Request) { serveToolCallSSE(w) })
	sess := newRetryTestSession(t, rs.server.URL)
	ctx, cancel := loopCtx(3)
	defer cancel()

	_, err := RunLoop(ctx, sess, 20, nil, nil, nil, nil, nil, nil, nil)
	if !errors.Is(err, ErrRepeatedToolCalls) {
		t.Fatalf("err = %v, want ErrRepeatedToolCalls", err)
	}
	if got := rs.postCount(); got != 4 {
		t.Errorf("server got %d POSTs, want 4 (3 identical turns, nudge, 1 more, stop)", got)
	}
	nudges := 0
	for _, m := range sess.History {
		if m.Role == "user" && strings.Contains(m.Content.String(), "Loop detected") {
			nudges++
		}
	}
	if nudges != 1 {
		t.Errorf("history has %d loop nudges, want exactly 1", nudges)
	}
}

func TestRunLoopLoopDetectionDisabled(t *testing.T) {
	rs := newRetryServer(t, func(w http.ResponseWriter, r *http.Request) { serveToolCallSSE(w) })
	sess := newRetryTestSession(t, rs.server.URL)
	ctx, cancel := loopCtx(0)
	defer cancel()

	res, err := RunLoop(ctx, sess, 6, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("err = %v, want nil (detection disabled)", err)
	}
	if !strings.Contains(res, "max turns") {
		t.Errorf("result = %q, want the max-turns termination", res)
	}
	if got := rs.postCount(); got != 6 {
		t.Errorf("server got %d POSTs, want 6", got)
	}
}

// A real looping model words its text and reasoning differently every turn.
// Only the tool calls and their results are compared, so that must not hide a
// loop.
func TestRunLoopDetectsLoopDespiteVaryingTextAndReasoning(t *testing.T) {
	var rs *retryServer
	rs = newRetryServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := rs.postCount()
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(delta map[string]any, finish any) {
			b, _ := json.Marshal(map[string]any{
				"id":      "t",
				"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
			})
			fmt.Fprintf(w, "data: %s\n", b)
		}
		chunk(map[string]any{"reasoning_content": fmt.Sprintf("thought number %d", n)}, nil)
		chunk(map[string]any{"content": fmt.Sprintf("Let me try that again (attempt %d).", n)}, nil)
		chunk(map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "id": "call_1", "type": "function",
			"function": map[string]any{"name": "no_such_tool", "arguments": "{}"},
		}}}, nil)
		chunk(map[string]any{}, "tool_calls")
		fmt.Fprint(w, "data: [DONE]\n")
	})
	sess := newRetryTestSession(t, rs.server.URL)
	ctx, cancel := loopCtx(3)
	defer cancel()

	_, err := RunLoop(ctx, sess, 20, nil, nil, nil, nil, nil, nil, nil)
	if !errors.Is(err, ErrRepeatedToolCalls) {
		t.Fatalf("err = %v, want ErrRepeatedToolCalls", err)
	}
	if got := rs.postCount(); got != 4 {
		t.Errorf("server got %d POSTs, want 4", got)
	}
	var texts = map[string]bool{}
	for _, m := range sess.History {
		if m.Role == "assistant" {
			texts[m.Content.String()] = true
		}
	}
	if len(texts) < 3 {
		t.Errorf("assistant texts were not varied (%d distinct); the test is not exercising what it claims", len(texts))
	}
}

func TestLoopNudgeIsRecognisedAsSystemNotice(t *testing.T) {
	if !common.IsSystemNotice(loopNudgeMessage) {
		t.Fatal("the loop nudge must be recognised so the TUI does not draw it as a user prompt")
	}
}
