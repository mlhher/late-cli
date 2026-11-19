package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

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

type SubagentTask struct {
	Goal      string   `json:"goal"`
	CtxFiles  []string `json:"ctx_files"`
	AgentType string   `json:"agent_type"`
}

type SpawnSubagentsTool struct {
	Runner        SubagentRunner
	MaxConcurrent int
	// Subagents optionally overrides assets.GetSubagents for tests.
	Subagents func() []assets.SubagentConfig
}

func (t SpawnSubagentsTool) subagents() []assets.SubagentConfig {
	if t.Subagents != nil {
		return t.Subagents()
	}
	return assets.GetSubagents()
}

func (t SpawnSubagentsTool) maxLimit() int {
	if t.MaxConcurrent < 1 {
		return 1
	}
	return t.MaxConcurrent
}

func (t SpawnSubagentsTool) Name() string { return "spawn_subagents" }

func (t SpawnSubagentsTool) Description() string {
	configs := t.subagents()
	var syncOnlyNames []string
	for _, c := range configs {
		if !c.Async {
			syncOnlyNames = append(syncOnlyNames, c.Name)
		}
	}
	maxLimit := t.maxLimit()
	desc := fmt.Sprintf("Spawn one or more specialist subagents. Pass 1 entry for a single or sequential step, or up to %d entries to run independent steps in parallel and return all results in one turn. If more independent steps remain, split them into batches of at most %d across consecutive calls. Keep each subagent focused on one atomic step.", maxLimit, maxLimit)
	if len(syncOnlyNames) > 0 {
		desc += fmt.Sprintf(" Note: %s cannot run concurrently and will run sequentially.", strings.Join(syncOnlyNames, ", "))
	}
	return desc
}

func (t SpawnSubagentsTool) Parameters() json.RawMessage {
	configs := t.subagents()
	enums := make([]string, 0, len(configs))
	var descriptions []string
	var syncOnlyNames []string
	for _, c := range configs {
		enums = append(enums, c.Name)
		if c.Description != "" {
			descriptions = append(descriptions, c.Description)
		}
		if !c.Async {
			syncOnlyNames = append(syncOnlyNames, c.Name)
		}
	}

	agentTypeDesc := "The type of subagent to spawn."
	if len(descriptions) > 0 {
		agentTypeDesc += " " + strings.Join(descriptions, " ")
	}
	if len(syncOnlyNames) > 0 {
		agentTypeDesc += " Note: " + strings.Join(syncOnlyNames, ", ") + " run sequentially rather than concurrently."
	}

	maxLimit := t.maxLimit()
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"subagents": map[string]any{
				"type":        "array",
				"description": fmt.Sprintf("One or more subagent tasks to execute (maximum %d per call). Pass 1 entry for a sequential step, or up to %d entries for independent steps that do not edit the same files.", maxLimit, maxLimit),
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"goal": map[string]any{
							"type":        "string",
							"description": "The specific goal or instruction for this subagent (one atomic step)",
						},
						"ctx_files": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "List of file paths to provide as context to the subagent",
						},
						"agent_type": map[string]any{
							"type":        "string",
							"enum":        enums,
							"description": agentTypeDesc,
						},
					},
					"required": []string{"goal", "agent_type"},
				},
			},
		},
		"required": []string{"subagents"},
	}

	b, _ := json.Marshal(schema)
	return json.RawMessage(b)
}

func (t SpawnSubagentsTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if t.Runner == nil {
		return "", fmt.Errorf("subagent runner not configured")
	}

	var params struct {
		Subagents []SubagentTask `json:"subagents"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %v", err)
	}

	taskList := params.Subagents
	if len(taskList) == 0 {
		return "Error: No subagent tasks provided. Please provide at least one subagent task to execute.", nil
	}

	configs := t.subagents()
	knownMap := make(map[string]assets.SubagentConfig, len(configs))
	knownNames := make([]string, 0, len(configs))
	for _, c := range configs {
		knownMap[c.Name] = c
		knownNames = append(knownNames, c.Name)
	}

	for i, task := range taskList {
		if _, ok := knownMap[task.AgentType]; !ok {
			return fmt.Sprintf("Error: Subagent at index %d specified unknown agent_type '%s'. Valid types are: %s.", i, task.AgentType, strings.Join(knownNames, ", ")), nil
		}
	}

	maxLimit := t.maxLimit()
	if len(taskList) > maxLimit {
		return fmt.Sprintf("Error: Requested %d subagents in one call, but the limit is %d per call. Call spawn_subagents with at most %d entries now and run the remaining steps in the next call — do not combine multiple steps into one goal.", len(taskList), maxLimit, maxLimit), nil
	}

	if len(taskList) == 1 {
		task := taskList[0]
		return t.Runner(ctx, task.Goal, task.CtxFiles, task.AgentType)
	}

	var asyncIndices []int
	var syncIndices []int
	for i, task := range taskList {
		if knownMap[task.AgentType].Async {
			asyncIndices = append(asyncIndices, i)
		} else {
			syncIndices = append(syncIndices, i)
		}
	}

	type taskResult struct {
		index     int
		agentType string
		goal      string
		output    string
		err       error
	}

	results := make([]taskResult, len(taskList))

	if len(asyncIndices) > 0 {
		var wg sync.WaitGroup
		for _, idx := range asyncIndices {
			wg.Add(1)
			go func(idx int, tsk SubagentTask) {
				defer wg.Done()

				out, err := t.Runner(ctx, tsk.Goal, tsk.CtxFiles, tsk.AgentType)
				results[idx] = taskResult{
					index:     idx + 1,
					agentType: tsk.AgentType,
					goal:      tsk.Goal,
					output:    out,
					err:       err,
				}
			}(idx, taskList[idx])
		}
		wg.Wait()
	}

	for _, idx := range syncIndices {
		tsk := taskList[idx]
		if err := ctx.Err(); err != nil {
			results[idx] = taskResult{
				index:     idx + 1,
				agentType: tsk.AgentType,
				goal:      tsk.Goal,
				err:       err,
			}
			continue
		}
		out, err := t.Runner(ctx, tsk.Goal, tsk.CtxFiles, tsk.AgentType)
		results[idx] = taskResult{
			index:     idx + 1,
			agentType: tsk.AgentType,
			goal:      tsk.Goal,
			output:    out,
			err:       err,
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("All %d subagent tasks have finished executing\n\n", len(taskList)))
	for _, res := range results {
		sb.WriteString(fmt.Sprintf("### Subagent %d (%s) - Goal: %s\n", res.index, res.agentType, truncate(res.goal, 60)))
		if res.err != nil {
			sb.WriteString(fmt.Sprintf("Error: %v\n\n", res.err))
		} else {
			sb.WriteString(res.output)
			sb.WriteString("\n\n")
		}
		sb.WriteString("---\n\n")
	}

	return strings.TrimSpace(sb.String()), nil
}

func (t SpawnSubagentsTool) RequiresConfirmation(args json.RawMessage) bool {
	return false
}

func (t SpawnSubagentsTool) CallString(args json.RawMessage) string {
	var params struct {
		Subagents []struct {
			Goal string `json:"goal"`
		} `json:"subagents"`
	}
	_ = json.Unmarshal(args, &params)
	list := params.Subagents
	if len(list) == 0 {
		return "Spawning subagents"
	}
	if len(list) == 1 {
		goal := list[0].Goal
		if goal == "" {
			goal = "unknown goal"
		}
		return fmt.Sprintf("Spawning subagent for: %s", truncate(goal, 50))
	}
	goals := make([]string, len(list))
	for i, item := range list {
		goals[i] = truncate(item.Goal, 30)
	}
	return fmt.Sprintf("Spawning %d subagents: [%s]", len(list), strings.Join(goals, ", "))
}
