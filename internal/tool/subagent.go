package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"late/internal/assets"
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
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %v", err)
	}

	// Fresh-spawn input validation: every failure is an error RESULT (nil
	// Go error), so the model can read the hint and retry with fixed
	// arguments instead of the spawn failing as a bare Go error — or an
	// empty goal spawning a child whose only instruction is "Goal: ".
	if hint := validateFreshSpawnArgs(params.Goal, params.AgentType, params.CtxFiles); hint != "" {
		return hint, nil
	}

	return t.Runner(ctx, params.Goal, params.CtxFiles, params.AgentType)
}

// validateFreshSpawnArgs checks the fresh-spawn argument surface the schema
// cannot guarantee (a provider may not enforce "required", and empty strings
// satisfy JSON types). Returns an error-RESULT string when validation fails —
// the spawn never reaches the runner — or "" when the arguments are usable:
//
//   - goal: non-empty after trimming. An empty goal would spawn a child whose
//     only instruction is "Goal: ".
//   - agent_type: one of the configured subagent types (the same source the
//     schema enum is built from). Without this check an unknown type fails
//     deeper in the runner with a bare Go error instead of a retryable hint.
//   - ctx_files: every entry must exist and be a readable FILE. Missing
//     entries used to be dropped silently when the goal message was built
//     (os.ReadFile error → skipped), so the model believed context was
//     attached when it was not.
func validateFreshSpawnArgs(goal, agentType string, ctxFiles []string) string {
	if strings.TrimSpace(goal) == "" {
		return `Error: empty goal — pass the task for the subagent in the "goal" field`
	}

	if strings.TrimSpace(agentType) == "" {
		return fmt.Sprintf("Error: empty agent_type — valid types: %s", configuredAgentTypes())
	}
	if !isConfiguredAgentType(agentType) {
		return fmt.Sprintf("Error: unknown agent_type %q — valid types: %s", agentType, configuredAgentTypes())
	}

	if len(ctxFiles) > 0 {
		var problems []string
		for _, f := range ctxFiles {
			info, err := os.Stat(f)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%q does not exist (%v)", f, err))
				continue
			}
			if info.IsDir() {
				problems = append(problems, fmt.Sprintf("%q is a directory, not a file", f))
			}
		}
		if len(problems) > 0 {
			return fmt.Sprintf("Error: unusable ctx_files entries — %s. Remove them or pass existing file paths, then spawn again",
				strings.Join(problems, "; "))
		}
	}
	return ""
}

// isConfiguredAgentType reports whether name is a configured subagent type.
// A broken/empty embedded registry disables the check (spawns then fail
// deeper in the runner with the historical "unknown agent type" error
// instead of being bricked at the door).
func isConfiguredAgentType(name string) bool {
	configs := assets.GetSubagents()
	if len(configs) == 0 {
		return true
	}
	for _, c := range configs {
		if c.Name == name {
			return true
		}
	}
	return false
}

// configuredAgentTypes renders the configured subagent type names for
// model-facing hints (falls back to a pointer at the config when the
// embedded registry is unavailable).
func configuredAgentTypes() string {
	configs := assets.GetSubagents()
	names := make([]string, 0, len(configs))
	for _, c := range configs {
		names = append(names, c.Name)
	}
	if len(names) == 0 {
		return "see the subagent registry"
	}
	return strings.Join(names, ", ")
}

func (t SpawnSubagentTool) RequiresConfirmation(args json.RawMessage) bool { return false }

func (t SpawnSubagentTool) CallString(args json.RawMessage) string {
	goal := getToolParam(args, "goal")
	if goal == "" {
		goal = "unknown goal"
	}
	return fmt.Sprintf("Spawning subagent for: %s", truncate(goal, 50))
}
