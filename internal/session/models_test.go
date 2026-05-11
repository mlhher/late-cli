package session

import (
	"late/internal/client"
	"os"
	"path/filepath"
	"testing"
)

func TestSessionMeta(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "late-session-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Mock SessionDir
	oldSessionDir := SessionDir
	SessionDir = func() (string, error) {
		return tmpDir, nil
	}
	defer func() { SessionDir = oldSessionDir }()

	historyPath := filepath.Join(tmpDir, "session-test.json")
	history := []client.ChatMessage{{Role: "user", Content: client.TextContent("Hello")}}

	s := New(nil, historyPath, history, "", false)
	meta := s.GenerateSessionMeta()

	if meta.ID != "session-test" {
		t.Errorf("Expected ID 'session-test', got %q", meta.ID)
	}

	if err := SaveSessionMeta(meta); err != nil {
		t.Errorf("Failed to save meta: %v", err)
	}

	// Test exact load
	loaded, err := LoadSessionMeta("session-test")
	if err != nil || loaded == nil {
		t.Fatalf("Failed to load meta exactly: %v", err)
	}
	if loaded.ID != "session-test" {
		t.Errorf("Expected loaded ID 'session-test', got %q", loaded.ID)
	}

	// Test prefix load
	loadedPrefix, err := LoadSessionMeta("session-")
	if err != nil || loadedPrefix == nil {
		t.Fatalf("Failed to load meta by prefix: %v", err)
	}
	if loadedPrefix.ID != "session-test" {
		t.Errorf("Expected loaded prefix ID 'session-test', got %q", loadedPrefix.ID)
	}

	// Test ambiguous prefix
	meta2 := meta
	meta2.ID = "session-other"
	SaveSessionMeta(meta2)

	_, err = LoadSessionMeta("session-")
	if err == nil {
		t.Error("Expected error for ambiguous prefix, got nil")
	}
}
