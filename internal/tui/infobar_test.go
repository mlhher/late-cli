package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"late/internal/common"
	"late/internal/config"
	"late/internal/orchestrator"
	"late/internal/session"
	"late/internal/tool"
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

func TestInfoBarRenderContents(t *testing.T) {
	cfg := &config.Config{
		Models:      []config.ModelSetting{{ID: "provider-a", URL: "https://a.example/v1", Key: "k", Model: "gpt-test"}},
		AgentModels: map[string]string{"researcher": "provider-a"},
	}
	m := NewModel(&mockOrchestrator{}, nil, cfg)
	m.ShowInfoBar = true
	m.Width = 160
	m.ModelName = "fallback-model"
	m.PermissionMode = config.PermissionModeAskForUserApproval
	// Focus a researcher subagent: its type has an explicit agent_models entry.
	m.Focused = &typedOrchestrator{mockOrchestrator{}, "researcher-subagent-0"}

	plain := ansi.Strip(m.infoBarView())
	for _, want := range []string{"a.example · gpt-test", "approval ask", "todos –"} {
		if !strings.Contains(plain, want) {
			t.Errorf("expected %q in %q", want, plain)
		}
	}
	// Nothing that duplicates the status bar or says little.
	for _, unwanted := range []string{"late v", "ctx", "skills", "threshold", "up ", "subagents", "turn"} {
		if strings.Contains(plain, unwanted) {
			t.Errorf("unexpected %q in %q", unwanted, plain)
		}
	}

	// Subagents with work in flight are counted; the root state is not, and
	// one blocked on the user is called out separately.
	m.GetAgentState("researcher-subagent-0").State = StateStreaming
	m.GetAgentState("coder-subagent-1").State = StateThinking
	m.GetAgentState("coder-subagent-2").State = StateConfirmTool
	plain = ansi.Strip(m.infoBarView())
	if !strings.Contains(plain, "subagents 2 running, 1 awaiting approval") {
		t.Errorf("expected subagent summary in %q", plain)
	}
}

func TestEndpointHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://api.deepseek.com", "api.deepseek.com"},
		{"https://API.DeepSeek.com/v1/", "api.deepseek.com"},
		{"http://localhost:9931", "localhost:9931"},
		{"http://localhost:9931/v1", "localhost:9931"},
		{"localhost:9931", "localhost:9931"},
		{"https://user:secret@host.example:8443/x", "host.example:8443"},
		{"http://[::1]:8080/v1", "[::1]:8080"},
		{"", ""},
		{"   ", ""},
		{"http://bad host/", ""},
	}
	for _, tc := range cases {
		if got := endpointHost(tc.in); got != tc.want {
			t.Errorf("endpointHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestInfoBarClipsLongHostAndModel(t *testing.T) {
	cfg := &config.Config{
		Models:      []config.ModelSetting{{ID: "x", URL: "https://a-very-long-internal-hostname.corp.example.com", Model: strings.Repeat("m", 60)}},
		AgentModels: map[string]string{"orchestrator": "x"},
	}
	m := NewModel(&mockOrchestrator{}, nil, cfg)
	m.Focused = &typedOrchestrator{mockOrchestrator{}, common.MainAgentID}
	m.ShowInfoBar = true
	m.Width = 200
	plain := ansi.Strip(m.infoBarView())
	if strings.Contains(plain, "corp.example.com") || strings.Contains(plain, strings.Repeat("m", 41)) {
		t.Errorf("host and model should be clipped to their caps: %q", plain)
	}
	if strings.Count(plain, "…") != 2 {
		t.Errorf("expected one ellipsis for the host and one for the model: %q", plain)
	}
}

func TestInfoBarFallbackHostFollowsResolvedEndpoints(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "http://main.test:1")
	t.Setenv("LATE_SUBAGENT_BASE_URL", "http://sub.test:2")
	m := NewModel(&mockOrchestrator{}, nil, &config.Config{})
	m.Focused = &typedOrchestrator{mockOrchestrator{}, common.MainAgentID}
	m.ModelName = "main-model"
	m.SubagentInfo = "sub-model"

	if host, name := m.focusedModelInfo(); host != "main.test:1" || name != "main-model" {
		t.Errorf("orchestrator = (%q, %q), want (main.test:1, main-model)", host, name)
	}
	m.Focused = &typedOrchestrator{mockOrchestrator{}, "coder-subagent-0"}
	if host, name := m.focusedModelInfo(); host != "sub.test:2" || name != "sub-model" {
		t.Errorf("subagent = (%q, %q), want (sub.test:2, sub-model)", host, name)
	}
}

func TestInfoBarModelFallsBackToConfiguredName(t *testing.T) {
	m := NewModel(&mockOrchestrator{}, nil, &config.Config{})
	m.ShowInfoBar = true
	m.Width = 120
	m.ModelName = "solo-model"
	if plain := ansi.Strip(m.infoBarView()); !strings.Contains(plain, "solo-model") {
		t.Errorf("expected fallback model name in %q", plain)
	}
}

func TestInfoBarApprovalMode(t *testing.T) {
	m := NewModel(&mockOrchestrator{}, nil, &config.Config{})
	m.ShowInfoBar = true
	m.Width = 120

	m.PermissionMode = config.PermissionModeAskForUserApproval
	if plain := ansi.Strip(m.infoBarView()); !strings.Contains(plain, "approval ask") {
		t.Errorf("default mode should read 'approval ask': %q", plain)
	}
	m.PermissionMode = ""
	if plain := ansi.Strip(m.infoBarView()); !strings.Contains(plain, "approval ask") {
		t.Errorf("unset mode defaults to asking: %q", plain)
	}
	m.PermissionMode = config.PermissionModeUnsupervised
	if plain := ansi.Strip(m.infoBarView()); !strings.Contains(plain, "approval off") {
		t.Errorf("unsupervised mode should read 'approval off': %q", plain)
	}
}

func TestInfoBarTodoProgress(t *testing.T) {
	rootSess := session.New(nil, "", nil, "", false)
	var todos []tool.Todo
	var mu sync.Mutex
	rootSess.Registry.Register(tool.CreateTodosTool{Todos: &todos, Mu: &mu})
	rootSess.Registry.Register(tool.ListTodosTool{Todos: &todos, Mu: &mu})
	rootSess.Registry.Register(tool.FinishTodoTool{Todos: &todos, Mu: &mu})
	root := orchestrator.NewBaseOrchestrator(common.MainAgentID, rootSess, nil, 0)

	m := NewModel(root, nil, &config.Config{})
	m.ShowInfoBar = true
	m.Width = 120
	if plain := ansi.Strip(m.infoBarView()); !strings.Contains(plain, "todos –") {
		t.Errorf("no todos should read 'todos –': %q", plain)
	}

	ctx := context.WithValue(context.Background(), common.OrchestratorIDKey, common.MainAgentID)
	if _, err := rootSess.Registry.Get("create_todos").Execute(ctx, json.RawMessage(`{"todos": ["one", "two", "three"]}`)); err != nil {
		t.Fatal(err)
	}
	if plain := ansi.Strip(m.infoBarView()); !strings.Contains(plain, "todos 0/3") {
		t.Errorf("expected 'todos 0/3' in %q", plain)
	}
}

func TestInfoBarTurnTimerFollowsAgentActivity(t *testing.T) {
	m := NewModel(&mockOrchestrator{}, nil, &config.Config{})
	m.ShowInfoBar = true
	m.Width = 120

	if plain := ansi.Strip(m.infoBarView()); strings.Contains(plain, "turn") {
		t.Fatalf("no turn has happened yet: %q", plain)
	}

	// An agent starts working: the timer starts at that update.
	s := m.GetAgentState(m.Focused.ID())
	s.State = StateThinking
	t0 := time.Now()
	m.turn.observe(m.hasActiveAgent(), t0)
	d, running, ok := m.turn.elapsed(t0.Add(42 * time.Second))
	if !ok || !running || d != 42*time.Second {
		t.Fatalf("running turn = (%v, %v, %v), want (42s, true, true)", d, running, ok)
	}
	if plain := ansi.Strip(m.infoBarView()); !strings.Contains(plain, "turn ") {
		t.Fatalf("running turn should show on the bar: %q", plain)
	}

	// A message queued mid-turn is not a new idle→busy edge: no reset.
	m.turn.observe(m.hasActiveAgent(), t0.Add(10*time.Second))
	if d, _, _ := m.turn.elapsed(t0.Add(42 * time.Second)); d != 42*time.Second {
		t.Fatalf("turn reset mid-run: %v", d)
	}

	// Everything goes idle: the value freezes and stays.
	s.State = StateIdle
	m.turn.observe(m.hasActiveAgent(), t0.Add(2*time.Minute))
	d, running, ok = m.turn.elapsed(t0.Add(time.Hour))
	if !ok || running || d != 2*time.Minute {
		t.Fatalf("frozen turn = (%v, %v, %v), want (2m, false, true)", d, running, ok)
	}

	// The next send starts a fresh turn.
	s.State = StateThinking
	t1 := t0.Add(3 * time.Minute)
	m.turn.observe(m.hasActiveAgent(), t1)
	if d, running, _ := m.turn.elapsed(t1.Add(5 * time.Second)); !running || d != 5*time.Second {
		t.Fatalf("new turn = (%v, %v), want (5s, true)", d, running)
	}
}

func TestInfoBarTurnTimerRunsWhileAnySubagentWorks(t *testing.T) {
	m := NewModel(&mockOrchestrator{}, nil, &config.Config{})
	t0 := time.Now()
	m.GetAgentState("coder-subagent-1").State = StateStreaming
	m.turn.observe(m.hasActiveAgent(), t0)
	// The root is idle but a subagent still works: the turn continues.
	m.turn.observe(m.hasActiveAgent(), t0.Add(time.Minute))
	if _, running, _ := m.turn.elapsed(t0.Add(2 * time.Minute)); !running {
		t.Fatal("turn should run while any subagent works")
	}
}

func TestUpdateStartsAndFreezesTurnTimer(t *testing.T) {
	m := NewModel(&mockOrchestrator{}, nil, &config.Config{})
	m.SetSize(120, 30)
	s := m.GetAgentState(m.Focused.ID())

	s.State = StateThinking
	updated, _ := m.Update(spinner.TickMsg{})
	m = updated.(Model)
	if _, running, ok := m.turn.elapsed(time.Now()); !ok || !running {
		t.Fatal("Update should start the turn timer while an agent is busy")
	}
	s.State = StateIdle
	updated, _ = m.Update(spinner.TickMsg{})
	m = updated.(Model)
	if _, running, ok := m.turn.elapsed(time.Now()); !ok || running {
		t.Fatal("Update should freeze the turn timer once every agent is idle")
	}
}

func TestTurnDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{7 * time.Second, "7s"},
		{42 * time.Second, "42s"},
		{time.Minute, "1m00s"},
		{102 * time.Second, "1m42s"},
		{59*time.Minute + 59*time.Second, "59m59s"},
		{62 * time.Minute, "1h02m"},
		{25 * time.Hour, "25h00m"},
	}
	for _, tc := range cases {
		if got := turnDuration(tc.d); got != tc.want {
			t.Errorf("turnDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestInfoBarDropsWholeSegmentsWhenNarrow(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "http://h.test:1")
	m := NewModel(&mockOrchestrator{}, nil, &config.Config{})
	m.ShowInfoBar = true
	m.ModelName = "model-x"
	m.PermissionMode = config.PermissionModeAskForUserApproval
	m.GetAgentState("coder-subagent-1").State = StateStreaming
	t0 := time.Now().Add(-90 * time.Second)
	m.GetAgentState(m.Focused.ID()).State = StateThinking
	m.turn.observe(true, t0)

	widths := map[int][]string{
		120: {"h.test:1 · model-x", "approval ask", "todos", "subagents 1 running", "turn 1m30s"},
		86:  {"h.test:1 · model-x", "approval ask", "todos", "subagents 1 running", "turn 1m30s"},
		80:  {"model-x", "approval ask", "todos", "subagents 1 running", "turn 1m30s"},
		70:  {"model-x", "approval ask", "todos", "subagents 1 running"},
		62:  {"model-x", "approval ask", "todos", "subagents 1 running"},
		50:  {"model-x", "approval ask", "subagents 1 running"},
		44:  {"model-x", "approval ask"},
		30:  {"model-x", "approval ask"},
		12:  {"model-x"},
	}
	for w, wants := range widths {
		m.Width = w
		view := m.infoBarView()
		if got := lipgloss.Width(view); got != w {
			t.Errorf("width %d: row is %d wide", w, got)
		}
		plain := ansi.Strip(view)
		for _, want := range wants {
			if !strings.Contains(plain, want) {
				t.Errorf("width %d: expected %q in %q", w, want, plain)
			}
		}
		if w < 86 && strings.Contains(plain, "h.test:1") {
			t.Errorf("width %d: the host should be the first thing dropped: %q", w, plain)
		}
		if w < 80 && strings.Contains(plain, "turn") {
			t.Errorf("width %d: the turn time should be dropped before todos: %q", w, plain)
		}
		if strings.Contains(plain, "…") && w >= 30 {
			t.Errorf("width %d: segments should drop whole, not truncate: %q", w, plain)
		}
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
	m.Width = 20
	m.ModelName = "a-very-long-model-name-that-will-not-fit"

	view := m.infoBarView()
	if strings.Contains(view, "\n") {
		t.Fatalf("info bar must never wrap, got %q", view)
	}
	if got := lipgloss.Width(view); got != m.Width {
		t.Fatalf("info bar width = %d, want %d (padded to full row)", got, m.Width)
	}
	if plain := ansi.Strip(view); !strings.Contains(plain, "…") {
		t.Errorf("the model name is the last thing truncated, expected an ellipsis: %q", plain)
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
