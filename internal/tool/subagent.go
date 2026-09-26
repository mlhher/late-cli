package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"late/internal/assets"
)

// SubagentRunner executes one subagent run. timeoutOverride carries the
// per-spawn wall-clock budget parsed from the spawn_subagent "timeout"
// argument: nil = no override (the global --subagent-timeout/config value
// applies); a non-positive value = unlimited (no budget for this run);
// a positive value = a per-spawn budget overriding the global one.
type SubagentRunner func(ctx context.Context, goal string, ctxFiles []string, agentType string, timeoutOverride *time.Duration) (string, error)

type SpawnSubagentTool struct {
	Runner SubagentRunner
}

func (t SpawnSubagentTool) Name() string { return "spawn_subagent" }
func (t SpawnSubagentTool) Description() string {
	return "Spawn a specialist subagent to perform a complex task. Use this when you need to isolate a task, such as researching a topic or writing a specific module."
}
func (t SpawnSubagentTool) Parameters() json.RawMessage {
	configs := assets.GetSubagents()
	var enums []string
	var descriptions []string
	for _, c := range configs {
		enums = append(enums, fmt.Sprintf(`"%s"`, c.Name))
		descriptions = append(descriptions, c.Description)
	}

	enumStr := strings.Join(enums, ", ")
	descStr := strings.Join(descriptions, " ")

	paramStr := fmt.Sprintf(`{
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
				"enum": [%s],
				"description": "The type of subagent to spawn. %s"
			},
			"timeout": { 
				"type": "string",
				"description": "Optional wall-clock budget for this subagent run, e.g. \"45m\", \"2h\"; \"0\" = unlimited; omitted = the global --subagent-timeout/config value"
			}
		},
		"required": ["goal", "agent_type"]
	}`, enumStr, descStr)

	return json.RawMessage(paramStr)
}

func (t SpawnSubagentTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if t.Runner == nil {
		return "", fmt.Errorf("subagent runner not configured")
	}

	var params struct {
		Goal      string   `json:"goal"`
		CtxFiles  []string `json:"ctx_files"`
		AgentType string   `json:"agent_type"`
		Timeout   string   `json:"timeout"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %v", err)
	}

	timeoutOverride, err := parseSubagentTimeout(params.Timeout)
	if err != nil {
		// Surface the failure as an error RESULT (nil Go error) so the model
		// can read the hint and retry with a valid duration.
		return fmt.Sprintf("Error: invalid subagent timeout %q — use a duration like 45m, 2h, or 0 for unlimited", params.Timeout), nil
	}

	return t.Runner(ctx, params.Goal, params.CtxFiles, params.AgentType, timeoutOverride)
}

// parseSubagentTimeout parses the optional per-spawn "timeout" argument.
// Empty (absent) → nil override: the global budget applies.
// "0" or a negative duration → pointer to 0 (explicit unlimited).
// A positive duration → pointer to that budget.
// Anything else → a parse error for the caller to surface as an error result.
func parseSubagentTimeout(raw string) (*time.Duration, error) {
	if raw == "" {
		return nil, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return nil, err
	}
	if parsed <= 0 {
		unlimited := time.Duration(0)
		return &unlimited, nil
	}
	return &parsed, nil
}

func (t SpawnSubagentTool) RequiresConfirmation(args json.RawMessage) bool { return false }

func (t SpawnSubagentTool) CallString(args json.RawMessage) string {
	goal := getToolParam(args, "goal")
	if goal == "" {
		goal = "unknown goal"
	}
	return fmt.Sprintf("Spawning subagent for: %s", truncate(goal, 50))
}
