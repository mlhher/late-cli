package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"late/internal/assets"
)

func TestSpawnSubagentsTool_Schema(t *testing.T) {
	tool := SpawnSubagentsTool{
		MaxConcurrent: 3,
	}

	if tool.Name() != "spawn_subagents" {
		t.Fatalf("expected tool name 'spawn_subagents', got %s", tool.Name())
	}

	desc := tool.Description()
	if !strings.Contains(desc, "3") {
		t.Errorf("expected description to mention max concurrent limit '3', got: %s", desc)
	}
	if strings.Contains(desc, "cannot run concurrently") || strings.Contains(desc, "async capabilities") {
		t.Errorf("expected description without non-async warnings when all subagents are async, got: %s", desc)
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

	agentDesc := schema.Properties.Subagents.Items.Properties.AgentType.Description
	if !strings.Contains(agentDesc, "'coder' for writing/modifying code.") || !strings.Contains(agentDesc, "'researcher' for codebase analysis.") {
		t.Errorf("expected agent_type description to include subagent descriptions, got: %s", agentDesc)
	}
	if strings.Contains(agentDesc, "cannot run concurrently") || strings.Contains(agentDesc, "async capabilities") {
		t.Errorf("expected agent_type description without async warning when all subagents are async, got: %s", agentDesc)
	}
}

func TestSpawnSubagentsTool_SchemaWithSyncOnlyAgent(t *testing.T) {
	tool := SpawnSubagentsTool{
		MaxConcurrent: 4,
		Subagents: func() []assets.SubagentConfig {
			return []assets.SubagentConfig{
				{Name: "coder", Description: `'coder' for "fast" edits.`, Async: true},
				{Name: "browser", Description: "'browser' for exclusive UI sessions.", Async: false},
			}
		},
	}

	desc := tool.Description()
	if !strings.Contains(desc, "browser") || !strings.Contains(desc, "cannot run concurrently") {
		t.Errorf("expected description to mention sync-only agent 'browser', got: %s", desc)
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
		t.Fatalf("failed to parse schema JSON (must escape quotes cleanly): %v", err)
	}

	enums := schema.Properties.Subagents.Items.Properties.AgentType.Enum
	if len(enums) != 2 || enums[0] != "coder" || enums[1] != "browser" {
		t.Fatalf("expected both coder and browser in enum, got %v", enums)
	}

	agentDesc := schema.Properties.Subagents.Items.Properties.AgentType.Description
	if !strings.Contains(agentDesc, "browser") || !strings.Contains(agentDesc, "sequentially") {
		t.Errorf("expected agent_type description to note 'browser' runs sequentially, got: %s", agentDesc)
	}
}

func TestSpawnSubagentsTool_Validation(t *testing.T) {
	var calls int32
	tool := SpawnSubagentsTool{
		Runner: func(ctx context.Context, goal string, ctxFiles []string, agentType string) (string, error) {
			atomic.AddInt32(&calls, 1)
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

	// 2. Unknown agent_type
	unknownArgs := json.RawMessage(`{
		"subagents": [
			{"goal": "research something", "agent_type": "unsupported"}
		]
	}`)
	res, err = tool.Execute(ctx, unknownArgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res, "unknown agent_type 'unsupported'") {
		t.Errorf("expected unknown agent_type error, got: %s", res)
	}

	// 3. Exceeds MaxConcurrent per-call cap
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
	if !strings.Contains(res, "limit is 2 per call") || !strings.Contains(res, "do not combine multiple steps") {
		t.Errorf("expected per-call limit error with anti-cramming guidance, got: %s", res)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("expected Runner not to be invoked on validation errors, got %d calls", got)
	}
}

func TestSpawnSubagentsTool_SingleTaskFastPath(t *testing.T) {
	tool := SpawnSubagentsTool{
		Runner: func(ctx context.Context, goal string, ctxFiles []string, agentType string) (string, error) {
			return fmt.Sprintf("single-result:%s:%s", agentType, goal), nil
		},
		MaxConcurrent: 3,
	}

	args := json.RawMessage(`{"subagents":[{"goal":"inspect auth","agent_type":"researcher"}]}`)
	if cs := tool.CallString(args); cs != "Spawning subagent for: inspect auth" {
		t.Errorf("CallString() = %q, want %q", cs, "Spawning subagent for: inspect auth")
	}

	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != "single-result:researcher:inspect auth" {
		t.Errorf("expected raw single-task runner result without batch header, got: %q", res)
	}
}

func TestSpawnSubagentsTool_ExecuteConcurrentlyAndOrder(t *testing.T) {
	var running int32
	var maxObservedRunning int32

	tool := SpawnSubagentsTool{
		Runner: func(ctx context.Context, goal string, ctxFiles []string, agentType string) (string, error) {
			cur := atomic.AddInt32(&running, 1)
			for {
				old := atomic.LoadInt32(&maxObservedRunning)
				if cur <= old || atomic.CompareAndSwapInt32(&maxObservedRunning, old, cur) {
					break
				}
			}
			time.Sleep(40 * time.Millisecond)
			atomic.AddInt32(&running, -1)
			return "completed: " + goal, nil
		},
		MaxConcurrent: 2,
	}

	args := json.RawMessage(`{
		"subagents": [
			{"goal": "task 1", "agent_type": "coder"},
			{"goal": "task 2", "agent_type": "researcher"}
		]
	}`)

	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := atomic.LoadInt32(&maxObservedRunning); got != 2 {
		t.Errorf("expected max concurrent running to be 2, got %d", got)
	}

	firstLine, _, _ := strings.Cut(res, "\n")
	if firstLine != "All 2 subagent tasks have finished executing" {
		t.Errorf("unexpected header line %q (should have no trailing colon)", firstLine)
	}

	pos1 := strings.Index(res, "### Subagent 1 (coder) - Goal: task 1")
	pos2 := strings.Index(res, "### Subagent 2 (researcher) - Goal: task 2")
	if pos1 < 0 || pos2 <= pos1 {
		t.Errorf("expected ordered outputs 1..2, got positions %d,%d in:\n%s", pos1, pos2, res)
	}
	for _, g := range []string{"completed: task 1", "completed: task 2"} {
		if !strings.Contains(res, g) {
			t.Errorf("missing %q in output:\n%s", g, res)
		}
	}
}

func TestSpawnSubagentsTool_MixedAsyncAndSync(t *testing.T) {
	var totalRunning int32
	var syncOverlap int32
	var asyncMax int32

	tool := SpawnSubagentsTool{
		MaxConcurrent: 4,
		Subagents: func() []assets.SubagentConfig {
			return []assets.SubagentConfig{
				{Name: "coder", Description: "coder", Async: true},
				{Name: "syncer", Description: "syncer", Async: false},
			}
		},
		Runner: func(ctx context.Context, goal string, ctxFiles []string, agentType string) (string, error) {
			cur := atomic.AddInt32(&totalRunning, 1)
			defer atomic.AddInt32(&totalRunning, -1)

			if agentType == "syncer" {
				if cur > 1 {
					atomic.AddInt32(&syncOverlap, 1)
				}
				time.Sleep(20 * time.Millisecond)
				if atomic.LoadInt32(&totalRunning) > 1 {
					atomic.AddInt32(&syncOverlap, 1)
				}
			} else {
				for {
					old := atomic.LoadInt32(&asyncMax)
					if cur <= old || atomic.CompareAndSwapInt32(&asyncMax, old, cur) {
						break
					}
				}
				time.Sleep(35 * time.Millisecond)
			}
			return "ok:" + goal, nil
		},
	}

	args := json.RawMessage(`{
		"subagents": [
			{"goal": "s1", "agent_type": "syncer"},
			{"goal": "a1", "agent_type": "coder"},
			{"goal": "s2", "agent_type": "syncer"},
			{"goal": "a2", "agent_type": "coder"}
		]
	}`)

	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt32(&syncOverlap) != 0 {
		t.Errorf("expected zero concurrency overlap during sync-only tasks, got %d", syncOverlap)
	}
	if atomic.LoadInt32(&asyncMax) < 2 {
		t.Errorf("expected async tasks to run concurrently, max was %d", asyncMax)
	}

	pos1 := strings.Index(res, "### Subagent 1 (syncer) - Goal: s1")
	pos2 := strings.Index(res, "### Subagent 2 (coder) - Goal: a1")
	pos3 := strings.Index(res, "### Subagent 3 (syncer) - Goal: s2")
	pos4 := strings.Index(res, "### Subagent 4 (coder) - Goal: a2")
	if pos1 < 0 || pos2 <= pos1 || pos3 <= pos2 || pos4 <= pos3 {
		t.Errorf("expected original input ordering preserved, got:\n%s", res)
	}
}
