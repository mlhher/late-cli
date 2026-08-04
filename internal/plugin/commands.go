package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// HandleCommand looks up a registered slash-command handler across all
// enabled plugins and, if found, runs the handler script with the
// trailing positional arguments (JSON-encoded as a string array on stdin).
//
// Returns:
//
//	("output",  true,  err)  — plugin declared this command and a handler ran
//	("",        false, nil)  — no plugin registered this name (fall through)
//	("",        false, nil)  — plugin registered it but didn't set a Handler
//	                             (legacy "plain prompt" dispatch behavior)
//
// Leading "/" is stripped on both sides before comparison so plugin authors
// can declare commands as either "/weather" or "weather" indistinguishably.
// When two enabled plugins declare the same command name, the first one in
// sorted-by-name order wins; the duplicate is logged to stderr so authors
// notice conflicts immediately.
func (pm *PluginManager) HandleCommand(ctx context.Context, cmdName string, args []string) (string, bool, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	normalized := strings.TrimPrefix(cmdName, "/")

	// Reuse the sorted-copy helper so we don't reinvent the sort+copy here;
	// we already hold RLock, so call allLocked() rather than All() — All()
	// would take a second RLock that can deadlock against a queued writer.
	plugins := pm.allLocked()

	var (
		matched       bool
		firstOwner    string
		firstMatchCmd LateCommandManifest
		firstMatchDir string
	)

	for _, p := range plugins {
		if !p.Enabled || p.Late == nil {
			continue
		}
		for _, c := range p.Late.Commands {
			if strings.TrimPrefix(c.Name, "/") != normalized {
				continue
			}
			if !matched {
				matched = true
				firstOwner = p.Name
				firstMatchCmd = c
				firstMatchDir = p.Path
				continue
			}
			// Already matched once — log duplicate registrations.
			fmt.Fprintf(os.Stderr,
				"[commands] duplicate registration for %q: %q wins (shadows %q from %q)\n",
				cmdName, firstOwner, p.Name, c.Name)
		}
	}

	if !matched {
		return "", false, nil
	}
	if firstMatchCmd.Handler == "" {
		// Legacy "no handler declared" — the TUI should fall through to
		// plain-prompt dispatch.
		return "", false, nil
	}

	payload, _ := json.Marshal(args)
	out, err := runHook(ctx, firstMatchDir, firstMatchCmd.Handler, payload)
	if err != nil {
		return "", true, fmt.Errorf("plugin command %q failed: %w", cmdName, err)
	}
	return out, true, nil
}
