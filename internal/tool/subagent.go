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
	return "Spawn a specialist subagent to perform a complex task. Use this when you need to isolate a task, such as researching a topic or writing a specific module. Note that this spawns only one subagent synchronously. To spawn multiple subagents at once use 'batch_spawn_subagents' instead."
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

type BatchSpawnSubagentsTool struct {
	Runner        SubagentRunner
	MaxConcurrent int
}

// Deprecated: SpawnSubagentsAsyncTool is an alias for BatchSpawnSubagentsTool.
type SpawnSubagentsAsyncTool = BatchSpawnSubagentsTool

func (t BatchSpawnSubagentsTool) Name() string { return "batch_spawn_subagents" }

func (t BatchSpawnSubagentsTool) Description() string {
	_, asyncList := getAsyncSubagentsMapAndList()
	max := t.MaxConcurrent
	if max <= 0 {
		max = 2
	}
	return fmt.Sprintf("Spawn multiple subagents concurrently to execute independent tasks in parallel and return all outputs at once in a single turn (preserving KV-cache). Only subagent types with async capabilities are permitted. These are currently %s. All others cannot be spawned async and will return an error if attempted to do so. Maximum allowed concurrent subagents: %d.", asyncList, max)
}

func (t BatchSpawnSubagentsTool) Parameters() json.RawMessage {
	configs := assets.GetSubagents()
	var asyncEnums []string
	var asyncNames []string
	for _, c := range configs {
		if c.Async {
			asyncEnums = append(asyncEnums, fmt.Sprintf(`"%s"`, c.Name))
			asyncNames = append(asyncNames, c.Name)
		}
	}
	enumStr := strings.Join(asyncEnums, ", ")
	asyncList := strings.Join(asyncNames, ", ")
	max := t.MaxConcurrent
	if max <= 0 {
		max = 2
	}

	paramStr := fmt.Sprintf(`{
		"type": "object",
		"properties": {
			"subagents": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"goal": { "type": "string", "description": "The specific goal or instruction for this subagent" },
						"ctx_files": { 
							"type": "array", 
							"items": { "type": "string" }, 
							"description": "List of file paths to provide as context to the subagent" 
						},
						"agent_type": { 
							"type": "string", 
							"enum": [%s],
							"description": "The type of subagent to spawn. Only subagent types with async capabilities are permitted. These are currently %s. All others cannot be spawned async and will return an error if attempted to do so."
						}
					},
					"required": ["goal", "agent_type"]
				},
				"description": "List of subagent tasks to execute concurrently. Maximum %d tasks."
			}
		},
		"required": ["subagents"]
	}`, enumStr, asyncList, max)

	return json.RawMessage(paramStr)
}

func (t BatchSpawnSubagentsTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if t.Runner == nil {
		return "", fmt.Errorf("subagent runner not configured")
	}

	maxLimit := t.MaxConcurrent
	if maxLimit <= 0 {
		maxLimit = 2
	}

	var params struct {
		Subagents []SubagentTask `json:"subagents"`
		Tasks     []SubagentTask `json:"tasks"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %v", err)
	}

	taskList := params.Subagents
	if len(taskList) == 0 {
		taskList = params.Tasks
	}

	if len(taskList) == 0 {
		return "Error: No subagent tasks provided. Please provide a list of subagent tasks to execute.", nil
	}

	asyncMap, asyncList := getAsyncSubagentsMapAndList()
	for i, task := range taskList {
		if !asyncMap[task.AgentType] {
			return fmt.Sprintf("Error: Subagent at index %d specified agent_type '%s', which does not have async capability. Only subagent types with async capabilities are permitted. These are currently %s. All others cannot be spawned async and will return an error if attempted to do so.", i, task.AgentType, asyncList), nil
		}
	}

	if len(taskList) > maxLimit {
		return fmt.Sprintf("Error: Cannot spawn %d subagents concurrently. The maximum configured limit is %d concurrent subagents. Please reduce the number of tasks or execute them in batches of at most %d.", len(taskList), maxLimit, maxLimit), nil
	}

	type taskResult struct {
		index     int
		agentType string
		goal      string
		output    string
		err       error
	}

	results := make([]taskResult, len(taskList))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxLimit)

	for i, task := range taskList {
		wg.Add(1)
		go func(idx int, tsk SubagentTask) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			out, err := t.Runner(ctx, tsk.Goal, tsk.CtxFiles, tsk.AgentType)
			results[idx] = taskResult{
				index:     idx + 1,
				agentType: tsk.AgentType,
				goal:      tsk.Goal,
				output:    out,
				err:       err,
			}
		}(i, task)
	}

	wg.Wait()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("All %d async subagent tasks have finished executing:\n\n", len(taskList)))
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

func (t BatchSpawnSubagentsTool) RequiresConfirmation(args json.RawMessage) bool {
	return false
}

func (t BatchSpawnSubagentsTool) CallString(args json.RawMessage) string {
	var params struct {
		Subagents []struct {
			Goal string `json:"goal"`
		} `json:"subagents"`
		Tasks []struct {
			Goal string `json:"goal"`
		} `json:"tasks"`
	}
	_ = json.Unmarshal(args, &params)
	list := params.Subagents
	if len(list) == 0 {
		list = params.Tasks
	}
	if len(list) == 0 {
		return "Spawning subagents asynchronously"
	}
	goals := make([]string, len(list))
	for i, item := range list {
		goals[i] = truncate(item.Goal, 30)
	}
	return fmt.Sprintf("Spawning %d subagents asynchronously: [%s]", len(list), strings.Join(goals, ", "))
}

func getAsyncSubagentsMapAndList() (map[string]bool, string) {
	configs := assets.GetSubagents()
	asyncMap := make(map[string]bool)
	var asyncNames []string
	for _, c := range configs {
		if c.Async {
			asyncMap[c.Name] = true
			asyncNames = append(asyncNames, c.Name)
		}
	}
	return asyncMap, strings.Join(asyncNames, ", ")
}
