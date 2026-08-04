package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"late/internal/client"
)

// InlineTool describes a plugin-declared tool that runs as a local
// script invocation. Each inline tool becomes a Tool the agent can
// call directly, without an MCP server wrapper.
//
// The Runner signature mirrors common.ToolRunner so the tool can be
// chained into the same ToolMiddleware pipeline that MCP-backed tools
// use.
type InlineTool struct {
	Name        string          // "<plugin>:<tool>" — namespaced to avoid collisions
	Description string          // shown in the model's tool definitions
	Parameters  json.RawMessage // raw JSON Schema fragment
	Runner      func(ctx context.Context, call client.ToolCall) (string, error)
}

// GetInlineTools aggregates every inline tool declared across all
// enabled plugins. Names are always namespaced as "<plugin>:<tool>"
// to prevent collisions when two plugins declare a tool with the
// same short name. Disabled and nil-manifest plugins are skipped; a
// plugin whose script path fails containment is silently skipped with
// a stderr warning rather than crashing discovery.
func (pm *PluginManager) GetInlineTools() []InlineTool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	var tools []InlineTool
	for _, p := range pm.plugins {
		if !p.Enabled || p.Late == nil {
			continue
		}
		for _, t := range p.Late.Tools {
			if t.Name == "" || t.Script == "" {
				continue
			}
			// Validate path containment up-front so we can skip tools
			// whose script would escape the plugin directory.
			if _, err := resolveHookPath(p.Path, t.Script); err != nil {
				fmt.Fprintf(os.Stderr, "[tools] plugin %s tool %q: %v\n", p.Name, t.Name, err)
				continue
			}

			// Capture loop vars by value for the closure.
			pluginDir := p.Path
			scriptPath := t.Script
			tools = append(tools, InlineTool{
				Name:        p.Name + ":" + t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
				Runner: func(ctx context.Context, call client.ToolCall) (string, error) {
					// Tool arguments feed directly into the script stdin.
					payload := []byte(call.Function.Arguments)
					if !json.Valid(payload) {
						// Fallback for tools that don't get a tool-args JSON
						// object — pass an empty object so the script has a
						// well-formed stdin.
						payload = []byte("{}")
					}
					return runHook(ctx, pluginDir, scriptPath, payload)
				},
			})
		}
	}
	return tools
}
