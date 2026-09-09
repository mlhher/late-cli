package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"late/internal/client"
	"late/internal/session"
)

// TestPluginInlineTool_RequiresConfirmation guards the documented contract
// that plugin inline tools (arbitrary scripts) go through the normal user
// confirmation flow. plugin-example.md: "user confirmation still prompts
// the user"; plugin-sdk.md: "plugin tools respect user confirmation".
func TestPluginInlineTool_RequiresConfirmation(t *testing.T) {
	tool := pluginInlineTool{name: "example:lookup"}
	if !tool.RequiresConfirmation(nil) {
		t.Error("plugin inline tool must require confirmation before running its script")
	}
}

// TestToolEnabled_LegacyColonKeyFallback guards migration compatibility: a
// config written before tool names were namespaced as "server__tool" may
// still disable a tool by its old "server:tool" key. toolEnabled must
// reconstruct and check that legacy form before falling back further to
// the bare tool name.
func TestToolEnabled_LegacyColonKeyFallback(t *testing.T) {
	enabledTools := map[string]bool{"myserver:mytool": false}

	if toolEnabled(enabledTools, "myserver__mytool") {
		t.Error("expected the namespaced name to resolve via the legacy colon key and report disabled")
	}

	// A namespaced-key entry still takes priority over the legacy form.
	enabledTools["other__tool"] = true
	enabledTools["other:tool"] = false
	if !toolEnabled(enabledTools, "other__tool") {
		t.Error("expected the exact namespaced key to win over the legacy colon key")
	}

	// Unrelated tools still default to enabled.
	if !toolEnabled(enabledTools, "unrelated__tool") {
		t.Error("expected an unconfigured tool to default to enabled")
	}
}

// TestToolEnabled_BareNameFallback guards compatibility with configs
// written before tool names were namespaced at all (e.g. "list_files":
// false), which must still disable a namespaced MCP tool name via
// common.BareToolName's fallback. A namespaced-key entry still takes
// priority over the bare-name form.
func TestToolEnabled_BareNameFallback(t *testing.T) {
	if toolEnabled(map[string]bool{"list_files": false}, "graph-rag__list_files") {
		t.Fatal("legacy bare-name setting did not disable a namespaced MCP tool")
	}
	enabledTools := map[string]bool{"list_files": false, "graph-rag__list_files": true}
	if !toolEnabled(enabledTools, "graph-rag__list_files") {
		t.Fatal("namespaced setting did not override the bare-name setting")
	}
}

// writeTestSession creates a flat session in the injected sessions directory:
// <dir>/<id>.json (history) and <dir>/<id>.meta.json. It returns both paths.
func writeTestSession(t *testing.T, sessionsDir, id string) (metaPath, historyPath string) {
	t.Helper()

	historyPath = filepath.Join(sessionsDir, id+".json")
	if err := session.SaveHistory(historyPath, []client.ChatMessage{
		{Role: "user", Content: client.TextContent("hello")},
	}); err != nil {
		t.Fatalf("SaveHistory(%s): %v", id, err)
	}

	if err := session.SaveSessionMeta(session.SessionMeta{
		ID:           id,
		Title:        "Test session " + id,
		CreatedAt:    time.Now(),
		LastUpdated:  time.Now(),
		HistoryPath:  historyPath,
		MessageCount: 1,
	}); err != nil {
		t.Fatalf("SaveSessionMeta(%s): %v", id, err)
	}

	return filepath.Join(sessionsDir, id+".meta.json"), historyPath
}

// injectSessionDir points session.SessionDir at a temp dir for the test's duration.
func injectSessionDir(t *testing.T) string {
	t.Helper()

	tmp := t.TempDir()
	oldDir := session.SessionDir
	session.SessionDir = func() (string, error) { return tmp, nil }
	t.Cleanup(func() { session.SessionDir = oldDir })

	return tmp
}

func assertFileGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be removed, but it still exists", path)
	}
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to still exist: %v", path, err)
	}
}

func TestHandleSessionDelete_RemovesSubagentFolder(t *testing.T) {
	tmp := injectSessionDir(t)

	metaA, historyA := writeTestSession(t, tmp, "session-20250101-123456")
	// Session A also has the hierarchical subagent history folder.
	folderA := filepath.Join(tmp, "session-20250101-123456")
	subagentsDir := filepath.Join(folderA, "subagents")
	if err := os.MkdirAll(subagentsDir, 0700); err != nil {
		t.Fatalf("creating subagent dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subagentsDir, "researcher-subagent-0.json"), []byte("[]"), 0600); err != nil {
		t.Fatalf("writing subagent history: %v", err)
	}

	metaB, historyB := writeTestSession(t, tmp, "session-20250102-999999")

	handleSessionDelete("session-20250101-123456")

	// Session A: meta, history, and the entire subagent folder are all gone.
	assertFileGone(t, metaA)
	assertFileGone(t, historyA)
	assertFileGone(t, folderA)

	// Session B is untouched.
	assertFileExists(t, metaB)
	assertFileExists(t, historyB)
}

func TestHandleSessionDelete_LegacyFlatSession(t *testing.T) {
	tmp := injectSessionDir(t)

	metaC, historyC := writeTestSession(t, tmp, "session-20250103-000000")

	handleSessionDelete("session-20250103-000000")

	assertFileGone(t, metaC)
	assertFileGone(t, historyC)
}

func TestDeriveEffectiveSessionID(t *testing.T) {
	tests := []struct {
		name        string
		historyPath string
		want        string
	}{
		{
			name:        "plain session history file",
			historyPath: "session-20260815-123456.json",
			want:        "session-20260815-123456",
		},
		{
			name:        "full path uses base name",
			historyPath: "/tmp/sessions/session-abc.json",
			want:        "session-abc",
		},
		{
			name:        "parent directory reference",
			historyPath: "..",
			want:        "",
		},
		{
			name:        "full path ending in parent directory",
			historyPath: "/some/dir/..",
			want:        "",
		},
		{
			name:        "current directory reference",
			historyPath: ".",
			want:        "",
		},
		{
			name:        "json suffix only",
			historyPath: ".json",
			want:        "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveEffectiveSessionID(tt.historyPath); got != tt.want {
				t.Errorf("deriveEffectiveSessionID(%q) = %q, want %q", tt.historyPath, got, tt.want)
			}
		})
	}
}
