package plugin

import (
	"context"
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
// namespaced to "<plugin>:<tool>" so identical bare names do not collide.
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

	tools := pm.GetInlineTools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 namespaced tools, got %d", len(tools))
	}
	seen := map[string]bool{}
	for _, t1 := range tools {
		seen[t1.Name] = true
		if !strings.Contains(t1.Name, ":") {
			t.Fatalf("expected namespaced tool name, got %q", t1.Name)
		}
	}
	if !seen["alpha:summarize"] || !seen["beta:summarize"] {
		t.Fatalf("expected alpha:summarize and beta:summarize, got %v", seen)
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
