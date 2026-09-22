package session

import (
	"errors"
	"fmt"
	"late/internal/client"
	"os"
	"path/filepath"
	"slices"
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

// newWorkingDirMeta builds a session meta recording dir as its project folder,
// with the surrounding fields modeled on the metas saved by the other tests.
func newWorkingDirMeta(sessionsDir, id, dir string) SessionMeta {
	return SessionMeta{
		ID:             id,
		Title:          "Session " + id,
		CreatedAt:      time.Now().Add(-24 * time.Hour),
		LastUpdated:    time.Now().Add(-24 * time.Hour),
		HistoryPath:    filepath.Join(sessionsDir, id+".json"),
		LastUserPrompt: "Hello",
		MessageCount:   1,
		WorkingDir:     dir,
	}
}

// setMetaModTimes pins each session's .meta.json file to the given mod time so
// the newest-wins selection in GetLatestSessionForDir is deterministic.
func setMetaModTimes(t *testing.T, sessionsDir string, times map[string]time.Time) {
	t.Helper()
	for id, modTime := range times {
		metaPath := filepath.Join(sessionsDir, id+".meta.json")
		if err := os.Chtimes(metaPath, modTime, modTime); err != nil {
			t.Fatalf("os.Chtimes(%s) error = %v", metaPath, err)
		}
	}
}

func TestGetLatestSessionForDir_ReturnsNewestForDirectory(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "late-latest-for-dir-test-*")
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

	// Three sessions across two project folders; /proj/a has two candidates.
	for _, meta := range []SessionMeta{
		newWorkingDirMeta(tmpDir, "session-20250101-100000", "/proj/a"),
		newWorkingDirMeta(tmpDir, "session-20250102-100000", "/proj/a"),
		newWorkingDirMeta(tmpDir, "session-20250103-100000", "/proj/b"),
	} {
		if err := SaveSessionMeta(meta); err != nil {
			t.Fatalf("Failed to save meta %s: %v", meta.ID, err)
		}
	}

	// Enforce deterministic mtime ordering: the newest session overall is the
	// /proj/b one, while /proj/a's newest is session-20250102-100000.
	base := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	setMetaModTimes(t, tmpDir, map[string]time.Time{
		"session-20250101-100000": base,
		"session-20250102-100000": base.Add(1 * time.Hour),
		"session-20250103-100000": base.Add(2 * time.Hour),
	})

	latestA, err := GetLatestSessionForDir("/proj/a")
	if err != nil {
		t.Fatalf("GetLatestSessionForDir(/proj/a): %v", err)
	}
	if latestA == nil || latestA.ID != "session-20250102-100000" {
		t.Fatalf("GetLatestSessionForDir(/proj/a) = %v, want session-20250102-100000 (newest /proj/a session, not the globally newest one)", latestA)
	}

	latestB, err := GetLatestSessionForDir("/proj/b")
	if err != nil {
		t.Fatalf("GetLatestSessionForDir(/proj/b): %v", err)
	}
	if latestB == nil || latestB.ID != "session-20250103-100000" {
		t.Fatalf("GetLatestSessionForDir(/proj/b) = %v, want session-20250103-100000", latestB)
	}
}

func TestGetLatestSessionForDir_NoMatchReturnsNil(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "late-latest-for-dir-test-*")
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

	if err := SaveSessionMeta(newWorkingDirMeta(tmpDir, "session-20250101-100000", "/proj/a")); err != nil {
		t.Fatalf("Failed to save meta: %v", err)
	}

	latest, err := GetLatestSessionForDir("/proj/other")
	if err != nil {
		t.Fatalf("GetLatestSessionForDir(/proj/other): %v", err)
	}
	if latest != nil {
		t.Errorf("Expected nil latest session for a directory with no sessions, got %v", latest)
	}
}

func TestGetLatestSessionForDir_IgnoresSessionsWithoutWorkingDir(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "late-latest-for-dir-test-*")
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

	// Legacy meta with no working_dir key, hand-crafted like a
	// pre-working-dir session sidecar.
	legacyID := "session-20250101-legacy"
	legacyMetaPath := filepath.Join(tmpDir, legacyID+".meta.json")
	legacyJSON := fmt.Sprintf(`{"id":"%s","history_path":"%s"}`, legacyID, filepath.Join(tmpDir, legacyID+".json"))
	if err := os.WriteFile(legacyMetaPath, []byte(legacyJSON), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := SaveSessionMeta(newWorkingDirMeta(tmpDir, "session-20250102-100000", "/proj/a")); err != nil {
		t.Fatalf("Failed to save meta: %v", err)
	}

	// Make the legacy session the globally newest one: it must still never be
	// selected because it records no working_dir.
	base := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	setMetaModTimes(t, tmpDir, map[string]time.Time{
		legacyID:                  base.Add(2 * time.Hour),
		"session-20250102-100000": base.Add(1 * time.Hour),
	})

	latestLegacy, err := GetLatestSessionForDir("/proj/legacy")
	if err != nil {
		t.Fatalf("GetLatestSessionForDir(/proj/legacy): %v", err)
	}
	if latestLegacy != nil {
		t.Errorf("Expected nil latest session for a directory with no matching sessions, got %v", latestLegacy)
	}

	latestA, err := GetLatestSessionForDir("/proj/a")
	if err != nil {
		t.Fatalf("GetLatestSessionForDir(/proj/a): %v", err)
	}
	if latestA == nil || latestA.ID != "session-20250102-100000" {
		t.Fatalf("GetLatestSessionForDir(/proj/a) = %v, want session-20250102-100000 (the legacy meta without working_dir must be skipped despite its newer mtime)", latestA)
	}
}

func TestSessionMeta_WorkingDirRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	oldSessionDir := SessionDir
	SessionDir = func() (string, error) { return tmpDir, nil }
	t.Cleanup(func() { SessionDir = oldSessionDir })

	// Constructed like TestSessionMetadataRetainsSubagentState: New captures
	// the current working directory as the session's project folder.
	s := New(nil, filepath.Join(tmpDir, "session-test.json"), nil, "", false)
	if err := s.AddUserMessage("Hello"); err != nil {
		t.Fatalf("AddUserMessage() error = %v", err)
	}

	loaded, err := LoadSessionMeta("session-test")
	if err != nil || loaded == nil {
		t.Fatalf("LoadSessionMeta() error = %v", err)
	}
	if loaded.WorkingDir != tmpDir {
		t.Errorf("WorkingDir = %q, want %q", loaded.WorkingDir, tmpDir)
	}

	// Resume path: the working dir is restored explicitly and must survive the
	// next metadata save.
	s.SetWorkingDir("/restored/path")
	if err := s.AddUserMessage("Hello again"); err != nil {
		t.Fatalf("AddUserMessage() after SetWorkingDir error = %v", err)
	}

	loaded, err = LoadSessionMeta("session-test")
	if err != nil || loaded == nil {
		t.Fatalf("LoadSessionMeta() after SetWorkingDir error = %v", err)
	}
	if loaded.WorkingDir != "/restored/path" {
		t.Errorf("WorkingDir after SetWorkingDir = %q, want %q", loaded.WorkingDir, "/restored/path")
	}
}

// TestGetLatestSessionForDir_SkipsVanishedSidecar makes the enumeration-time
// race deterministic. A dangling symlink's lstat (entry.Info) succeeds while
// reading it (loadMetaFile) fails with ENOENT — exactly the "meta file
// disappeared after entry.Info() but before the load" window that used to
// yield a (nil, nil) meta and panic on meta.WorkingDir. The scan must skip
// such a sidecar without panicking and still find the healthy session.
func TestGetLatestSessionForDir_SkipsVanishedSidecar(t *testing.T) {
	tmpDir := t.TempDir()
	oldSessionDir := SessionDir
	SessionDir = func() (string, error) { return tmpDir, nil }
	t.Cleanup(func() { SessionDir = oldSessionDir })

	// Vanished sidecar: lstat succeeds, read fails.
	vanishedPath := filepath.Join(tmpDir, "session-20250101-vanished.meta.json")
	if err := os.Symlink(filepath.Join(tmpDir, "gone.target"), vanishedPath); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	if err := SaveSessionMeta(newWorkingDirMeta(tmpDir, "session-20250102-100000", "/proj/a")); err != nil {
		t.Fatalf("Failed to save meta: %v", err)
	}

	latest, err := GetLatestSessionForDir("/proj/a") // must not panic
	if err != nil {
		t.Fatalf("GetLatestSessionForDir(/proj/a): %v", err)
	}
	if latest == nil || latest.ID != "session-20250102-100000" {
		t.Fatalf("GetLatestSessionForDir(/proj/a) = %v, want session-20250102-100000 (the vanished sidecar must be skipped, not crash the scan)", latest)
	}
}

// TestGetLatestSessionForDir_NilMetaNeverDereferenced injects the historical
// (nil, nil) loader result through the loadEnumeratedMeta seam and asserts
// the scan skips it instead of dereferencing meta.WorkingDir — defense in
// depth beyond loadMetaFile's (meta, error) contract.
func TestGetLatestSessionForDir_NilMetaNeverDereferenced(t *testing.T) {
	tmpDir := t.TempDir()
	oldSessionDir := SessionDir
	SessionDir = func() (string, error) { return tmpDir, nil }
	t.Cleanup(func() { SessionDir = oldSessionDir })

	if err := SaveSessionMeta(newWorkingDirMeta(tmpDir, "session-20250101-100000", "/proj/a")); err != nil {
		t.Fatalf("Failed to save meta: %v", err)
	}

	oldLoad := loadEnumeratedMeta
	loadEnumeratedMeta = func(path string) (*SessionMeta, error) {
		return nil, nil // simulate the race result: nothing loaded, no error
	}
	t.Cleanup(func() { loadEnumeratedMeta = oldLoad })

	latest, err := GetLatestSessionForDir("/proj/a") // must not panic
	if err != nil {
		t.Fatalf("GetLatestSessionForDir(/proj/a): %v", err)
	}
	if latest != nil {
		t.Fatalf("GetLatestSessionForDir(/proj/a) = %+v, want nil when every sidecar loads as (nil, nil)", latest)
	}
}

// TestGetLatestSessionForDir_LoadsExactEnumeratedFiles pins the exact-file
// loading rule with prefix-colliding IDs. The vanished
// "session-20250101.meta.json" sidecar's ID is a prefix of the surviving
// "session-20250101-abcdef" and carries the newest mtime: with
// LoadSessionMeta-style prefix fallback the scan would resurrect the abcdef
// meta under the vanished entry's newer mtime and wrongly beat the genuinely
// newest session. The loader must also see the enumerated paths verbatim.
func TestGetLatestSessionForDir_LoadsExactEnumeratedFiles(t *testing.T) {
	tmpDir := t.TempDir()
	oldSessionDir := SessionDir
	SessionDir = func() (string, error) { return tmpDir, nil }
	t.Cleanup(func() { SessionDir = oldSessionDir })

	if err := SaveSessionMeta(newWorkingDirMeta(tmpDir, "session-20250101-abcdef", "/proj/a")); err != nil {
		t.Fatalf("Failed to save meta: %v", err)
	}
	if err := SaveSessionMeta(newWorkingDirMeta(tmpDir, "session-20250102-x", "/proj/a")); err != nil {
		t.Fatalf("Failed to save meta: %v", err)
	}
	// Vanished sidecar whose ID prefix-collides with session-20250101-abcdef.
	vanishedPath := filepath.Join(tmpDir, "session-20250101.meta.json")
	if err := os.Symlink(filepath.Join(tmpDir, "gone.target"), vanishedPath); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	// Both real sessions are older than the vanished sidecar's (current)
	// mtime, so a fallback resurrection would win the newest-wins race.
	base := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	setMetaModTimes(t, tmpDir, map[string]time.Time{
		"session-20250101-abcdef": base.Add(1 * time.Hour),
		"session-20250102-x":      base.Add(2 * time.Hour),
	})

	oldLoad := loadEnumeratedMeta
	t.Cleanup(func() { loadEnumeratedMeta = oldLoad })
	var loaded []string
	loadEnumeratedMeta = func(path string) (*SessionMeta, error) {
		loaded = append(loaded, path)
		return loadMetaFile(path)
	}

	latest, err := GetLatestSessionForDir("/proj/a")
	if err != nil {
		t.Fatalf("GetLatestSessionForDir(/proj/a): %v", err)
	}
	if latest == nil || latest.ID != "session-20250102-x" {
		t.Fatalf("GetLatestSessionForDir(/proj/a) = %v, want session-20250102-x (the vanished prefix-colliding sidecar must not resurrect session-20250101-abcdef under its newer mtime)", latest)
	}

	wantPaths := []string{
		filepath.Join(tmpDir, "session-20250101-abcdef.meta.json"),
		filepath.Join(tmpDir, "session-20250101.meta.json"),
		filepath.Join(tmpDir, "session-20250102-x.meta.json"),
	}
	slices.Sort(loaded) // ReadDir order is already sorted; sort defensively
	slices.Sort(wantPaths)
	if !slices.Equal(loaded, wantPaths) {
		t.Errorf("loader saw paths %v, want the exact enumerated files %v", loaded, wantPaths)
	}
}

// TestGetLatestSession_SkipsVanishedSidecar mirrors the exact-file rule on
// the global --continue path: a vanished sidecar must be skipped, never
// re-resolved through the ID-prefix fallback to a different session.
func TestGetLatestSession_SkipsVanishedSidecar(t *testing.T) {
	tmpDir := t.TempDir()
	oldSessionDir := SessionDir
	SessionDir = func() (string, error) { return tmpDir, nil }
	t.Cleanup(func() { SessionDir = oldSessionDir })

	if err := SaveSessionMeta(newWorkingDirMeta(tmpDir, "session-20250101-abcdef", "/proj/a")); err != nil {
		t.Fatalf("Failed to save meta: %v", err)
	}
	if err := SaveSessionMeta(newWorkingDirMeta(tmpDir, "session-20250102-x", "/proj/b")); err != nil {
		t.Fatalf("Failed to save meta: %v", err)
	}
	vanishedPath := filepath.Join(tmpDir, "session-20250101.meta.json")
	if err := os.Symlink(filepath.Join(tmpDir, "gone.target"), vanishedPath); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	// Same mtime setup as the --continue-project variant: the vanished
	// sidecar is the newest entry, and its ID prefix-collides with
	// session-20250101-abcdef.
	base := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	setMetaModTimes(t, tmpDir, map[string]time.Time{
		"session-20250101-abcdef": base.Add(1 * time.Hour),
		"session-20250102-x":      base.Add(2 * time.Hour),
	})

	latest, err := GetLatestSession() // must not panic
	if err != nil {
		t.Fatalf("GetLatestSession(): %v", err)
	}
	if latest == nil || latest.ID != "session-20250102-x" {
		t.Fatalf("GetLatestSession() = %v, want session-20250102-x (the vanished sidecar must be skipped, not fall back to session-20250101-abcdef)", latest)
	}
}

// TestGetLatestSessionForDir_MatchesViaSymlinkIdentity covers directory
// identity matching: the session records the real directory while the lookup
// goes through a symlink to it. The lexical fast path misses, but os.SameFile
// must still match. A lookup directory that exists nowhere must return
// (nil, nil) without an error, and a recorded directory that has since been
// deleted must still match through the lexical fast path.
func TestGetLatestSessionForDir_MatchesViaSymlinkIdentity(t *testing.T) {
	tmpDir := t.TempDir()
	oldSessionDir := SessionDir
	SessionDir = func() (string, error) { return tmpDir, nil }
	t.Cleanup(func() { SessionDir = oldSessionDir })

	realDir := filepath.Join(tmpDir, "real-project")
	if err := os.MkdirAll(realDir, 0700); err != nil {
		t.Fatalf("creating real-project: %v", err)
	}
	linkDir := filepath.Join(tmpDir, "linked-project")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	if err := SaveSessionMeta(newWorkingDirMeta(tmpDir, "session-20250101-100000", realDir)); err != nil {
		t.Fatalf("Failed to save meta: %v", err)
	}

	latest, err := GetLatestSessionForDir(linkDir)
	if err != nil {
		t.Fatalf("GetLatestSessionForDir(%s): %v", linkDir, err)
	}
	if latest == nil || latest.ID != "session-20250101-100000" {
		t.Fatalf("GetLatestSessionForDir(%s) = %v, want session-20250101-100000 found through the symlink via os.SameFile", linkDir, latest)
	}

	// A directory that exists nowhere: no error, no match.
	missing := filepath.Join(tmpDir, "does-not-exist")
	latest, err = GetLatestSessionForDir(missing)
	if err != nil {
		t.Fatalf("GetLatestSessionForDir(%s): %v", missing, err)
	}
	if latest != nil {
		t.Fatalf("GetLatestSessionForDir(%s) = %+v, want nil for a missing directory", missing, latest)
	}

	// A recorded project directory that has since been deleted must still be
	// matched lexically (identity cannot be checked on a missing directory).
	gone := filepath.Join(tmpDir, "gone-project")
	if err := os.Mkdir(gone, 0700); err != nil {
		t.Fatalf("creating gone-project: %v", err)
	}
	if err := SaveSessionMeta(newWorkingDirMeta(tmpDir, "session-20250102-100000", gone)); err != nil {
		t.Fatalf("Failed to save meta: %v", err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatalf("removing gone-project: %v", err)
	}

	latest, err = GetLatestSessionForDir(gone)
	if err != nil {
		t.Fatalf("GetLatestSessionForDir(%s): %v", gone, err)
	}
	if latest == nil || latest.ID != "session-20250102-100000" {
		t.Fatalf("GetLatestSessionForDir(%s) = %v, want session-20250102-100000 matched lexically despite the directory being gone", gone, latest)
	}
}
