package orchestrator

import (
	"encoding/json"
	"late/internal/client"
	"late/internal/session"
	"late/internal/tool"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeTestConfig writes a minimal late config.json to the temp config dir and
// returns a cleanup function that resets the env.
func writeTestConfig(t *testing.T, enabled bool, threshold int) {
	t.Helper()
	configRoot := t.TempDir()
	if runtime.GOOS != "windows" {
		t.Setenv("XDG_CONFIG_HOME", configRoot)
	} else {
		t.Setenv("APPDATA", configRoot)
	}
	configDir := filepath.Join(configRoot, "late")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{
		"archive_compaction": map[string]any{
			"enabled":                       enabled,
			"compaction_threshold_messages": threshold,
			"keep_recent_messages":          3,
			"archive_chunk_size":            4,
		},
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// newTestOrchestrator builds a minimal BaseOrchestrator with a temp history file.
func newTestOrchestrator(t *testing.T, histPath string, history []client.ChatMessage) *BaseOrchestrator {
	t.Helper()
	sess := session.New(nil, histPath, history, "", false)
	return NewBaseOrchestrator("test-orch", sess, nil, 10)
}

// saveHistoryFile writes a JSON history file.
func saveHistoryFile(t *testing.T, histPath string, msgs []client.ChatMessage) {
	t.Helper()
	data, err := json.MarshalIndent(msgs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(histPath)
	tmp, err := os.CreateTemp(dir, "hist-*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		t.Fatal(err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp.Name(), histPath); err != nil {
		t.Fatal(err)
	}
}

func makeTestMessages(prefix string, n int) []client.ChatMessage {
	msgs := make([]client.ChatMessage, n)
	for i := range msgs {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs[i] = client.ChatMessage{Role: role, Content: prefix + "-" + string(rune('A'+i))}
	}
	return msgs
}

// TestArchiveHook_DisabledIsNoOp verifies that when compaction is disabled,
// runArchivePreHook leaves the history unmodified and creates no archive file.
func TestArchiveHook_DisabledIsNoOp(t *testing.T) {
	writeTestConfig(t, false, 10)
	dir := t.TempDir()
	histPath := filepath.Join(dir, "session-dis.json")
	msgs := makeTestMessages("dis", 20)
	saveHistoryFile(t, histPath, msgs)

	o := newTestOrchestrator(t, histPath, msgs)
	o.runArchivePreHook()

	// Archive file must not be created.
	archPath := histPath[:len(histPath)-len(filepath.Ext(histPath))] + ".archive.json"
	if _, err := os.Stat(archPath); !os.IsNotExist(err) {
		t.Fatal("archive file should not exist when compaction is disabled")
	}
	// In-memory history should remain unchanged.
	if len(o.sess.History) != 20 {
		t.Fatalf("history length changed: got %d, want 20", len(o.sess.History))
	}
	// archiveSub should remain nil.
	if o.archiveSub != nil {
		t.Fatal("archiveSub should be nil when compaction is disabled")
	}
}

// TestArchiveHook_CompactsWhenOverThreshold verifies that when history exceeds
// the compaction threshold, runArchivePreHook reduces the in-memory history and
// registers archive tools.
func TestArchiveHook_CompactsWhenOverThreshold(t *testing.T) {
	writeTestConfig(t, true, 10)
	dir := t.TempDir()
	histPath := filepath.Join(dir, "session-over.json")
	msgs := makeTestMessages("over", 20)
	saveHistoryFile(t, histPath, msgs)

	o := newTestOrchestrator(t, histPath, msgs)
	o.runArchivePreHook()

	// History must be trimmed.
	if len(o.sess.History) >= 20 {
		t.Fatalf("expected history to be trimmed; got %d messages", len(o.sess.History))
	}
	// archiveSub must be populated.
	if o.archiveSub == nil || o.archiveSub.sub == nil {
		t.Fatal("archiveSub should be populated after compaction")
	}
	// Archive tools should be registered.
	reg := o.sess.Registry
	if reg == nil || reg.Get("search_session_archive") == nil {
		t.Fatal("search_session_archive tool should be registered after compaction")
	}
}

// TestArchiveHook_FailureIsNonFatal verifies that runArchivePreHook does not
// panic and does not change the history when HistoryPath is empty (bad config).
func TestArchiveHook_FailureIsNonFatal(t *testing.T) {
	writeTestConfig(t, true, 10)
	// Use an empty HistoryPath — hook must silently return.
	o := &BaseOrchestrator{
		id: "test-orch",
		sess: &session.Session{
			History:  makeTestMessages("fail", 20),
			Registry: tool.NewRegistry(),
		},
		archiveSub: nil,
	}
	// Must not panic.
	o.runArchivePreHook()
	// History remains untouched.
	if len(o.sess.History) != 20 {
		t.Fatalf("FailureIsNonFatal: history changed unexpectedly")
	}
}
