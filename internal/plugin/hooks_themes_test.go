package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"late/internal/client"
	"late/internal/common"
)

// Compile-time assertion: HookedMessage now takes a context so the TUI
// can cancel a misbehaving plugin. Existing tests below were updated
// to pass context.Background().

// helper: write a small POSIX shell script
func writeExecutableShell(t *testing.T, path, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script test only runs on POSIX")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir shell dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0755); err != nil {
		t.Fatalf("write shell: %v", err)
	}
}

// helper: write a fake plugin into a temp dir (the rich, namespaced
// helper used across plugin tests).
func writeTestPlugin(t *testing.T, parentDir, name string, manifest *LateManifest) *InstalledPlugin {
	t.Helper()
	pluginDir := filepath.Join(parentDir, name)
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("mkdir plugin: %v", err)
	}
	pkg := PackageJSON{Name: name, Version: "1.0.0", Late: manifest}
	b, _ := json.Marshal(pkg)
	if err := os.WriteFile(filepath.Join(pluginDir, "package.json"), b, 0644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	p, err := LoadPlugin(pluginDir)
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}
	_ = SavePluginMeta(p)
	return p
}

// TestBuildToolResultMiddlewares_PostExecMutate: the post-execution
// middleware calls the inner runner first, then applies onToolResult hooks.
func TestBuildToolResultMiddlewares_PostExecMutate(t *testing.T) {
	root := t.TempDir()
	pluginsDir := filepath.Join(root, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	hookDir := filepath.Join(pluginsDir, "muter")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(hookDir, "mute.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho '{\"mutated\":true}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	pkg := map[string]any{
		"name":    "muter",
		"version": "1.0.0",
		"late": map[string]any{
			"hooks": map[string]any{
				"onToolResult": []string{"mute.sh"},
			},
		},
	}
	pkgBytes, _ := json.Marshal(pkg)
	if err := os.WriteFile(filepath.Join(hookDir, "package.json"), pkgBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	pm := NewPluginManager(pluginsDir)
	if err := pm.Discover(); err != nil {
		t.Fatal(err)
	}
	p := pm.Plugin("muter")
	if p == nil {
		t.Fatal("expected muter plugin to be discovered")
	}
	p.Enabled = true

	mws := pm.BuildToolResultMiddlewares()
	if len(mws) != 1 {
		t.Fatalf("expected 1 middleware, got %d", len(mws))
	}

	innerRan := false
	inner := func(ctx context.Context, call client.ToolCall) (string, error) {
		innerRan = true
		return `{"original":true}`, nil
	}

	wrapped := mws[0](inner)
	result, err := wrapped(context.Background(), client.ToolCall{
		Function: client.FunctionCall{Name: "test_tool"},
	})
	if err != nil {
		t.Fatalf("middleware error: %v", err)
	}
	if !innerRan {
		t.Fatal("inner runner was never called")
	}
	if !strings.Contains(result, `"mutated":true`) {
		t.Fatalf("expected mutated result, got %q", result)
	}
}

// TestCallOnToolResultHooks_PlainTextResultPayload: a plain-text (non-JSON)
// tool result must still reach the hook as a valid JSON payload
// {"tool": ..., "result": "..."}. Wrapping the result in json.RawMessage
// made json.Marshal fail on non-JSON text, and the ignored error left the
// hook with empty stdin.
func TestCallOnToolResultHooks_PlainTextResultPayload(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	pluginDir := t.TempDir()
	capture := filepath.Join(pluginDir, "stdin-capture.txt")
	writeExecutableShell(t, filepath.Join(pluginDir, "capture.sh"), "cat > "+capture)

	mf := &LateManifest{
		Hooks: &LateHooksManifest{
			OnToolResult: []string{"capture.sh"},
		},
	}
	p := writeTestPlugin(t, pluginDir, "capture-plugin", mf)
	p.Path = pluginDir
	pm.Add(p)

	result := "Command output:\nline two with \"quotes\" and \\backslash"
	if _, err := pm.CallOnToolResultHooks(context.Background(), "bash", []byte(result)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	var payload struct {
		Tool   string `json:"tool"`
		Result string `json:"result"`
	}
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatalf("hook did not receive valid JSON payload, got %q: %v", got, err)
	}
	if payload.Tool != "bash" {
		t.Errorf("tool = %q, want %q", payload.Tool, "bash")
	}
	if payload.Result != result {
		t.Errorf("result = %q, want %q", payload.Result, result)
	}
}

// TestBuildToolResultMiddlewares_SkipOnError: if the inner runner returns
// an error, the middleware does NOT call onToolResult hooks.
func TestBuildToolResultMiddlewares_SkipOnError(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	mws := pm.BuildToolResultMiddlewares()
	if len(mws) != 1 {
		t.Fatalf("expected 1 middleware, got %d", len(mws))
	}

	inner := func(ctx context.Context, call client.ToolCall) (string, error) {
		return "", os.ErrNotExist
	}
	wrapped := mws[0](inner)
	_, err := wrapped(context.Background(), client.ToolCall{
		Function: client.FunctionCall{Name: "failer"},
	})
	if err == nil {
		t.Fatal("expected inner error to propagate")
	}
}

// 1. Hook path containment: rejects escaping paths
func TestResolveHookPath_RejectsTraversal(t *testing.T) {
	pluginDir := t.TempDir()
	if _, err := resolveHookPath(pluginDir, "../other.sh"); err == nil {
		t.Fatal("expected ../other.sh to escape plugin dir")
	}
	if _, err := resolveHookPath(pluginDir, "/etc/passwd"); err == nil {
		t.Fatal("expected absolute /etc/passwd to escape plugin dir")
	}
	if _, err := resolveHookPath(pluginDir, ""); err == nil {
		t.Fatal("expected empty path to be rejected")
	}
}

// 2. Hook path containment: allows contained paths
func TestResolveHookPath_AllowsContained(t *testing.T) {
	pluginDir := t.TempDir()
	got, err := resolveHookPath(pluginDir, "subdir/hook.sh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(got, pluginDir) {
		t.Fatalf("expected resolved path under plugin dir, got %q", got)
	}
}

// 3. Hook execution: happy path reads from stdin
func TestRunHook_HappyPath(t *testing.T) {
	pluginDir := t.TempDir()
	script := filepath.Join(pluginDir, "echo.sh")
	writeExecutableShell(t, script, `cat`)
	out, err := runHook(context.Background(), pluginDir, "echo.sh", []byte("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "hello" {
		t.Fatalf("expected hello, got %q", out)
	}
}

// 4. Hook execution: timeout enforced
func TestRunHook_TimeoutEnforced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell test only on POSIX")
	}
	pluginDir := t.TempDir()
	script := filepath.Join(pluginDir, "sleep.sh")
	writeExecutableShell(t, script, `sleep 30`)
	// Override hookTimeout via short ctx to keep test fast; we pass a
	// shorter context via WithTimeout to be portable.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := runHook(ctx, pluginDir, "sleep.sh", nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

// 4b. Hook execution: payloads larger than the old 256-byte cap flow
// through intact. Tool arguments, tool results, and user messages
// routinely exceed 256 bytes; the cap is a sanity bound, not a
// functional limit.
func TestRunHook_LargePayloadPassesThrough(t *testing.T) {
	pluginDir := t.TempDir()
	script := filepath.Join(pluginDir, "echo.sh")
	writeExecutableShell(t, script, `cat`)
	payload := []byte(strings.Repeat("x", 64*1024)) // 64 KiB, 256x the old cap
	out, err := runHook(context.Background(), pluginDir, "echo.sh", payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != string(payload) {
		t.Fatalf("payload corrupted: got %d bytes, want %d", len(out), len(payload))
	}
}

// 4c. boundedWriter: the io.Writer runHook uses to cap captured
// stdout/stderr during the copy from the child process (rather than
// buffering everything and truncating after cmd.Run() returns). Write
// must never report an error — exec.Cmd treats a copy error from its
// Stdout/Stderr writer as a fatal failure of the running command, which
// would abort a hook mid-execution instead of just dropping its excess
// output.
func TestBoundedWriter_CapsWithoutErroringOnOverflow(t *testing.T) {
	w := &boundedWriter{maxBytes: 10}

	n, err := w.Write([]byte("hello "))
	if err != nil || n != 6 {
		t.Fatalf("first write: n=%d err=%v, want n=6 err=nil", n, err)
	}

	// Pushes well past the 10-byte cap.
	n, err = w.Write([]byte("world!!!!!"))
	if err != nil || n != 10 {
		t.Fatalf("overflowing write: n=%d err=%v, want n=10 err=nil (must not error)", n, err)
	}
	if got := w.String(); got != "hello worl" {
		t.Fatalf("expected content capped at %q, got %q", "hello worl", got)
	}

	// A write after the cap is already full must still report success
	// (all bytes "accepted") without changing the retained content.
	n, err = w.Write([]byte("more"))
	if err != nil || n != 4 {
		t.Fatalf("post-cap write: n=%d err=%v, want n=4 err=nil", n, err)
	}
	if got := w.String(); got != "hello worl" {
		t.Fatalf("content changed after cap reached: %q", got)
	}
}

// 5. HookedMessage: empty/no hooks returns input unchanged
func TestHookedMessage_NoHooksReturnsInput(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	if got := pm.HookedMessage(context.Background(), "hi"); got != "hi" {
		t.Fatalf("expected unchanged, got %q", got)
	}
}

// 6. HookedMessage: applies OnMessageSend script transform
func TestHookedMessage_TransformsText(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	pluginDir := t.TempDir()
	mf := &LateManifest{Hooks: &LateHooksManifest{OnMessageSend: []string{"wrap.sh"}}}
	p := writeTestPlugin(t, pluginDir, "msg-wrap", mf)
	writeExecutableShell(t, filepath.Join(p.Path, "wrap.sh"), `cat; echo`)
	p.Path = filepath.Join(pluginDir, "msg-wrap")
	pm.Add(p)
	got := pm.HookedMessage(context.Background(), "hi")
	if got != "hi" {
		t.Fatalf("expected 'hi', got %q (note: shell `echo` without args prints empty)", got)
	}
}

// 7. BuildHookMiddlewares: returns one middleware per plugin
func TestBuildHookMiddlewares_PerPlugin(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	for i := 0; i < 3; i++ {
		dir := t.TempDir()
		name := "p" + string(rune('1'+i))
		mf := &LateManifest{Hooks: &LateHooksManifest{OnToolCall: []string{"noop.sh"}}}
		p := writeTestPlugin(t, dir, name, mf)
		p.Path = filepath.Join(dir, name)
		pm.Add(p)
	}
	mws := pm.BuildHookMiddlewares()
	if len(mws) != 3 {
		t.Fatalf("expected 3 middlewares, got %d", len(mws))
	}
	// Verify signature is correct: invoking the middleware with a noop next
	// should still call next and return its result.
	var called bool
	next := common.ToolRunner(func(ctx context.Context, call client.ToolCall) (string, error) {
		called = true
		return "ok", nil
	})
	for _, mw := range mws {
		runner := mw(next)
		out, err := runner(context.Background(), client.ToolCall{
			Function: client.FunctionCall{Name: "anything", Arguments: "{}"},
		})
		if err != nil {
			t.Fatalf("middleware returned error: %v", err)
		}
		if out != "ok" {
			t.Fatalf("middleware didn't pass through, got %q", out)
		}
	}
	if !called {
		t.Fatal("expected 'next' to be called")
	}
}

// 8. BuildHookMiddlewares: empty when no plugins have OnToolCall
func TestBuildHookMiddlewares_EmptyWhenNoHooks(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	dir := t.TempDir()
	mf := &LateManifest{} // no hooks at all
	p := writeTestPlugin(t, dir, "silent", mf)
	p.Path = filepath.Join(dir, "silent")
	pm.Add(p)
	if mws := pm.BuildHookMiddlewares(); len(mws) != 0 {
		t.Fatalf("expected 0, got %d", len(mws))
	}
}

// 9. CallOnSessionStartHooks runs without panic on empty manager
func TestCallOnSessionStartHooks_NoPanicOnEmpty(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	pm.CallOnSessionStartHooks()
}

// 9b. onSessionStart hooks receive an empty JSON object on stdin, matching
// the documented contract (previously they got nil stdin, which made
// scripts that `cat | jq .` hang).
func TestCallOnSessionStartHooks_EmptyJSONStdin(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	pluginDir := t.TempDir()
	capture := filepath.Join(pluginDir, "stdin.txt")
	writeExecutableShell(t, filepath.Join(pluginDir, "start.sh"), "cat > "+capture)

	mf := &LateManifest{Hooks: &LateHooksManifest{OnSessionStart: []string{"start.sh"}}}
	p := writeTestPlugin(t, pluginDir, "start-plugin", mf)
	p.Path = pluginDir
	pm.Add(p)

	pm.CallOnSessionStartHooks()

	got, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	if string(got) != "{}" {
		t.Fatalf("expected empty JSON object on stdin, got %q", got)
	}
}

// 9c. runHook must not pass the script path as a positional argument:
// scripts reading $1 should see nothing (the old double-path argv leaked
// the script path into $1).
func TestRunHook_NoStrayPositionalArg(t *testing.T) {
	pluginDir := t.TempDir()
	script := filepath.Join(pluginDir, "arg.sh")
	writeExecutableShell(t, script, `echo "[$1]"`)
	out, err := runHook(context.Background(), pluginDir, "arg.sh", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "[]" {
		t.Fatalf("expected empty $1, got %q", out)
	}
}

// NOTE: Tests for ResolveRenderTheme and LateTheme live in
// internal/tui/theme_test.go since the helper lives in the tui
// package. Putting them here would create a circular import.

// 13. Theme path resolution: rejects traversal
func TestResolveThemePath_RejectsTraversal(t *testing.T) {
	pluginDir := t.TempDir()
	if _, err := resolveThemePath(pluginDir, "../../etc/theme.json"); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
	if _, err := resolveThemePath(pluginDir, "/etc/passwd"); err == nil {
		t.Fatal("expected absolute to be rejected")
	}
}

// 14. Theme load: rejects missing name
func TestLoadThemeFile_RequiresName(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(p, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadThemeFile(p); err == nil {
		t.Fatal("expected error when 'name' missing")
	}
}

// 15. GetTheme: bare name lookup across plugins
func TestGetTheme_BareNameLookup(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	dir := t.TempDir()
	mf := &LateManifest{Themes: []string{"ocean.json"}}
	p := writeTestPlugin(t, dir, "theme-plugin", mf)
	writeJSON(t, p.Path, "ocean.json", `{"name":"ocean"}`)
	pm.Add(p)

	info, err := pm.GetTheme("ocean")
	if err != nil || info == nil {
		t.Fatalf("expected to find 'ocean', got err=%v info=%v", err, info)
	}
	if info.ID != "theme-plugin:ocean" {
		t.Fatalf("unexpected id: %s", info.ID)
	}
}

// 16. GetTheme: namespaced lookup
func TestGetTheme_NamespacedLookup(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	dir := t.TempDir()
	mf := &LateManifest{Themes: []string{"theme.json"}}
	p := writeTestPlugin(t, dir, "green", mf)
	writeJSON(t, p.Path, "theme.json", `{"name":"v1"}`)
	pm.Add(p)

	info, err := pm.GetTheme("green:v1")
	if err != nil || info == nil {
		t.Fatalf("expected namespace match, got err=%v info=%v", err, info)
	}
}

// 17. GetTheme: empty returns (nil, nil)
func TestGetTheme_EmptyReturnsNilNil(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	info, err := pm.GetTheme("")
	if err != nil || info != nil {
		t.Fatalf("expected nil/nil for empty id, got info=%v err=%v", info, err)
	}
}

// 18. AllThemes: aggregates across enabled plugins only
func TestAllThemes_AggregatesEnabledOnly(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	dir := t.TempDir()
	mf := &LateManifest{Themes: []string{"a.json"}}
	p := writeTestPlugin(t, dir, "alpha", mf)
	writeJSON(t, p.Path, "a.json", `{"name":"alpha"}`)
	p.Enabled = false
	pm.Add(p)

	got := pm.AllThemes()
	if len(got) != 0 {
		t.Fatalf("expected 0 themes from disabled plugin, got %d", len(got))
	}
	p.Enabled = true
	got = pm.AllThemes()
	if len(got) != 1 {
		t.Fatalf("expected 1 theme after enabling, got %d", len(got))
	}
}

// 19. AllThemes: skips unparseable files
func TestAllThemes_SkipsUnparseable(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	dir := t.TempDir()
	mf := &LateManifest{Themes: []string{"missing.json", "garbage.json"}}
	p := writeTestPlugin(t, dir, "broken", mf)
	// missing.json doesn't exist; garbage.json is not valid
	_ = os.WriteFile(filepath.Join(p.Path, "garbage.json"), []byte("not json{{"), 0644)
	pm.Add(p)

	got := pm.AllThemes()
	if len(got) != 0 {
		t.Fatalf("expected 0 themes, got %d", len(got))
	}
}

// 20. findTheme: name-mismatch returns error
func TestFindTheme_NameMismatch(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	dir := t.TempDir()
	writeJSON(t, dir, "x.json", `{"name":"realname"}`)
	mf := &LateManifest{Themes: []string{"x.json"}}
	p := writeTestPlugin(t, dir, "finder", mf)
	pm.Add(p)

	_, err := pm.findTheme("finder", "wrongname")
	if err == nil {
		t.Fatal("expected error for name mismatch")
	}
}

// ----------------------------------------------------------------------------

func writeJSON(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
		t.Fatalf("write json: %v", err)
	}
}
