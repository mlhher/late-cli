package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"late/internal/client"
	"late/internal/common"
)

// InlineTool describes a plugin-declared tool that runs as a local
// script invocation. Each inline tool becomes a Tool the agent can
// call directly, without an MCP server wrapper.
//
// The Runner signature mirrors common.ToolRunner so the tool can be
// chained into the same ToolMiddleware pipeline that MCP-backed tools
// use.
//
// Name is sanitized for OpenAI-compatible endpoints (which reject ':'
// and names over common.MaxToolNameLen): the parts are joined as
// "<plugin>__<tool>" with every character outside [A-Za-z0-9_-]
// replaced by '_', then capped at 64 chars. Rare collisions between
// distinct combos that sanitize identically are resolved with a
// deterministic hash suffix.
type InlineTool struct {
	Name        string          // namespaced, endpoint-safe tool name
	Description string          // shown in the model's tool definitions
	Parameters  json.RawMessage // raw JSON Schema fragment
	Runner      func(ctx context.Context, call client.ToolCall) (string, error)
}

// GetInlineTools returns every inline tool declared across all enabled
// plugins. Names are namespaced as "<plugin>__<tool>" (sanitized — see
// InlineTool) so two plugins declaring the same short tool name cannot
// collide, and so the names are accepted by OpenAI-compatible
// endpoints. Disabled and nil-manifest plugins are skipped; a plugin
// whose script path fails the containment check is silently skipped
// with a stderr warning rather than crashing discovery.
//
// used pre-seeds names already taken by tools from other sources (e.g.
// MCP-backed tools) so an inline tool can never silently overwrite one
// registered from elsewhere; assigned names are recorded into it. A nil
// map dedupes only within this call's own results, same as before.
func (pm *PluginManager) GetInlineTools(used map[string]bool) []InlineTool {
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
				Name:        common.NamespaceToolName(p.Name, t.Name),
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

	// Resolve collisions between (rare) pairs of distinct plugin:tool
	// combos that sanitize to the same name, e.g. "a-b:c" vs "a:b-c" —
	// both become "a-b__c". The first occurrence keeps the name; later
	// duplicates get a deterministic hash suffix.
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	uniq := common.DeduplicateToolNames(names, used)
	for i := range tools {
		tools[i].Name = uniq[i]
	}
	return tools
}
