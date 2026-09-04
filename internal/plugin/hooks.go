package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"late/internal/client"
	"late/internal/common"
)

// Per-hook execution limits.
const (
	hookTimeout = 15 * time.Second
	// maxStdoutBytes bounds captured stdout during the copy from the child
	// process. Stdout is the hook's actual return channel — it REPLACES
	// tool call arguments, tool results, or the outgoing message text (see
	// BuildHookMiddlewares/CallOnToolResultHooks/HookedMessage) — so this
	// mirrors hookStdinMax's generosity rather than maxStderrBytes' much
	// smaller log-line budget; it only guards against pathological
	// (multi-hundred-MB) output, not realistic payload sizes.
	maxStdoutBytes = hookStdinMax
	// maxStderrBytes bounds captured stderr, which is only ever used for a
	// single diagnostic log line (see the "[hook ...]" print below) — a
	// small cap is intentional here.
	maxStderrBytes = 4096
	// hookStdinMax bounds the stdin payload sanity check. Payloads routinely
	// exceed 256 bytes (tool arguments, tool results, full user messages),
	// so the cap is generous; it only guards against pathological sizes —
	// the caller already holds the payload in memory and the hook timeout
	// bounds how long a script may consume it.
	hookStdinMax = 16 << 20 // 16 MiB
)

// boundedWriter is an io.Writer that retains only the first maxBytes
// written to it; everything beyond that is discarded. Write always
// reports success (n == len(p), err == nil) — exec.Cmd treats a copy
// error from its Stdout/Stderr writer as a fatal failure of the running
// command, so a bounded writer that returned an error on overflow would
// abort a hook mid-execution instead of just dropping its excess output.
// Capping here (during the copy from the child process) rather than after
// cmd.Run() returns is what keeps a noisy or malicious hook script from
// buffering unbounded output in memory for the full 15s hookTimeout.
type boundedWriter struct {
	buf      bytes.Buffer
	maxBytes int
}

func (b *boundedWriter) Write(p []byte) (int, error) {
	if remaining := b.maxBytes - b.buf.Len(); remaining > 0 {
		if len(p) < remaining {
			remaining = len(p)
		}
		b.buf.Write(p[:remaining])
	}
	return len(p), nil
}

func (b *boundedWriter) Bytes() []byte  { return b.buf.Bytes() }
func (b *boundedWriter) String() string { return b.buf.String() }

// ToolCallHookPayload is written to the script's stdin when an OnToolCall
// hook fires. Plugins can inspect tool name + raw arguments JSON.
type ToolCallHookPayload struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Timestamp string          `json:"timestamp"`
}

// resolveHookPath resolves a hook script's relative path inside the plugin's
// directory and rejects any path that escapes it. Returns the cleaned
// absolute path or an error.
func resolveHookPath(pluginDir, relPath string) (string, error) {
	if relPath == "" {
		return "", fmt.Errorf("empty hook path")
	}
	// Absolute paths are rejected outright: filepath.Join would silently
	// flatten them ("/etc/passwd" becomes pluginDir/etc/passwd), masking a
	// manifest that is not a relative plugin-relative path.
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("hook path %q is absolute; only plugin-relative paths are allowed", relPath)
	}
	abs := filepath.Clean(filepath.Join(pluginDir, relPath))
	rel, err := filepath.Rel(pluginDir, abs)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("hook path %q escapes plugin directory", relPath)
	}
	return abs, nil
}

// runHook executes a single hook script with the given stdin payload. It is
// a no-op for empty script paths. Errors are returned but never panic.
func runHook(ctx context.Context, pluginDir string, scriptPath string, stdin []byte) (string, error) {
	resolved, err := resolveHookPath(pluginDir, scriptPath)
	if err != nil {
		return "", err
	}

	execCtx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()

	// exec.CommandContext's first arg is the binary path. Passing it only
	// once gives argv = [resolved] ($0 = the script path) with no stray
	// positional arguments — passing it twice used to leak the path into
	// $1, which scripts reading positional args did not expect.
	cmd := exec.CommandContext(execCtx, resolved)
	setCmdSysProcAttr(cmd)
	cmd.Dir = pluginDir

	if len(stdin) > 0 {
		if len(stdin) > hookStdinMax {
			return "", fmt.Errorf("hook stdin payload too large (%d > %d)", len(stdin), hookStdinMax)
		}
		cmd.Stdin = bytes.NewReader(stdin)
	}

	stdout := &boundedWriter{maxBytes: maxStdoutBytes}
	stderr := &boundedWriter{maxBytes: maxStderrBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err = cmd.Run()

	// Capture and forward stderr (already capped to maxStderrBytes during
	// the copy above, not sliced after the fact).
	stderrStr := strings.TrimRight(stderr.String(), "\n")
	if stderrStr != "" {
		fmt.Fprintf(os.Stderr, "[hook %s:%s] %s\n", filepath.Base(pluginDir), filepath.Base(resolved), stderrStr)
	}

	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			return strings.TrimSpace(stdout.String()), fmt.Errorf("hook timed out after %v", hookTimeout)
		}
		return strings.TrimSpace(stdout.String()), fmt.Errorf("hook failed: %w", err)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// hookData copies the plugin entry to avoid retaining the manager's mutex
// across goroutine boundaries.
type hookData struct {
	pluginDir  string
	pluginName string
	scripts    []string
}

// snapshotHooks returns every plugin that declares scripts for the given
// hook type, sorted by plugin name (then per-script filename for
// tie-breaking). The sort is the contract for sequential hook pipelines:
// onMessageSend must run plugins in a deterministic order so
// the stdout->stdin chain produces the same transformation every time.
func (pm *PluginManager) snapshotHooks(t string) []hookData {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	var out []hookData
	for _, p := range pm.plugins {
		if !p.Enabled || p.Late == nil || p.Late.Hooks == nil {
			continue
		}
		var scripts []string
		switch t {
		case "tool-call":
			scripts = p.Late.Hooks.OnToolCall
		case "tool-result":
			scripts = p.Late.Hooks.OnToolResult
		case "session-start":
			scripts = p.Late.Hooks.OnSessionStart
		case "message-send":
			scripts = p.Late.Hooks.OnMessageSend
		default:
			return nil
		}
		if len(scripts) == 0 {
			continue
		}
		out = append(out, hookData{
			pluginDir:  p.Path,
			pluginName: p.Name,
			scripts:    append([]string(nil), scripts...),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].pluginName != out[j].pluginName {
			return out[i].pluginName < out[j].pluginName
		}
		// Tie-break on first script filename so two plugins whose names
		// match (e.g. "foo" and "foo") still order deterministically.
		if len(out[i].scripts) == 0 || len(out[j].scripts) == 0 {
			return len(out[i].scripts) < len(out[j].scripts)
		}
		return out[i].scripts[0] < out[j].scripts[0]
	})
	return out
}

// fanout fires all hooks across all plugins for the given event type in
// parallel. Each hook's stdout is logged; errors and stderr are forwarded
// but never abort the chain.
func (pm *PluginManager) fanout(ctx context.Context, eventType string, stdinFor func(pluginDir, script string, pluginName string) []byte) {
	hooks := pm.snapshotHooks(eventType)
	if len(hooks) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, h := range hooks {
		for _, script := range h.scripts {
			wg.Add(1)
			go func(h hookData, script string) {
				defer wg.Done()
				payload := []byte(nil)
				if stdinFor != nil {
					payload = stdinFor(h.pluginDir, script, h.pluginName)
				}
				out, err := runHook(ctx, h.pluginDir, script, payload)
				if err != nil {
					fmt.Fprintf(os.Stderr, "[%s/%s/%s] %v\n", h.pluginName, eventType, script, err)
				}
				if out != "" {
					fmt.Fprintf(os.Stderr, "[%s/%s/%s] %s\n", h.pluginName, eventType, script, out)
				}
			}(h, script)
		}
	}
	wg.Wait()
}

// BuildHookMiddlewares returns one common.ToolMiddleware per enabled plugin
// that declares OnToolCall hooks. Each middleware runs its plugin's scripts
// sequentially (so veto / argument mutation is deterministic across a
// plugin's own scripts) and then unconditionally calls next() so the rest
// of the chain runs normally — UNLESS a script returns the literal veto
// string "blocked", in which case the middleware aborts the chain with an
// error.
//
// hook contract (per script, in declaration order):
//   - empty / non-JSON stdout → pass-through (call unchanged, next() runs)
//   - JSON-valued stdout → REPLACES call.Function.Arguments (and next() runs)
//   - literal stdout "blocked" → call is vetoed, next() is SKIPPED, error
//     returned. The veto wins even if earlier scripts mutated arguments;
//     this is the recommended way to write "block dangerous commands" hooks.
func (pm *PluginManager) BuildHookMiddlewares() []common.ToolMiddleware {
	hooks := pm.snapshotHooks("tool-call")
	if len(hooks) == 0 {
		return nil
	}

	mws := make([]common.ToolMiddleware, 0, len(hooks))
	for _, h := range hooks {
		h := h // capture
		mw := func(next common.ToolRunner) common.ToolRunner {
			return func(ctx context.Context, call client.ToolCall) (string, error) {
				for _, script := range h.scripts {
					payload, _ := json.Marshal(ToolCallHookPayload{
						Tool:      call.Function.Name,
						Arguments: json.RawMessage(call.Function.Arguments),
						Timestamp: time.Now().UTC().Format(time.RFC3339),
					})
					out, err := runHook(ctx, h.pluginDir, script, payload)
					if err != nil {
						fmt.Fprintf(os.Stderr, "[%s/onToolCall/%s] %v\n", h.pluginName, script, err)
						continue
					}
					if out == "blocked" {
						return "", fmt.Errorf("tool call %q blocked by plugin %q", call.Function.Name, h.pluginName)
					}
					if out != "" && json.Valid([]byte(out)) {
						call.Function.Arguments = out
					}
				}
				return next(ctx, call)
			}
		}
		mws = append(mws, mw)
	}
	return mws
}

// CallOnSessionStartHooks fires OnSessionStart hooks for all enabled plugins
// in parallel. Each hook receives an empty JSON object on stdin (the
// documented contract); errors are logged but never fatal.
func (pm *PluginManager) CallOnSessionStartHooks() {
	pm.fanout(context.Background(), "session-start", func(pluginDir, script, pluginName string) []byte {
		return []byte("{}")
	})
}

// BuildToolResultMiddlewares returns post-execution ToolMiddlewares that
// fire onToolResult hooks after each tool completes successfully.
//
// Unlike BuildHookMiddlewares (which creates one middleware per plugin so
// each can independently veto/mutate the call pre-flight), this returns a
// single middleware that delegates to CallOnToolResultHooks — which already
// iterates every enabled plugin's scripts in deterministic order.
//
// If the inner runner returns an error (tool execution failed), the
// hooks are skipped and the original error passes through unchanged.
// A hook that returns "blocked" generates a veto error visible to the
// LLM caller as the tool error message.
func (pm *PluginManager) BuildToolResultMiddlewares() []common.ToolMiddleware {
	return []common.ToolMiddleware{
		func(next common.ToolRunner) common.ToolRunner {
			return func(ctx context.Context, call client.ToolCall) (string, error) {
				result, err := next(ctx, call)
				if err != nil {
					return result, err
				}
				mutated, hookErr := pm.CallOnToolResultHooks(ctx, call.Function.Name, []byte(result))
				if hookErr != nil {
					return "", hookErr
				}
				return string(mutated), nil
			}
		},
	}
}

// CallOnToolResultHooks fires OnToolResult hooks sequentially after each
// tool invocation completes. The payload is JSON of
// {"tool": name, "result": resultBytes} on each plugin's stdin. Per-script
// contract matches BuildHookMiddlewares so plugins can reason uniformly:
//   - empty stdout → pass-through (result unchanged)
//   - non-empty JSON stdout → REPLACE result bytes with the hook's stdout
//   - literal stdout "blocked" → drop the result; the hook returns an error
//     (so callers surface a "tool result was blocked by plugin X" message)
//   - errors logged + skipped, never abort the chain
//
// Ordering is deterministic: snapshotHooks() sorts by plugin name then
// first script, so the same plugin chain runs in the same order on every
// invocation. Returns the (possibly mutated) result bytes plus any veto
// error from a "blocked" return.
func (pm *PluginManager) CallOnToolResultHooks(ctx context.Context, tool string, result []byte) ([]byte, error) {
	hooks := pm.snapshotHooks("tool-result")
	if len(hooks) == 0 {
		return result, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for _, h := range hooks {
		for _, script := range h.scripts {
			// result is plain text, not JSON — marshal it as a string so the
			// payload is always valid JSON. json.RawMessage would embed the
			// bytes verbatim and fail marshal on non-JSON results, silently
			// leaving the hook with empty stdin.
			payload, err := json.Marshal(map[string]any{
				"tool":   tool,
				"result": string(result),
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "[%s/onToolResult/%s] marshal payload: %v\n", h.pluginName, script, err)
				continue
			}
			out, err := runHook(ctx, h.pluginDir, script, payload)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[%s/onToolResult/%s] %v\n", h.pluginName, script, err)
				continue
			}
			if out == "blocked" {
				return nil, fmt.Errorf("tool result %q blocked by plugin %q", tool, h.pluginName)
			}
			if out != "" && json.Valid([]byte(out)) {
				result = []byte(out)
			}
		}
	}
	return result, nil
}

// HookedMessage applies OnMessageSend hooks sequentially (after sort by
// plugin name) and returns the transformed message. By default each hook
// sees the output of the previous hook. If no hooks are registered, the
// input is returned unchanged. The supplied context is forwarded to
// each hook so the TUI can cancel a misbehaving plugin.
func (pm *PluginManager) HookedMessage(ctx context.Context, text string) string {
	hooks := pm.snapshotHooks("message-send")
	if len(hooks) == 0 || text == "" {
		return text
	}
	if ctx == nil {
		ctx = context.Background()
	}
	current := text
	for _, h := range hooks {
		for _, script := range h.scripts {
			out, err := runHook(ctx, h.pluginDir, script, []byte(current))
			if err != nil {
				fmt.Fprintf(os.Stderr, "[%s/onMessageSend/%s] %v\n", h.pluginName, script, err)
				continue
			}
			if out != "" {
				// Deliberately not logged: every successful onMessageSend
				// transform would otherwise print a line to the terminal
				// for every message the user sends, leaking into the TUI's
				// stderr. Re-add this once there's a proper logger that
				// isn't just os.Stderr.
				current = out
			}
		}
	}
	return current
}
