package tool

import (
	"context"
	"encoding/json"
	"fmt"
)

type SubagentRunner func(ctx context.Context, goal string, ctxFiles []string, agentType string) (string, error)

type SpawnSubagentTool struct {
	Runner SubagentRunner
}

func (t SpawnSubagentTool) Name() string { return "spawn_subagent" }
func (t SpawnSubagentTool) Description() string {
	return "Spawn a specialist subagent to perform a complex task. Use this when you need to isolate a task, such as researching a topic or writing a specific module."
}
func (t SpawnSubagentTool) Parameters() json.RawMessage {
	// TODO: add reviewer, committer
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"goal": { "type": "string", "description": "The specific goal or instruction for the subagent" },
			"ctx_files": { 
				"type": "array", 
				"items": { "type": "string" },
				"description": "List of file paths to provide as context to the subagent" 
			},
			"agent_type": { 
				"type": "string", 
				"enum": ["coder", "researcher"],
				"description": "The type of subagent to spawn. 'coder' for writing/modifying code, 'researcher' for codebase analysis."
			}
		},
		"required": ["goal", "agent_type"]
	}`)
}

func (t SpawnSubagentTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if t.Runner == nil {
		return "", fmt.Errorf("subagent runner not configured")
	}

	var params struct {
		Goal      string   `json:"goal"`
		CtxFiles  []string `json:"ctx_files"`
		AgentType string   `json:"agent_type"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %v", err)
	}

	return t.Runner(ctx, params.Goal, params.CtxFiles, params.AgentType)
}

func (t SpawnSubagentTool) RequiresConfirmation(args json.RawMessage) bool { return false }

func (t SpawnSubagentTool) CallString(args json.RawMessage) string {
	goal := getToolParam(args, "goal")
	if goal == "" {
		goal = "unknown goal"
	}
	return fmt.Sprintf("Spawning subagent for: %s", truncate(goal, 50))
}
