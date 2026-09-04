package plugin

import (
	"context"
	"late/internal/common"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestHandleCommand_DispatchesHandler verifies that when a plugin declares
// a command with a Handler script, HandleCommand runs the script with the
// args JSON-encoded on stdin and returns the trimmed stdout as output.
func TestHandleCommand_DispatchesHandler(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	dir := t.TempDir()
	mf := &LateManifest{
		Commands: LateCommands{
			{Name: "/weather", Handler: "scripts/weather.sh"},
		},
	}
	p := writeTestPlugin(t, dir, "weather-plugin", mf)
	p.Path = filepath.Join(dir, "weather-plugin")
	writeExecutableShell(t,
		filepath.Join(p.Path, "scripts/weather.sh"),
		`echo "forecast: $(cat)"`)
	pm.Add(p)

	out, handled, err := pm.HandleCommand(context.Background(), "/weather", []string{"sf"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true for plugin with a Handler script")
	}
	if !strings.Contains(out, "sf") {
		t.Fatalf("expected output to contain the arg, got %q", out)
	}
	if !strings.Contains(out, "forecast") {
		t.Fatalf("expected output to contain the script's echo, got %q", out)
	}
}

// TestHandleCommand_NoPluginReturnsUnhandled asserts the fall-through
// behavior when no enabled plugin declares the requested command.
func TestHandleCommand_NoPluginReturnsUnhandled(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	out, handled, err := pm.HandleCommand(context.Background(), "/nothing", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled {
		t.Fatal("expected handled=false when no plugin declares /nothing")
	}
	if out != "" {
		t.Fatalf("expected empty output, got %q", out)
	}
}

// TestHandleCommand_NoHandlerFallsBack covers the legacy string-form case
// where a plugin declares a command name via the bare string (no Handler
// script) — the contract is that HandleCommand returns handled=false so
// the TUI submits the input as a plain user prompt.
func TestHandleCommand_NoHandlerFallsBack(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	dir := t.TempDir()
	mf := &LateManifest{
		Commands: LateCommands{{Name: "/legacy"}},
	}
	p := writeTestPlugin(t, dir, "legacy-plugin", mf)
	p.Path = filepath.Join(dir, "legacy-plugin")
	pm.Add(p)

	_, handled, err := pm.HandleCommand(context.Background(), "/legacy", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled {
		t.Fatal("expected handled=false when command has no Handler script (legacy dispatch)")
	}
}

// TestHandleCommand_DuplicateLogsWarning exercises the resolution rule:
// when two enabled plugins declare the same command name with a Handler,
// the first one in sorted-by-name order wins, and the duplicate is logged
// to stderr. We do not assert the log content (test runner swallows
// stderr) — only that the call still succeeds.
func TestHandleCommand_DuplicateLogsWarning(t *testing.T) {
	pm := NewPluginManager(t.TempDir())

	// Alpha (wins because "alpha" < "beta").
	dirA := t.TempDir()
	mfA := &LateManifest{
		Commands: LateCommands{{Name: "/shared", Handler: "scripts/a.sh"}},
	}
	pA := writeTestPlugin(t, dirA, "alpha", mfA)
	pA.Path = filepath.Join(dirA, "alpha")
	writeExecutableShell(t, filepath.Join(pA.Path, "scripts/a.sh"), `echo alpha-result`)
	pm.Add(pA)

	// Beta (shadowed).
	dirB := t.TempDir()
	mfB := &LateManifest{
		Commands: LateCommands{{Name: "/shared", Handler: "scripts/b.sh"}},
	}
	pB := writeTestPlugin(t, dirB, "beta", mfB)
	pB.Path = filepath.Join(dirB, "beta")
	writeExecutableShell(t, filepath.Join(pB.Path, "scripts/b.sh"), `echo beta-result`)
	pm.Add(pB)

	out, handled, err := pm.HandleCommand(context.Background(), "/shared", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true from the winning plugin")
	}
	if !strings.Contains(out, "alpha-result") {
		t.Fatalf("expected alpha's output (alpha is alphabetically first), got %q", out)
	}
}

// TestGetInlineTools_AggregatesAcrossPlugins ensures that tools declared
// independently by two plugins both appear in the result set, with names
// namespaced to "<plugin>__<tool>" (sanitized — no ':') so identical
// bare names do not collide and OpenAI-compatible endpoints accept them.
func TestGetInlineTools_AggregatesAcrossPlugins(t *testing.T) {
	pm := NewPluginManager(t.TempDir())

	dirA := t.TempDir()
	mfA := &LateManifest{
		Tools: []LateToolManifest{
			{Name: "summarize", Description: "A's summarize",
				Script: "scripts/a.sh", Parameters: jsonRaw(`{}`)},
		},
	}
	pA := writeTestPlugin(t, dirA, "alpha", mfA)
	pA.Path = filepath.Join(dirA, "alpha")
	writeExecutableShell(t, filepath.Join(pA.Path, "scripts/a.sh"), `echo a`)
	pm.Add(pA)

	dirB := t.TempDir()
	mfB := &LateManifest{
		Tools: []LateToolManifest{
			{Name: "summarize", Description: "B's summarize",
				Script: "scripts/b.sh", Parameters: jsonRaw(`{}`)},
		},
	}
	pB := writeTestPlugin(t, dirB, "beta", mfB)
	pB.Path = filepath.Join(dirB, "beta")
	writeExecutableShell(t, filepath.Join(pB.Path, "scripts/b.sh"), `echo b`)
	pm.Add(pB)

	tools := pm.GetInlineTools(nil)
	if len(tools) != 2 {
		t.Fatalf("expected 2 namespaced tools, got %d", len(tools))
	}
	seen := map[string]bool{}
	for _, t1 := range tools {
		seen[t1.Name] = true
		if strings.Contains(t1.Name, ":") {
			t.Fatalf("tool name %q contains ':', which OpenAI-compatible endpoints reject", t1.Name)
		}
	}
	if !seen["alpha__summarize"] || !seen["beta__summarize"] {
		t.Fatalf("expected alpha__summarize and beta__summarize, got %v", seen)
	}
}

// TestGetInlineTools_SanitizesAndDeduplicates verifies the endpoint-safety
// contract for inline tool names: invalid characters are replaced, names
// are capped at common.MaxToolNameLen, and distinct plugin:tool combos
// that sanitize to the same name get deterministic hash suffixes.
func TestGetInlineTools_SanitizesAndDeduplicates(t *testing.T) {
	pm := NewPluginManager(t.TempDir())

	// "a-b" tool "c" and "a" tool "b-c" both sanitize to "a-b__c"
	// without collision handling.
	writeToolPlugin := func(dir, name, toolName string) {
		mf := &LateManifest{
			Tools: []LateToolManifest{
				{Name: toolName, Description: "t",
					Script: "scripts/t.sh", Parameters: jsonRaw(`{}`)},
			},
		}
		p := writeTestPlugin(t, dir, name, mf)
		p.Path = filepath.Join(dir, name)
		writeExecutableShell(t, filepath.Join(p.Path, "scripts/t.sh"), `echo t`)
		pm.Add(p)
	}
	writeToolPlugin(t.TempDir(), "a-b", "c")
	writeToolPlugin(t.TempDir(), "a", "b-c")

	tools := pm.GetInlineTools(nil)
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Name] = true
		if len(tl.Name) > common.MaxToolNameLen {
			t.Fatalf("tool name %q exceeds %d chars", tl.Name, common.MaxToolNameLen)
		}
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 unique tool names after dedup, got %v", names)
	}
	// The first occurrence keeps the plain name.
	if !names["a-b__c"] {
		t.Fatalf("expected first occurrence to keep name a-b__c, got %v", names)
	}
}

// TestGetInlineTools_DedupesAgainstUsedNames is a regression test for the
// cross-source tool-name-collision bug: GetInlineTools used to dedupe only
// against other inline tools (used=nil), so an inline tool could silently
// overwrite an MCP-backed tool registered under the same namespaced name
// (e.g. inline plugin "github" tool "create_issue" vs. an MCP server
// literally named "github" exposing "create_issue" — both sanitize to
// "github__create_issue"). Passing a pre-seeded `used` map (as
// cmd/late/main.go now does, seeded from already-registered MCP tool
// names) must make the inline tool pick a different, deduped name instead.
func TestGetInlineTools_DedupesAgainstUsedNames(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	mf := &LateManifest{
		Tools: []LateToolManifest{
			{Name: "create_issue", Description: "t",
				Script: "scripts/t.sh", Parameters: jsonRaw(`{}`)},
		},
	}
	dir := t.TempDir()
	p := writeTestPlugin(t, dir, "github", mf)
	p.Path = filepath.Join(dir, "github")
	writeExecutableShell(t, filepath.Join(p.Path, "scripts/t.sh"), `echo t`)
	pm.Add(p)

	// Simulate an MCP server ("github") having already claimed this name.
	used := map[string]bool{"github__create_issue": true}

	tools := pm.GetInlineTools(used)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name == "github__create_issue" {
		t.Fatalf("inline tool collided with the pre-seeded MCP tool name %q instead of deduping", tools[0].Name)
	}
	if !used[tools[0].Name] {
		t.Fatalf("expected the assigned name %q to be recorded into used", tools[0].Name)
	}
}

// TestGetInlineTools_ScopedPluginNamesSanitized ensures npm-scoped plugin
// names (@scope/name) produce endpoint-safe tool names.
func TestGetInlineTools_ScopedPluginNamesSanitized(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	mf := &LateManifest{
		Tools: []LateToolManifest{
			{Name: "summarize", Description: "t",
				Script: "scripts/t.sh", Parameters: jsonRaw(`{}`)},
		},
	}
	p := writeTestPlugin(t, t.TempDir(), "@late/cool", mf)
	writeExecutableShell(t, filepath.Join(p.Path, "scripts/t.sh"), `echo t`)
	pm.Add(p)

	tools := pm.GetInlineTools(nil)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name != "_late_cool__summarize" {
		t.Fatalf("unexpected sanitized name %q, want %q", tools[0].Name, "_late_cool__summarize")
	}
}

// TestHandleCommand_ConcurrentWithWriters guards against the nested-RLock
// deadlock: HandleCommand used to call All() (which takes the same RLock)
// while already holding RLock. A writer queued between the two read locks
// wedges the second RLock forever (Go RWMutex blocks new readers while a
// writer waits), stalling every lock user.
//
// The bad interleaving lands in a nanosecond window and can't be forced
// without a seam inside HandleCommand, so this is a bounded stress test:
// handlers and writers race under a watchdog. On the fixed code (single
// RLock, allLocked() for the sorted copy) no interleaving can deadlock;
// a regression stalls the watchdog and fails the test instead of hanging
// the suite.
func TestHandleCommand_ConcurrentWithWriters(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	dir := t.TempDir()
	mf := &LateManifest{
		Commands: LateCommands{
			{Name: "/weather", Handler: "scripts/weather.sh"},
		},
	}
	p := writeTestPlugin(t, dir, "weather-plugin", mf)
	p.Path = filepath.Join(dir, "weather-plugin")
	writeExecutableShell(t, filepath.Join(p.Path, "scripts/weather.sh"), `cat >/dev/null; echo ok`)
	pm.Add(p)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 300; i++ {
			if _, handled, err := pm.HandleCommand(context.Background(), "/weather", []string{"x"}); err != nil {
				t.Errorf("HandleCommand: %v", err)
				return
			} else if !handled {
				t.Error("expected handled=true")
				return
			}
		}
	}()
	go func() {
		// Writer traffic: continuously queue Lock() while the handler
		// goroutine holds its read lock.
		for i := 0; i < 5000; i++ {
			pm.Add(&InstalledPlugin{Name: "temp", Path: dir, Late: &LateManifest{}})
			pm.Remove("temp")
		}
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("HandleCommand deadlocked against queued writers (nested RLock?)")
	}
}
