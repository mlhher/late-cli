package session

import (
	"encoding/json"
	"late/internal/client"
	"os"
	"path/filepath"
	"testing"
)

func TestSubagentSession_SavesHistoryWithoutMeta(t *testing.T) {
	tmpDir := t.TempDir()

	// Mock SessionDir
	oldSessionDir := SessionDir
	SessionDir = func() (string, error) {
		return tmpDir, nil
	}
	t.Cleanup(func() { SessionDir = oldSessionDir })

	historyPath := filepath.Join(tmpDir, "session-test", "subagents", "coder-subagent-0.json")
	s := NewSubagentSession(nil, historyPath, nil, "sp")

	if err := s.AddUserMessage("hello"); err != nil {
		t.Fatalf("AddUserMessage returned error: %v", err)
	}

	// History must be persisted to the nested subagent path.
	data, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatalf("Expected subagent history file %q to exist, got: %v", historyPath, err)
	}
	var history []client.ChatMessage
	if err := json.Unmarshal(data, &history); err != nil {
		t.Fatalf("Failed to unmarshal subagent history: %v", err)
	}
	if len(history) == 0 {
		t.Errorf("Expected non-empty subagent history, got 0 messages")
	}

	// No top-level .meta.json sidecar must be written for subagents.
	metaPath := filepath.Join(tmpDir, "coder-subagent-0.meta.json")
	if _, err := os.Stat(metaPath); !os.IsNotExist(err) {
		t.Errorf("Expected no top-level meta file %q, got stat err=%v", metaPath, err)
	}
}

func TestRegularSession_StillWritesMeta(t *testing.T) {
	tmpDir := t.TempDir()

	// Mock SessionDir
	oldSessionDir := SessionDir
	SessionDir = func() (string, error) {
		return tmpDir, nil
	}
	t.Cleanup(func() { SessionDir = oldSessionDir })

	historyPath := filepath.Join(tmpDir, "session-test.json")
	s := New(nil, historyPath, nil, "sp", true)

	if err := s.AddUserMessage("hello"); err != nil {
		t.Fatalf("AddUserMessage returned error: %v", err)
	}

	metaPath := filepath.Join(tmpDir, "session-test.meta.json")
	if _, err := os.Stat(metaPath); err != nil {
		t.Errorf("Expected top-level meta file %q to exist, got stat err=%v", metaPath, err)
	}
}
