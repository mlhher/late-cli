package session

import (
	"errors"
	"late/internal/client"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestSessionMetadataRetainsSubagentState(t *testing.T) {
	tmpDir := t.TempDir()
	oldSessionDir := SessionDir
	SessionDir = func() (string, error) { return tmpDir, nil }
	t.Cleanup(func() { SessionDir = oldSessionDir })

	saveHistories := false
	s := New(nil, filepath.Join(tmpDir, "session-test.json"), nil, "", false)
	s.SetSubagentMetadata(7, &saveHistories)
	if err := s.AddUserMessage("Hello"); err != nil {
		t.Fatalf("AddUserMessage() error = %v", err)
	}

	loaded, err := LoadSessionMeta("session-test")
	if err != nil {
		t.Fatalf("LoadSessionMeta() error = %v", err)
	}
	if loaded.SubagentSeq != 7 {
		t.Errorf("SubagentSeq = %d, want 7", loaded.SubagentSeq)
	}
	if loaded.SaveSubagentHistories == nil || *loaded.SaveSubagentHistories {
		t.Errorf("SaveSubagentHistories = %v, want false", loaded.SaveSubagentHistories)
	}
}

func TestLoadSessionMetaLegacySubagentState(t *testing.T) {
	tmpDir := t.TempDir()
	oldSessionDir := SessionDir
	SessionDir = func() (string, error) { return tmpDir, nil }
	t.Cleanup(func() { SessionDir = oldSessionDir })

	metaPath := filepath.Join(tmpDir, "session-legacy.meta.json")
	if err := os.WriteFile(metaPath, []byte(`{"id":"session-legacy"}`), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	loaded, err := LoadSessionMeta("session-legacy")
	if err != nil {
		t.Fatalf("LoadSessionMeta() error = %v", err)
	}
	if loaded.SubagentSeq != 0 {
		t.Errorf("SubagentSeq = %d, want 0", loaded.SubagentSeq)
	}
	if loaded.SaveSubagentHistories != nil {
		t.Errorf("SaveSubagentHistories = %v, want nil", *loaded.SaveSubagentHistories)
	}
}

func TestUpdateSubagentSeqRestoresPreviousValueAfterMetadataFailure(t *testing.T) {
	oldSessionDir := SessionDir
	SessionDir = func() (string, error) { return "", errors.New("session directory unavailable") }
	t.Cleanup(func() { SessionDir = oldSessionDir })

	s := New(nil, "session-test.json", nil, "", false)
	s.SetSubagentMetadata(4, nil)
	if err := s.UpdateSubagentSeq(5); err == nil {
		t.Fatal("UpdateSubagentSeq() error = nil, want metadata save failure")
	}
	if s.SubagentSeq() != 4 {
		t.Errorf("SubagentSeq = %d, want 4", s.SubagentSeq())
	}
}

func TestGetLatestSession(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "late-latest-test-*")
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

	// 1. Test when no sessions exist
	latest, err := GetLatestSession()
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if latest != nil {
		t.Errorf("Expected nil latest session when none exist, got %v", latest)
	}

	// 2. Add one session
	meta1 := SessionMeta{
		ID:          "session-1",
		Title:       "First Session",
		LastUpdated: time.Now().Add(-1 * time.Hour),
	}
	if err := SaveSessionMeta(meta1); err != nil {
		t.Fatalf("Failed to save meta1: %v", err)
	}

	latest, err = GetLatestSession()
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if latest == nil || latest.ID != "session-1" {
		t.Errorf("Expected latest session to be 'session-1', got %v", latest)
	}

	// 3. Add a second, newer session
	meta2 := SessionMeta{
		ID:          "session-2",
		Title:       "Second Session",
		LastUpdated: time.Now(),
	}
	if err := SaveSessionMeta(meta2); err != nil {
		t.Fatalf("Failed to save meta2: %v", err)
	}

	latest, err = GetLatestSession()
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if latest == nil || latest.ID != "session-2" {
		t.Errorf("Expected latest session to be 'session-2', got %v", latest)
	}
}

// setupSubagentFolderFixture swaps SessionDir to a fresh temp dir containing:
//   - a legacy flat session (history file + meta file) with ID "session-20250101-123456"
//   - the hierarchical subagent artifacts of another session: a directory
//     "session-20250102-999999/subagents/" holding subagent history files,
//     intentionally WITHOUT a meta file of its own
//
// SessionDir is restored when the test finishes.
func setupSubagentFolderFixture(t *testing.T) {
	t.Helper()

	tmpDir := t.TempDir()

	// Mock SessionDir
	oldSessionDir := SessionDir
	SessionDir = func() (string, error) {
		return tmpDir, nil
	}
	t.Cleanup(func() { SessionDir = oldSessionDir })

	// Legacy session: flat history file + matching meta
	const legacyID = "session-20250101-123456"
	historyPath := filepath.Join(tmpDir, legacyID+".json")
	history := []client.ChatMessage{{Role: "user", Content: client.TextContent("Hello")}}
	if err := SaveHistory(historyPath, history); err != nil {
		t.Fatalf("Failed to save history: %v", err)
	}

	meta := SessionMeta{
		ID:          legacyID,
		Title:       "Legacy Session",
		CreatedAt:   time.Now().Add(-1 * time.Hour),
		LastUpdated: time.Now(),
		HistoryPath: historyPath,
	}
	if err := SaveSessionMeta(meta); err != nil {
		t.Fatalf("Failed to save meta: %v", err)
	}

	// Hierarchical subagent artifacts of another session (no meta file)
	subagentsDir := filepath.Join(tmpDir, "session-20250102-999999", "subagents")
	if err := os.MkdirAll(subagentsDir, 0700); err != nil {
		t.Fatalf("Failed to create subagents dir: %v", err)
	}
	for _, name := range []string{"coder-subagent-0.json", "researcher-subagent-1.json"} {
		if err := os.WriteFile(filepath.Join(subagentsDir, name), []byte("[]"), 0600); err != nil {
			t.Fatalf("Failed to write subagent history %s: %v", name, err)
		}
	}
}

func TestListSessions_IgnoresSubagentFolders(t *testing.T) {
	setupSubagentFolderFixture(t)

	metas, err := ListSessions()
	if err != nil {
		t.Fatalf("Expected no error from ListSessions, got %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("Expected exactly 1 session, got %d: %v", len(metas), metas)
	}
	if metas[0].ID != "session-20250101-123456" {
		t.Errorf("Expected session ID 'session-20250101-123456', got %q", metas[0].ID)
	}

	latest, err := GetLatestSession()
	if err != nil {
		t.Fatalf("Expected no error from GetLatestSession, got %v", err)
	}
	if latest == nil || latest.ID != "session-20250101-123456" {
		t.Errorf("Expected latest session 'session-20250101-123456', got %v", latest)
	}
}

func TestLoadSessionMeta_IgnoresSubagentFolders(t *testing.T) {
	setupSubagentFolderFixture(t)

	// Exact match still works
	exact, err := LoadSessionMeta("session-20250101-123456")
	if err != nil || exact == nil {
		t.Fatalf("Failed to load meta exactly: %v", err)
	}
	if exact.ID != "session-20250101-123456" {
		t.Errorf("Expected loaded ID 'session-20250101-123456', got %q", exact.ID)
	}

	// Prefix matching only the legacy session: the "session-20250102-999999"
	// directory must be skipped by the prefix scan, introducing no new match or ambiguity
	byPrefix, err := LoadSessionMeta("session-2025")
	if err != nil || byPrefix == nil {
		t.Fatalf("Failed to load meta by prefix: %v", err)
	}
	if byPrefix.ID != "session-20250101-123456" {
		t.Errorf("Expected loaded prefix ID 'session-20250101-123456', got %q", byPrefix.ID)
	}

	// Nonexistent ID behaves as before: (nil, nil)
	notFound, err := LoadSessionMeta("nonexistent")
	if err != nil {
		t.Fatalf("Expected no error for nonexistent session, got %v", err)
	}
	if notFound != nil {
		t.Errorf("Expected nil meta for nonexistent session, got %v", notFound)
	}
}
