package tui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"late/internal/common"
	"late/internal/config"
)

// setUserConfigEnv isolates config.json writes (the /infobar toggle persists
// via config.SaveConfig) into a temp dir, mirroring the config package's own
// test helper.
func setUserConfigEnv(t *testing.T, configRoot string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("APPDATA", configRoot)
	if runtime.GOOS != "windows" {
		t.Setenv("HOME", configRoot)
	}
}

func pressEnter(t *testing.T, m Model) Model {
	t.Helper()
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", updated)
	}
	return next
}

func TestInfoBarTogglePersistsToConfig(t *testing.T) {
	configRoot := t.TempDir()
	setUserConfigEnv(t, configRoot)
	// Resolve the config dir the way pathutil does (os.UserConfigDir —
	// XDG_CONFIG_HOME is ignored on darwin, HOME/APPDATA are not).
	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(userConfigDir, "late"), 0o700); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{}
	m := NewModel(&mockOrchestrator{}, nil, cfg)
	m.SetSize(120, 30)
	if m.ShowInfoBar {
		t.Fatal("ShowInfoBar should default to false when unset in config")
	}

	// Toggle on: view flag, config struct, and the persisted file all flip.
	m.Input.SetValue("/infobar")
	m = pressEnter(t, m)
	if !m.ShowInfoBar {
		t.Fatal("expected ShowInfoBar to be true after /infobar")
	}
	if m.ToastMessage != "info bar on" {
		t.Fatalf("toast = %q, want %q", m.ToastMessage, "info bar on")
	}
	if !cfg.ShowInfoBar {
		t.Fatal("expected cfg.ShowInfoBar to be true after /infobar")
	}
	loaded, err := config.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if !loaded.ShowInfoBar {
		t.Fatal("expected show-info-bar to be persisted as true")
	}

	// Toggle off again.
	m.Input.SetValue("/infobar")
	m = pressEnter(t, m)
	if m.ShowInfoBar {
		t.Fatal("expected ShowInfoBar to be false after second /infobar")
	}
	if m.ToastMessage != "info bar off" {
		t.Fatalf("toast = %q, want %q", m.ToastMessage, "info bar off")
	}
	if cfg.ShowInfoBar {
		t.Fatal("expected cfg.ShowInfoBar to be false after second /infobar")
	}
	loaded, err = config.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if loaded.ShowInfoBar {
		t.Fatal("expected show-info-bar to be persisted as false")
	}
}

func TestInfoBarToggleSaveFailureSurfacesStatusText(t *testing.T) {
	configRoot := t.TempDir()
	setUserConfigEnv(t, configRoot)
	// Make the late config dir path a regular file so SaveConfig cannot
	// create the atomic temp file inside it.
	blocking := filepath.Join(configRoot, "late")
	if err := os.WriteFile(blocking, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{}
	m := NewModel(&mockOrchestrator{}, nil, cfg)
	m.SetSize(120, 30)
	m.Input.SetValue("/infobar")
	m = pressEnter(t, m)

	if !m.ShowInfoBar {
		t.Fatal("view toggle should still apply when saving fails")
	}
	state := m.GetAgentState(m.Focused.ID())
	if state.StatusText != "failed to save info bar setting" {
		t.Fatalf("StatusText = %q, want save-failure status", state.StatusText)
	}
}

func TestInfoBarToggleReservesLayoutRow(t *testing.T) {
	m := NewModel(&mockOrchestrator{}, nil, &config.Config{})
	m.SetSize(80, 24)

	if m.infoBarHeight() != 0 {
		t.Fatalf("infoBarHeight() = %d, want 0 while disabled", m.infoBarHeight())
	}
	vpOff := m.Viewport.Height()
	fpOff := m.FilePicker.Height()

	m.ShowInfoBar = true
	m.updateLayout()

	if m.infoBarHeight() != InfoBarHeight {
		t.Fatalf("infoBarHeight() = %d, want %d", m.infoBarHeight(), InfoBarHeight)
	}
	if got := m.Viewport.Height(); got != vpOff-1 {
		t.Fatalf("viewport height = %d, want %d (one row reserved for the info bar)", got, vpOff-1)
	}
	if got := m.FilePicker.Height(); got != fpOff-1 {
		t.Fatalf("file picker height = %d, want %d", got, fpOff-1)
	}
}

// typedOrchestrator lets a test focus a subagent-style ID ("<type>-subagent-n")
// so the config.AgentModels lookup path is exercised.
type typedOrchestrator struct {
	mockOrchestrator
	id string
}

func (m *typedOrchestrator) ID() string { return m.id }

func TestInfoBarRenderContents(t *testing.T) {
	cfg := &config.Config{
		Models:      []config.ModelSetting{{ID: "provider-a", URL: "https://a.example/v1", Key: "k", Model: "gpt-test"}},
		AgentModels: map[string]string{"researcher": "provider-a"},
	}
	m := NewModel(&mockOrchestrator{}, nil, cfg)
	m.ShowInfoBar = true
	m.Width = 200
	m.CWD = "/home/user/myproject"
	m.ModelName = "fallback-model"
	m.SkillsInfo = SkillsInfo{Count: 2, Tokens: 2500}
	// Focus a researcher subagent: its type has an explicit agent_models entry.
	m.Focused = &typedOrchestrator{mockOrchestrator{}, "researcher-subagent-0"}

	state := m.GetAgentState(m.Focused.ID())
	state.CumulativeTokenCount = 20 // mockOrchestrator.MaxTokens() == 100
	state.CreatedAt = time.Now().Add(-2 * time.Hour)

	plain := ansi.Strip(m.infoBarView())

	if !strings.Contains(plain, "late v"+common.Version) {
		t.Errorf("expected version segment in %q", plain)
	}
	if !strings.Contains(plain, "myproject") {
		t.Errorf("expected project folder basename in %q", plain)
	}
	// Focused agent type "orchestrator" has an explicit entry: stable ref · model name.
	if !strings.Contains(plain, "provider-a · gpt-test") {
		t.Errorf("expected provider/profile ref and model name in %q", plain)
	}
	if !strings.Contains(plain, "20%") || !strings.Contains(plain, "20/100") {
		t.Errorf("expected context usage bar (20%% of 100) in %q", plain)
	}
	if !strings.Contains(plain, "subagents: 0 running") {
		t.Errorf("expected running-subagent count in %q", plain)
	}
	if !strings.Contains(plain, "skills: 2 (~2k tok)") {
		t.Errorf("expected skills segment with estimate in %q", plain)
	}
	// Headroom to the default 80%% threshold: 100*80/100 - 20 = 60 tokens.
	if !strings.Contains(plain, "~60 tokens to threshold") {
		t.Errorf("expected threshold headroom segment in %q", plain)
	}
	if !strings.Contains(plain, "up 2h") {
		t.Errorf("expected uptime segment in %q", plain)
	}

	// Subagents with work in flight are counted; the root state is not.
	m.GetAgentState("researcher-subagent-0").State = StateStreaming
	m.GetAgentState("coder-subagent-1").State = StateThinking
	m.GetAgentState("coder-subagent-2").State = StateConfirmTool // waiting, not running
	plain = ansi.Strip(m.infoBarView())
	if !strings.Contains(plain, "subagents: 2 running") {
		t.Errorf("expected 2 running subagents in %q", plain)
	}
}

// unknownCtxOrchestrator reports an unknown context size (ContextSize -1)
// so the info bar's threshold-headroom segment can be exercised as omitted.
type unknownCtxOrchestrator struct {
	mockOrchestrator
}

func (m *unknownCtxOrchestrator) MaxTokens() int { return -1 }

func TestInfoBarSegmentsOmittedWhenUnknown(t *testing.T) {
	m := NewModel(&unknownCtxOrchestrator{mockOrchestrator{}}, nil, &config.Config{})
	m.ShowInfoBar = true
	m.Width = 140
	m.ModelName = "solo-model"
	m.SkillsInfo = SkillsInfo{} // no skills discovered

	plain := ansi.Strip(m.infoBarView())
	if strings.Contains(plain, "skills:") {
		t.Errorf("skills segment should be omitted when no skills exist: %q", plain)
	}
	if strings.Contains(plain, "tokens to threshold") {
		t.Errorf("threshold segment should be omitted when ctx size is unknown: %q", plain)
	}
	if !strings.Contains(plain, "solo-model") {
		t.Errorf("expected fallback orchestrator model name in %q", plain)
	}
	if !strings.Contains(plain, "late v"+common.Version) {
		t.Errorf("expected version segment in %q", plain)
	}
}

func TestInfoBarHiddenWhenDisabled(t *testing.T) {
	m := NewModel(&mockOrchestrator{}, nil, &config.Config{})
	m.Width = 120
	m.CWD = "/tmp/project"
	if got := m.infoBarView(); got != "" {
		t.Fatalf("infoBarView() = %q, want empty while disabled", got)
	}

	// Even when enabled, the bar never renders while the file picker is open.
	m.ShowInfoBar = true
	m.ShowFilePicker = true
	if got := m.infoBarView(); got != "" {
		t.Fatalf("infoBarView() = %q, want empty while the file picker is open", got)
	}
	if got := m.infoBarHeight(); got != 0 {
		t.Fatalf("infoBarHeight() = %d, want 0 while the file picker is open", got)
	}
}

func TestInfoBarSingleLineTruncatesToWidth(t *testing.T) {
	m := NewModel(&mockOrchestrator{}, nil, nil)
	m.ShowInfoBar = true
	m.Width = 30
	m.CWD = "/home/user/some-repo"
	m.ModelName = "a-very-long-model-name-that-will-not-fit"
	m.SkillsInfo = SkillsInfo{Count: 3, Tokens: 42000}

	view := m.infoBarView()
	if strings.Contains(view, "\n") {
		t.Fatalf("info bar must never wrap, got %q", view)
	}
	if got := lipgloss.Width(view); got != m.Width {
		t.Fatalf("info bar width = %d, want %d (padded to full row)", got, m.Width)
	}
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "…") {
		t.Errorf("expected an ellipsis on the truncated row, got %q", plain)
	}
}

func TestFormatUptime(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{42 * time.Second, "42s"},
		{time.Minute, "1m"},
		{75 * time.Minute, "1h15m"},
		{3 * time.Hour, "3h"},
		{26 * time.Hour, "1d2h"},
		{48 * time.Hour, "2d"},
	}
	for _, tc := range cases {
		if got := formatUptime(tc.d); got != tc.want {
			t.Errorf("formatUptime(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestCompactionHeadroomTokens(t *testing.T) {
	cases := []struct {
		name              string
		current, max, pct int
		want              int
	}{
		{"below threshold", 20, 100, 80, 60},
		{"at threshold", 80, 100, 80, 0},
		{"over threshold clamps at zero", 120, 100, 80, 0},
		{"custom threshold", 10, 1000, 50, 490},
		{"unknown ctx size", 10, -1, 80, 0},
		{"unlimited ctx size", 10, 0, 80, 0},
		{"zero pct falls back to default", 0, 100, 0, 80},
		{"pct over 100 clamps", 0, 100, 150, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := compactionHeadroomTokens(tc.current, tc.max, tc.pct); got != tc.want {
				t.Fatalf("compactionHeadroomTokens(%d, %d, %d) = %d, want %d",
					tc.current, tc.max, tc.pct, got, tc.want)
			}
		})
	}
}

func TestAgentTypeForID(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		{common.MainAgentID, "orchestrator"},
		{"", "orchestrator"},
		{"researcher-subagent-3", "researcher"},
		{"subagent-9", ""}, // no "-subagent-" separator → unknown type
	}
	for _, tc := range cases {
		if got := agentTypeForID(tc.id); got != tc.want {
			t.Errorf("agentTypeForID(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}

func TestInfoBarCommandListed(t *testing.T) {
	found := false
	for _, cmd := range AvailableCommands {
		if cmd.Name == "/infobar" {
			found = true
			if cmd.Description == "" {
				t.Fatal("/infobar should carry a description for the help view")
			}
		}
	}
	if !found {
		t.Fatal("/infobar missing from AvailableCommands")
	}
}
