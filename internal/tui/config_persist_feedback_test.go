package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"late/internal/config"
	"late/internal/pathutil"
)

// isolateConfigHome points the OS user-config directory at a fresh temp dir
// for the duration of the test, so SaveConfig never touches the developer's
// real config.json (the same isolation model_picker_test.go uses; darwin's
// os.UserConfigDir ignores XDG_CONFIG_HOME, hence the HOME/APPDATA override).
func isolateConfigHome(t *testing.T) string {
	t.Helper()
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", configHome)
	t.Setenv("APPDATA", configHome)
	return configHome
}

// TestThemePickerWarnsWhenSaveRefusesDegradedConfig drives the theme picker's
// enter path with a degraded config (constructed the way the app really gets
// one: config.LoadConfig over a malformed config.json). The theme applies for
// the session, but SaveConfig must refuse to overwrite the user's broken file
// — and the refusal must surface on the focused agent's status line instead
// of being silently swallowed.
func TestThemePickerWarnsWhenSaveRefusesDegradedConfig(t *testing.T) {
	isolateConfigHome(t)
	lateDir, err := pathutil.LateConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(lateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lateDir, "config.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig()
	if err == nil {
		t.Fatal("expected a parse error from the malformed config.json")
	}
	if !cfg.Degraded {
		t.Fatal("expected LoadConfig to mark the fallback config degraded")
	}

	model := NewModel(&mockOrchestrator{}, nil, cfg)
	model.Mode = ViewThemes
	model.ThemeEntries = []ThemeEntry{makeTheme("ocean:deep", "ocean", "deep")}
	model.ThemeIndex = 0

	updated, _ := model.updateChat(mockKey{code: '\r', text: "enter"})

	if !strings.Contains(updated.ToastMessage, "theme applied") {
		t.Fatalf("toast = %q, want the theme-applied confirmation", updated.ToastMessage)
	}
	if updated.SelectedTheme != "ocean:deep" {
		t.Fatalf("SelectedTheme = %q, want ocean:deep (the session keeps the theme)", updated.SelectedTheme)
	}
	status := updated.GetAgentState(updated.Focused.ID()).StatusText
	if !strings.Contains(status, "settings changed but won't persist") {
		t.Fatalf("StatusText = %q, want the persistence-failure warning", status)
	}
	if !strings.Contains(status, "refusing to save config") {
		t.Fatalf("StatusText = %q, want it to carry SaveConfig's refusal reason", status)
	}
	// The refusal is the point: the malformed file must survive untouched.
	data, err := os.ReadFile(filepath.Join(lateDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{not json" {
		t.Fatalf("degraded config.json was overwritten: %q", string(data))
	}
}

// TestThemesCommandWarnsWhenSaveRefusesDegradedConfig drives the
// /themes <name> inline path with a config flagged degraded directly: same
// contract as the picker — apply in memory, warn on the status line.
func TestThemesCommandWarnsWhenSaveRefusesDegradedConfig(t *testing.T) {
	isolateConfigHome(t)
	cfg := &config.Config{Degraded: true}
	model := NewModel(&mockOrchestrator{}, nil, cfg)
	model.ThemeEntries = []ThemeEntry{makeTheme("ocean:deep", "ocean", "deep")}
	model.Input.SetValue("/themes deep")

	updated, _ := model.updateChat(mockKey{code: '\r', text: "enter"})

	if updated.SelectedTheme != "ocean:deep" {
		t.Fatalf("SelectedTheme = %q, want ocean:deep", updated.SelectedTheme)
	}
	status := updated.GetAgentState(updated.Focused.ID()).StatusText
	if !strings.Contains(status, "settings changed but won't persist") {
		t.Fatalf("StatusText = %q, want the persistence-failure warning", status)
	}
	if !strings.Contains(status, "refusing to save config") {
		t.Fatalf("StatusText = %q, want it to carry SaveConfig's refusal reason", status)
	}
}

// TestThemePickerNoWarningWhenPersistenceSucceeds pins the healthy path: a
// non-degraded config saves, and no persistence warning appears on the
// status line.
func TestThemePickerNoWarningWhenPersistenceSucceeds(t *testing.T) {
	isolateConfigHome(t)
	lateDir, err := pathutil.LateConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	// LoadConfig (fresh install) is what normally creates the dir; mirror
	// that here so SaveConfig's permission tightening finds it.
	if err := os.MkdirAll(lateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	model := NewModel(&mockOrchestrator{}, nil, cfg)
	model.Mode = ViewThemes
	model.ThemeEntries = []ThemeEntry{makeTheme("ocean:deep", "ocean", "deep")}
	model.ThemeIndex = 0

	updated, _ := model.updateChat(mockKey{code: '\r', text: "enter"})

	status := updated.GetAgentState(updated.Focused.ID()).StatusText
	if strings.Contains(status, "won't persist") {
		t.Fatalf("StatusText = %q, want no persistence warning when SaveConfig succeeds", status)
	}
	// And the choice really persisted to the isolated config dir.
	data, err := os.ReadFile(filepath.Join(lateDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved config.Config
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("saved config.json is not valid JSON: %v", err)
	}
	if saved.Theme != "ocean:deep" {
		t.Fatalf("persisted theme = %q, want ocean:deep", saved.Theme)
	}
}
