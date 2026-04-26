package tool

import (
	"encoding/json"
	"late/internal/common"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func withTempCWD(t *testing.T, fn func()) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("failed to chdir temp: %v", err)
	}
	defer func() {
		_ = os.Chdir(orig)
	}()
	fn()
}

func resetApprovalState(now time.Time) {
	sessionApprovalsMu.Lock()
	defer sessionApprovalsMu.Unlock()
	sessionAllowedTools = make(map[string]sessionApproval)
	sessionAllowedCommandMap = make(map[string]map[string]sessionApproval)
	nowFunc = func() time.Time { return now }
}

func TestSessionToolApproval_Expires(t *testing.T) {
	withTempCWD(t, func() {
		base := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
		resetApprovalState(base)
		defer func() { nowFunc = time.Now }()

		SaveSessionAllowedTool("read_file")
		allowed, err := LoadAllAllowedTools()
		if err != nil {
			t.Fatalf("LoadAllAllowedTools error: %v", err)
		}
		if !allowed["read_file"] {
			t.Fatalf("expected read_file to be allowed in session")
		}

		nowFunc = func() time.Time { return base.Add(sessionApprovalTTL + time.Second) }
		allowed, err = LoadAllAllowedTools()
		if err != nil {
			t.Fatalf("LoadAllAllowedTools error after expiry: %v", err)
		}
		if allowed["read_file"] {
			t.Fatalf("expected read_file session approval to expire")
		}
	})
}

func TestSessionCommandApproval_ActiveAndThenExpires(t *testing.T) {
	withTempCWD(t, func() {
		base := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
		resetApprovalState(base)
		defer func() { nowFunc = time.Now }()

		SaveSessionAllowedCommand("wget https://example.com")
		allowed, err := LoadAllAllowedCommands()
		if err != nil {
			t.Fatalf("LoadAllAllowedCommands error: %v", err)
		}
		if _, ok := allowed["wget"]; !ok {
			t.Fatalf("expected wget to be allowed in session")
		}
		if allowed["wget"][sessionBaseMarker] {
			t.Fatalf("expected internal session base marker to stay hidden from merged allowlist")
		}

		nowFunc = func() time.Time { return base.Add(sessionApprovalTTL + time.Second) }
		allowed, err = LoadAllAllowedCommands()
		if err != nil {
			t.Fatalf("LoadAllAllowedCommands error after expiry: %v", err)
		}
		if _, ok := allowed["wget"]; ok {
			t.Fatalf("expected wget session approval to expire")
		}
	})
}

func TestLoadAllowedCommands_RevalidatesByVersionAndExpiry(t *testing.T) {
	withTempCWD(t, func() {
		base := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
		resetApprovalState(base)
		defer func() { nowFunc = time.Now }()

		if err := os.MkdirAll(filepath.Dir(localAllowedCommandsFile), 0755); err != nil {
			t.Fatalf("mkdir failed: %v", err)
		}

		file := persistedCommandsFile{
			Version: common.Version,
			Entries: map[string]persistedCommandEntry{
				"git log": {
					Flags:     []string{"--oneline"},
					ExpiresAt: base.Add(time.Hour).Format(time.RFC3339),
					Version:   common.Version,
				},
				"git status": {
					Flags:     []string{"--porcelain"},
					ExpiresAt: base.Add(-time.Hour).Format(time.RFC3339),
					Version:   common.Version,
				},
				"go test": {
					Flags:     []string{"-v"},
					ExpiresAt: base.Add(time.Hour).Format(time.RFC3339),
					Version:   "different-version",
				},
			},
		}
		data, err := json.Marshal(file)
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}
		if err := os.WriteFile(localAllowedCommandsFile, data, 0644); err != nil {
			t.Fatalf("write failed: %v", err)
		}

		allowed, err := LoadAllowedCommands(false)
		if err != nil {
			t.Fatalf("load failed: %v", err)
		}
		if !allowed["git log"]["--oneline"] {
			t.Fatalf("expected valid entry to load")
		}
		if _, ok := allowed["git status"]; ok {
			t.Fatalf("expected expired entry to be filtered")
		}
		if _, ok := allowed["go test"]; ok {
			t.Fatalf("expected version-mismatched entry to be filtered")
		}
	})
}

func TestLoadAllowedCommands_InvalidExpiryFailsClosed(t *testing.T) {
	withTempCWD(t, func() {
		base := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
		resetApprovalState(base)
		defer func() { nowFunc = time.Now }()

		if err := os.MkdirAll(filepath.Dir(localAllowedCommandsFile), 0755); err != nil {
			t.Fatalf("mkdir failed: %v", err)
		}

		file := persistedCommandsFile{
			Version: common.Version,
			Entries: map[string]persistedCommandEntry{
				"git log": {
					Flags:     []string{"--oneline"},
					ExpiresAt: "not-a-timestamp",
					Version:   common.Version,
				},
			},
		}
		data, err := json.Marshal(file)
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}
		if err := os.WriteFile(localAllowedCommandsFile, data, 0644); err != nil {
			t.Fatalf("write failed: %v", err)
		}

		allowed, err := LoadAllowedCommands(false)
		if err != nil {
			t.Fatalf("load failed: %v", err)
		}
		if _, ok := allowed["git log"]; ok {
			t.Fatalf("expected invalid expires_at entry to fail closed")
		}
	})
}

func TestSaveAllowedCommand_PreservesUntouchedEntryMetadata(t *testing.T) {
	withTempCWD(t, func() {
		base := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
		resetApprovalState(base)
		defer func() { nowFunc = time.Now }()

		if err := os.MkdirAll(filepath.Dir(localAllowedCommandsFile), 0755); err != nil {
			t.Fatalf("mkdir failed: %v", err)
		}

		originalSavedAt := base.Add(-2 * time.Hour).Format(time.RFC3339)
		originalExpiresAt := base.Add(2 * time.Hour).Format(time.RFC3339)
		file := persistedCommandsFile{
			Version: common.Version,
			Entries: map[string]persistedCommandEntry{
				"git log": {
					Flags:     []string{"--oneline"},
					SavedAt:   originalSavedAt,
					ExpiresAt: originalExpiresAt,
					Version:   common.Version,
				},
			},
		}
		data, err := json.Marshal(file)
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}
		if err := os.WriteFile(localAllowedCommandsFile, data, 0644); err != nil {
			t.Fatalf("write failed: %v", err)
		}

		nowFunc = func() time.Time { return base }
		if err := SaveAllowedCommand("grep foo bar", false); err != nil {
			t.Fatalf("SaveAllowedCommand failed: %v", err)
		}

		updatedData, err := os.ReadFile(localAllowedCommandsFile)
		if err != nil {
			t.Fatalf("read failed: %v", err)
		}
		var updated persistedCommandsFile
		if err := json.Unmarshal(updatedData, &updated); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}

		gitLog := updated.Entries["git log"]
		if gitLog.SavedAt != originalSavedAt {
			t.Fatalf("expected untouched entry SavedAt to be preserved, got %q want %q", gitLog.SavedAt, originalSavedAt)
		}
		if gitLog.ExpiresAt != originalExpiresAt {
			t.Fatalf("expected untouched entry ExpiresAt to be preserved, got %q want %q", gitLog.ExpiresAt, originalExpiresAt)
		}

		grep := updated.Entries["grep"]
		if grep.SavedAt != base.Format(time.RFC3339) {
			t.Fatalf("expected new entry SavedAt to be current time, got %q", grep.SavedAt)
		}
		if grep.ExpiresAt != base.Add(projectApprovalTTL).Format(time.RFC3339) {
			t.Fatalf("expected new entry ExpiresAt to use project TTL, got %q", grep.ExpiresAt)
		}
	})
}

func TestSaveAllowedTool_PersistsMetadataFormat(t *testing.T) {
	withTempCWD(t, func() {
		base := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
		resetApprovalState(base)
		defer func() { nowFunc = time.Now }()

		if err := SaveAllowedTool("read_file", false); err != nil {
			t.Fatalf("SaveAllowedTool failed: %v", err)
		}

		data, err := os.ReadFile(localAllowedToolsFile)
		if err != nil {
			t.Fatalf("read failed: %v", err)
		}

		var parsed persistedToolsFile
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("expected metadata format, got unmarshal error: %v", err)
		}
		entry, ok := parsed.Entries["read_file"]
		if !ok {
			t.Fatalf("expected read_file entry in persisted file")
		}
		if entry.Version != common.Version {
			t.Fatalf("expected version %q, got %q", common.Version, entry.Version)
		}

		allowed, err := LoadAllowedTools(false)
		if err != nil {
			t.Fatalf("LoadAllowedTools failed: %v", err)
		}
		if !allowed["read_file"] {
			t.Fatalf("expected read_file to load from metadata format")
		}
	})
}

func TestSaveAllowedTool_PreservesUntouchedEntryMetadata(t *testing.T) {
	withTempCWD(t, func() {
		base := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
		resetApprovalState(base)
		defer func() { nowFunc = time.Now }()

		if err := os.MkdirAll(filepath.Dir(localAllowedToolsFile), 0755); err != nil {
			t.Fatalf("mkdir failed: %v", err)
		}

		originalSavedAt := base.Add(-2 * time.Hour).Format(time.RFC3339)
		originalExpiresAt := base.Add(2 * time.Hour).Format(time.RFC3339)
		file := persistedToolsFile{
			Version: common.Version,
			Entries: map[string]persistedToolEntry{
				"read_file": {
					SavedAt:   originalSavedAt,
					ExpiresAt: originalExpiresAt,
					Version:   common.Version,
				},
			},
		}
		data, err := json.Marshal(file)
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}
		if err := os.WriteFile(localAllowedToolsFile, data, 0644); err != nil {
			t.Fatalf("write failed: %v", err)
		}

		nowFunc = func() time.Time { return base }
		if err := SaveAllowedTool("write_file", false); err != nil {
			t.Fatalf("SaveAllowedTool failed: %v", err)
		}

		updatedData, err := os.ReadFile(localAllowedToolsFile)
		if err != nil {
			t.Fatalf("read failed: %v", err)
		}
		var updated persistedToolsFile
		if err := json.Unmarshal(updatedData, &updated); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}

		readFile := updated.Entries["read_file"]
		if readFile.SavedAt != originalSavedAt {
			t.Fatalf("expected untouched tool SavedAt to be preserved, got %q want %q", readFile.SavedAt, originalSavedAt)
		}
		if readFile.ExpiresAt != originalExpiresAt {
			t.Fatalf("expected untouched tool ExpiresAt to be preserved, got %q want %q", readFile.ExpiresAt, originalExpiresAt)
		}

		writeFile := updated.Entries["write_file"]
		if writeFile.SavedAt != base.Format(time.RFC3339) {
			t.Fatalf("expected new tool SavedAt to be current time, got %q", writeFile.SavedAt)
		}
		if writeFile.ExpiresAt != base.Add(projectApprovalTTL).Format(time.RFC3339) {
			t.Fatalf("expected new tool ExpiresAt to use project TTL, got %q", writeFile.ExpiresAt)
		}
	})
}
