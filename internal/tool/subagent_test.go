package tool

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSpawnSubagentsAsyncTool_Schema(t *testing.T) {
	tool := BatchSpawnSubagentsTool{
		MaxConcurrent: 3,
	}

	if tool.Name() != "batch_spawn_subagents" {
		t.Fatalf("expected tool name 'batch_spawn_subagents', got %s", tool.Name())
	}

	desc := tool.Description()
	if !strings.Contains(desc, "coder") {
		t.Errorf("expected description to mention 'coder', got: %s", desc)
	}
	if !strings.Contains(desc, "researcher") {
		t.Errorf("expected description to mention 'researcher', got: %s", desc)
	}
	if !strings.Contains(desc, "3") {
		t.Errorf("expected description to mention max concurrent limit '3', got: %s", desc)
	}

	params := tool.Parameters()
	var schema struct {
		Properties struct {
			Subagents struct {
				Items struct {
					Properties struct {
						AgentType struct {
							Enum        []string `json:"enum"`
							Description string   `json:"description"`
						} `json:"agent_type"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"subagents"`
		} `json:"properties"`
	}

	if err := json.Unmarshal(params, &schema); err != nil {
		t.Fatalf("failed to parse schema JSON: %v", err)
	}

	enums := schema.Properties.Subagents.Items.Properties.AgentType.Enum
	foundCoder := false
	foundResearcher := false
	foundUnknown := false
	for _, e := range enums {
		if e == "coder" {
			foundCoder = true
		}
		if e == "researcher" {
			foundResearcher = true
		}
		if e == "unknown" {
			foundUnknown = true
		}
	}

	if !foundCoder {
		t.Errorf("expected 'coder' to be in agent_type enum, got %v", enums)
	}
	if !foundResearcher {
		t.Errorf("expected 'researcher' to be in agent_type enum, got %v", enums)
	}
	if foundUnknown {
		t.Errorf("expected 'unknown' NOT to be in agent_type enum, got %v", enums)
	}
}

func TestSpawnSubagentsAsyncTool_Validation(t *testing.T) {
	tool := BatchSpawnSubagentsTool{
		Runner: func(ctx context.Context, goal string, ctxFiles []string, agentType string) (string, error) {
			return "done", nil
		},
		MaxConcurrent: 2,
	}

	ctx := context.Background()

	// 1. Empty subagents
	emptyArgs := json.RawMessage(`{"subagents": []}`)
	res, err := tool.Execute(ctx, emptyArgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res, "Error: No subagent tasks provided") {
		t.Errorf("expected empty task error, got: %s", res)
	}

	// 2. Exceeds max limit (2 allowed, 3 sent)
	tooManyArgs := json.RawMessage(`{
		"subagents": [
			{"goal": "task 1", "agent_type": "coder"},
			{"goal": "task 2", "agent_type": "coder"},
			{"goal": "task 3", "agent_type": "coder"}
		]
	}`)
	res, err = tool.Execute(ctx, tooManyArgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res, "The maximum configured limit is 2") {
		t.Errorf("expected max limit error, got: %s", res)
	}

	// 3. Non-async subagent type (unsupported)
	unsupportedArgs := json.RawMessage(`{
		"subagents": [
			{"goal": "research something", "agent_type": "unsupported"}
		]
	}`)
	res, err = tool.Execute(ctx, unsupportedArgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res, "does not have async capability") {
		t.Errorf("expected async capability error, got: %s", res)
	}
}

func TestSpawnSubagentsAsyncTool_ExecuteConcurrently(t *testing.T) {
	var running int32
	var maxObservedRunning int32

	tool := BatchSpawnSubagentsTool{
		Runner: func(ctx context.Context, goal string, ctxFiles []string, agentType string) (string, error) {
			cur := atomic.AddInt32(&running, 1)
			for {
				old := atomic.LoadInt32(&maxObservedRunning)
				if cur <= old || atomic.CompareAndSwapInt32(&maxObservedRunning, old, cur) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
			atomic.AddInt32(&running, -1)
			return "completed: " + goal, nil
		},
		MaxConcurrent: 2,
	}

	args := json.RawMessage(`{
		"subagents": [
			{"goal": "build feature A", "agent_type": "coder"},
			{"goal": "research feature B", "agent_type": "researcher"}
		]
	}`)

	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if atomic.LoadInt32(&maxObservedRunning) < 2 {
		t.Errorf("expected tasks to run concurrently, max running was %d", maxObservedRunning)
	}

	if !strings.Contains(res, "completed: build feature A") || !strings.Contains(res, "completed: research feature B") {
		t.Errorf("expected both results in output, got: %s", res)
	}
}
