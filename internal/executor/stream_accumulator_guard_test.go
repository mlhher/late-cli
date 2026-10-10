package executor

import (
	"testing"

	"late/internal/client"
	"late/internal/common"
)

// TestStreamAccumulator_AppendNegativeToolCallIndexDoesNotPanic pins the
// malformed-input guard: a provider delta carrying a negative tool-call index
// would previously index a.ToolCalls[-1] and panic, killing the whole CLI.
// A delta that matches no slot is appended as its own entry instead.
func TestStreamAccumulator_AppendNegativeToolCallIndexDoesNotPanic(t *testing.T) {
	acc := StreamAccumulator{}
	// A panic here fails the test run — that is the regression signal.
	acc.Append(common.StreamResult{
		ToolCalls: []client.ToolCall{
			{Index: -1, ID: "call_x", Function: client.FunctionCall{Name: "read_file", Arguments: `{"path"`}},
		},
	})
	if len(acc.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call after negative-index delta, got %d", len(acc.ToolCalls))
	}
	if acc.ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("expected name 'read_file', got %q", acc.ToolCalls[0].Function.Name)
	}

	// Negative-index deltas never merge by slot (no slot can match a
	// negative index): each appends, and nothing panics or corrupts.
	acc.Append(common.StreamResult{
		ToolCalls: []client.ToolCall{
			{Index: -1, Function: client.FunctionCall{Arguments: `: "a.go"`}},
		},
	})
	if len(acc.ToolCalls) != 2 {
		t.Fatalf("expected the second negative-index delta to append, got %d entries", len(acc.ToolCalls))
	}
	if acc.ToolCalls[0].Function.Arguments != `{"path"` {
		t.Errorf("first entry corrupted: %q", acc.ToolCalls[0].Function.Arguments)
	}
}

// TestStreamAccumulator_AppendOutOfOrderToolCallDoesNotCrossMerge pins that an
// out-of-order delta (index 5 arriving before index 0) cannot splice its
// arguments into another tool call's slot: position-based merging without an
// identity check corrupted the first slot's arguments.
func TestStreamAccumulator_AppendOutOfOrderToolCallDoesNotCrossMerge(t *testing.T) {
	acc := StreamAccumulator{}
	acc.Append(common.StreamResult{
		ToolCalls: []client.ToolCall{
			{Index: 5, ID: "call_5", Function: client.FunctionCall{Name: "five", Arguments: `{"a":1}`}},
		},
	})
	acc.Append(common.StreamResult{
		ToolCalls: []client.ToolCall{
			{Index: 0, Function: client.FunctionCall{Arguments: `{"b":2}`}},
		},
	})

	if len(acc.ToolCalls) != 2 {
		t.Fatalf("got %d tool calls, want 2 (out-of-order delta must not cross-merge)", len(acc.ToolCalls))
	}
	if got := acc.ToolCalls[0].Function.Arguments; got != `{"a":1}` {
		t.Errorf("tool call 5 arguments corrupted by the out-of-order delta: %q", got)
	}
	if got := acc.ToolCalls[1].Function.Arguments; got != `{"b":2}` {
		t.Errorf("tool call 0 arguments = %q, want %q", got, `{"b":2}`)
	}
}

// TestStreamAccumulator_AppendInOrderStillMerges guards the fix against
// over-correction: the canonical in-order OpenAI delta sequence (create at
// index i, then append argument fragments to index i) must merge exactly as
// before.
func TestStreamAccumulator_AppendInOrderStillMerges(t *testing.T) {
	acc := StreamAccumulator{}
	acc.Append(common.StreamResult{
		ToolCalls: []client.ToolCall{
			{Index: 0, ID: "call_1", Function: client.FunctionCall{Name: "read_file", Arguments: `{"path"`}},
		},
	})
	acc.Append(common.StreamResult{
		ToolCalls: []client.ToolCall{
			{Index: 0, Function: client.FunctionCall{Arguments: `: "test.go"}`}},
		},
	})
	acc.Append(common.StreamResult{
		ToolCalls: []client.ToolCall{
			{Index: 1, ID: "call_2", Function: client.FunctionCall{Name: "write_file", Arguments: `{"path": "."}`}},
		},
	})

	if len(acc.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(acc.ToolCalls))
	}
	if got := acc.ToolCalls[0].Function.Arguments; got != `{"path": "test.go"}` {
		t.Errorf("tool call 0 arguments = %q, want merged %q", got, `{"path": "test.go"}`)
	}
	if acc.ToolCalls[0].ID != "call_1" || acc.ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("tool call 0 identity changed: id=%q name=%q", acc.ToolCalls[0].ID, acc.ToolCalls[0].Function.Name)
	}
	if acc.ToolCalls[1].Function.Name != "write_file" {
		t.Errorf("tool call 1 name = %q, want 'write_file'", acc.ToolCalls[1].Function.Name)
	}
}
