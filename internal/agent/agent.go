package agent

import (
	"encoding/json"
	"fmt"
	"late/internal/assets"
	"late/internal/client"
	"late/internal/common"
	"late/internal/executor"
	"late/internal/orchestrator"
	"late/internal/session"
	"late/internal/tui"
	"os"
)

// NewSubagentOrchestrator creates a new BaseOrchestrator for a subagent.
func NewSubagentOrchestrator(
	c *client.Client,
	goal string,
	ctxFiles []string,
	agentType string,
	enabledTools map[string]bool,
	injectCWD bool,
	gemmaThinking bool,
	maxTurns int,
	parent common.Orchestrator,
	messenger tui.Messenger,
) (common.Orchestrator, error) {
	// 1. Determine System Prompt
	systemPrompt := ""
	if agentType == "coder" {
		content, err := assets.PromptsFS.ReadFile("prompts/instruction-coding.md")
		if err != nil {
			return nil, fmt.Errorf("failed to load embedded subagent prompt: %w", err)
		}
		systemPrompt = string(content)

		if injectCWD {
			cwd, err := os.Getwd()
			if err == nil {
				systemPrompt = common.ReplacePlaceholders(systemPrompt, map[string]string{
					"${{CWD}}": cwd,
				})
			}
		}

		if gemmaThinking {
			systemPrompt = "<|think|>" + systemPrompt
		}
	} else {
		// TODO: reviewer, committer
		return nil, fmt.Errorf("unknown agent type: %s", agentType)
	}

	// 2. Create Session
	// Subagents should not persist their history to the sessions directory
	sess := session.New(c, "", []client.ChatMessage{}, systemPrompt, true)

	// Inherit context recovery setting from parent so subagents also prune
	// when --context-rot is active. Without this, each subagent slot fills
	// independently and produces the same 400 errors the flag was built to prevent.
	if p, ok := parent.(*orchestrator.BaseOrchestrator); ok {
		sess.ContextRecoveryEnabled = p.Session().ContextRecoveryEnabled
	}

	// Inherit all tools from parent (including MCP tools)
	if parent != nil && parent.Registry() != nil {
		for _, t := range parent.Registry().All() {
			// Skip spawn_subagent and write_implementation_plan to prevent recursion/confusion
			name := t.Name()
			if name == "spawn_subagent" || name == "write_implementation_plan" {
				continue
			}
			sess.Registry.Register(t)
		}
	}

	// Always ensure coder subagents have the full toolset (not just planning tools)
	if agentType == "coder" {
		executor.RegisterTools(sess.Registry, enabledTools, false)
	}

	// 3. Construct Initial Context
	initialMsg := fmt.Sprintf("Goal: %s\n\n", goal)
	if len(ctxFiles) > 0 {
		initialMsg += "Context Files:\n"
		for _, f := range ctxFiles {
			content, err := os.ReadFile(f)
			if err == nil {
				initialMsg += fmt.Sprintf("- %s:\n```\n%s\n```\n", f, string(content))
			}
		}
	}

	if err := sess.AddUserMessage(initialMsg); err != nil {
		return nil, fmt.Errorf("failed to add initial message: %w", err)
	}

	// 4. Create Orchestrator
	id := fmt.Sprintf("subagent-%d", len(parent.Children()))
	mws := parent.Middlewares()

	if messenger != nil {
		mws = []common.ToolMiddleware{
			tui.TUIConfirmMiddleware(messenger, sess.Registry),
		}
	}

	child := orchestrator.NewBaseOrchestrator(id, sess, mws, maxTurns)
	child.SetContext(parent.Context())

	if p, ok := parent.(*orchestrator.BaseOrchestrator); ok {
		p.AddChild(child)
	}

	return child, nil
}

func FormatToolConfirmPrompt(tc client.ToolCall) string {
	var jsonObj map[string]interface{}
	args := tc.Function.Arguments
	if err := json.Unmarshal([]byte(args), &jsonObj); err == nil {
		pretty, _ := json.MarshalIndent(jsonObj, "", "  ")
		args = string(pretty)
	}
	return fmt.Sprintf("Execute **%s**:\n\n```json\n%s\n```", tc.Function.Name, args)
}
