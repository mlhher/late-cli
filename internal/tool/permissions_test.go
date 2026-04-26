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
