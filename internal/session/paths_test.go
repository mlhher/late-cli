package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSubagentHistoryPath(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "late-session-paths-test-*")
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

	const sessionID = "session-20250101-123456"
	const childID = "researcher-subagent-0"

	wantDir := filepath.Join(tmpDir, sessionID, "subagents")
	gotDir, err := SubagentHistoryDir(sessionID)
	if err != nil {
		t.Fatalf("SubagentHistoryDir returned error: %v", err)
	}
	if gotDir != wantDir {
		t.Errorf("Expected SubagentHistoryDir %q, got %q", wantDir, gotDir)
	}

	wantPath := filepath.Join(wantDir, childID+".json")
	gotPath, err := SubagentHistoryPath(sessionID, childID)
	if err != nil {
		t.Fatalf("SubagentHistoryPath returned error: %v", err)
	}
	if gotPath != wantPath {
		t.Errorf("Expected SubagentHistoryPath %q, got %q", wantPath, gotPath)
	}

	// Neither helper must pre-create directories.
	if _, err := os.Stat(filepath.Join(tmpDir, sessionID)); !os.IsNotExist(err) {
		t.Errorf("Expected no pre-created %q folder, got stat err=%v", sessionID, err)
	}
}

func TestRemoveSessionFolder_RemovesFolderKeepsFlatFiles(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "late-session-rmtest-*")
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

	const sessionID = "session-X"
	subagentsDir := filepath.Join(tmpDir, sessionID, "subagents")
	if err := os.MkdirAll(subagentsDir, 0700); err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(subagentsDir, "coder-subagent-0.json")
	if err := os.WriteFile(childPath, []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}

	flatJSON := filepath.Join(tmpDir, sessionID+".json")
	flatMeta := filepath.Join(tmpDir, sessionID+".meta.json")
	if err := os.WriteFile(flatJSON, []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(flatMeta, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := RemoveSessionFolder(sessionID); err != nil {
		t.Fatalf("RemoveSessionFolder returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(tmpDir, sessionID)); !os.IsNotExist(err) {
		t.Errorf("Expected %q folder to be removed, got stat err=%v", sessionID, err)
	}
	if _, err := os.Stat(childPath); !os.IsNotExist(err) {
		t.Errorf("Expected subagent history %q to be removed, got stat err=%v", childPath, err)
	}
	if _, err := os.Stat(flatJSON); err != nil {
		t.Errorf("Expected flat file %q to survive, got stat err=%v", flatJSON, err)
	}
	if _, err := os.Stat(flatMeta); err != nil {
		t.Errorf("Expected flat file %q to survive, got stat err=%v", flatMeta, err)
	}
}

func TestRemoveSessionFolder_NoopForMissingAndUnsafeIDs(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "late-session-noop-*")
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

	// Never-created ID: no error, nothing deleted.
	if err := RemoveSessionFolder("session-does-not-exist"); err != nil {
		t.Errorf("Expected nil error for missing session ID, got %v", err)
	}

	// Unsafe IDs: no error, nothing deleted, no panic.
	for _, unsafeID := range []string{"", ".", "..", "../evil", "a/b", "..\\windows-evil"} {
		if err := RemoveSessionFolder(unsafeID); err != nil {
			t.Errorf("Expected nil error for unsafe session ID %q, got %v", unsafeID, err)
		}
		if _, err := os.Stat(filepath.Join(tmpDir, "..", "evil")); !os.IsNotExist(err) {
			t.Errorf("Expected no deletion outside temp dir for %q, got stat err=%v", unsafeID, err)
		}
	}

	// The temp dir itself must still exist with an empty listing.
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("Failed to read temp dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Expected temp dir to remain empty, found %d entries: %v", len(entries), entries)
	}
}

func TestSubagentHistoryPathRejectsUnsafeIDs(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "late-session-unsafe-*")
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

	for _, unsafeID := range []string{"", ".", "..", "../x", "a/b"} {
		if _, err := SubagentHistoryDir(unsafeID); err == nil {
			t.Errorf("Expected error from SubagentHistoryDir for unsafe session ID %q, got nil", unsafeID)
		}
		if _, err := SubagentHistoryPath(unsafeID, "coder"); err == nil {
			t.Errorf("Expected error from SubagentHistoryPath for unsafe session ID %q, got nil", unsafeID)
		}
	}

	if _, err := SubagentHistoryPath("session-x", ".."); err == nil {
		t.Errorf("Expected error from SubagentHistoryPath for unsafe child ID %q, got nil", "..")
	}

	// Nothing may have been created on disk.
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("Failed to read temp dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Expected temp dir to remain empty, found %d entries: %v", len(entries), entries)
	}
}

func TestSubagentHistoryPathValidInputs(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "late-session-valid-*")
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

	gotPath, err := SubagentHistoryPath("session-x", "coder-subagent-0")
	if err != nil {
		t.Fatalf("SubagentHistoryPath returned error: %v", err)
	}
	wantPath := filepath.Join(tmpDir, "session-x", "subagents", "coder-subagent-0.json")
	if gotPath != wantPath {
		t.Errorf("Expected SubagentHistoryPath %q, got %q", wantPath, gotPath)
	}
}
