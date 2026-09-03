package tui

import (
	"os"
	"strings"
	"testing"

	"late/internal/config"
	"late/internal/pathutil"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestReasoningPickerAppliesEffortToSession(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", configHome)
	t.Setenv("APPDATA", configHome)

	lateConfigDir, err := pathutil.LateConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(lateConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Models: []config.ModelSetting{
			{
				ID:    "deepseek",
				URL:   "https://api.deepseek.com/v1",
				Model: "deepseek-v4-flash",
				ReasoningMapping: map[string]string{
					"low":  "low",
					"high": "high",
				},
			},
		},
		AgentModels: map[string]config.AgentModelSetting{
			"orchestrator": {Model: "deepseek", Effort: "low"},
		},
	}
	model := NewModel(&mockOrchestrator{}, nil, cfg)
	model.Input.SetValue("> /reasoning")

	// Open /reasoning
	updated, _ := model.updateChat(mockKey{code: '\r', text: "enter"})
	if updated.Mode != ViewReasoningPicker {
		t.Fatalf("mode = %v, want ViewReasoningPicker", updated.Mode)
	}

	// Change effort to "high" (index 4 in ["default", "none", "low", "med", "high", "xhigh"])
	updated.ReasoningPickerAgentSelections["orchestrator"] = 4

	applied := false
	updated.ApplyOrchestratorModel = func(setting config.ModelSetting) tea.Cmd {
		applied = true
		return nil
	}

	saved, _ := updated.updateChat(mockKey{code: '\r', text: "enter"})
	if saved.Mode != ViewChat {
		t.Fatalf("mode = %v, want ViewChat", saved.Mode)
	}
	if got := cfg.AgentModels["orchestrator"].Effort; got != "high" {
		t.Fatalf("configured effort = %q, want high", got)
	}
	if !applied {
		t.Fatal("expected orchestrator model to be reapplied with new reasoning effort")
	}
	if saved.ToastMessage != "reasoning effort saved" {
		t.Fatalf("toast = %q", saved.ToastMessage)
	}

	loaded, err := config.LoadConfig()
	if err != nil {
		t.Fatalf("failed to load saved config: %v", err)
	}
	if got := loaded.AgentModels["orchestrator"].Effort; got != "high" {
		t.Fatalf("persisted effort = %q, want high", got)
	}
}

func TestReasoningPickerPublishesOnlyAfterSaveSucceeds(t *testing.T) {
	configHome := t.TempDir()
	blockingPath := configHome + "/not-a-directory"
	if err := os.WriteFile(blockingPath, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", blockingPath)
	t.Setenv("HOME", configHome)
	t.Setenv("APPDATA", configHome)

	cfg := &config.Config{
		AgentModels: map[string]config.AgentModelSetting{
			"orchestrator": {Model: "deepseek", Effort: "low"},
		},
	}
	model := NewModel(&mockOrchestrator{}, nil, cfg)
	model.Mode = ViewReasoningPicker
	model.ReasoningPickerAgents = []string{"orchestrator"}
	model.ReasoningPickerEfforts = []string{"default", "none", "low", "med", "high", "xhigh"}
	model.ReasoningPickerAgentSelections = map[string]int{"orchestrator": 4} // "high"

	applied := false
	model.ApplyOrchestratorModel = func(config.ModelSetting) tea.Cmd {
		applied = true
		return nil
	}

	updated, _ := model.updateChat(mockKey{code: '\r', text: "enter"})

	if updated.Err == nil {
		t.Fatal("expected config save to fail")
	}
	if got := cfg.AgentModels["orchestrator"].Effort; got != "low" {
		t.Fatalf("configured effort = %q, want low", got)
	}
	if applied {
		t.Fatal("model was applied after config save failed")
	}
}

func TestReasoningPickerCleansUpEmptyAgentSettings(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", configHome)
	t.Setenv("APPDATA", configHome)

	lateConfigDir, err := pathutil.LateConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(lateConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		AgentModels: map[string]config.AgentModelSetting{
			"orchestrator": {Model: "", Effort: "high"},
		},
	}
	model := NewModel(&mockOrchestrator{}, nil, cfg)
	model.Mode = ViewReasoningPicker
	model.ReasoningPickerAgents = []string{"orchestrator"}
	model.ReasoningPickerEfforts = []string{"default", "none", "low", "med", "high", "xhigh"}
	model.ReasoningPickerAgentSelections = map[string]int{"orchestrator": 0} // "default"

	updated, _ := model.updateChat(mockKey{code: '\r', text: "enter"})
	if updated.Mode != ViewChat {
		t.Fatalf("mode = %v, want ViewChat", updated.Mode)
	}
	if _, exists := cfg.AgentModels["orchestrator"]; exists {
		t.Fatal("expected orchestrator to be removed from AgentModels when both model and effort are empty")
	}

	loaded, err := config.LoadConfig()
	if err != nil {
		t.Fatalf("failed to load saved config: %v", err)
	}
	if _, exists := loaded.AgentModels["orchestrator"]; exists {
		t.Fatal("expected orchestrator to be absent from persisted config")
	}
}

func TestReasoningPickerUnavailableWhileAgentIsActive(t *testing.T) {
	model := NewModel(&mockOrchestrator{}, nil, &config.Config{})
	model.GetAgentState("active-child").State = StateThinking
	model.Input.SetValue("> /reasoning")

	updated, _ := model.updateChat(mockKey{code: '\r', text: "enter"})

	if updated.Mode != ViewChat {
		t.Fatalf("mode = %v, want ViewChat", updated.Mode)
	}
	if !updated.ToastWarning {
		t.Fatal("expected a warning toast")
	}
	if updated.ToastMessage != "Reasoning effort can be changed when all agents are idle" {
		t.Fatalf("toast = %q", updated.ToastMessage)
	}
}

func TestWelcomeScreenShowsEffortLevels(t *testing.T) {
	cfg := &config.Config{
		Models: []config.ModelSetting{
			{
				ID:    "deepseek",
				URL:   "https://api.deepseek.com/v1",
				Model: "deepseek-v4-flash",
			},
			{
				ID:    "qwen",
				URL:   "http://localhost:8080",
				Model: "qwen3.5-35b-a3b",
			},
		},
		AgentModels: map[string]config.AgentModelSetting{
			"orchestrator": {Model: "deepseek", Effort: "low"},
			"coder":        {Model: "qwen", Effort: "high"},
		},
	}
	model := NewModel(&mockOrchestrator{}, nil, cfg)
	model.ModelName = "deepseek-v4-flash"
	model.SubagentInfo = "coder:qwen3.5-35b-a3b (effort: high)"
	model.Viewport.SetWidth(80)
	model.Viewport.SetHeight(24)

	plainWelcome := ansi.Strip(model.renderWelcomeMessage())

	if !strings.Contains(plainWelcome, "Model: deepseek-v4-flash (effort: low)") {
		t.Errorf("welcome message should contain model with effort, got: %s", plainWelcome)
	}
	if !strings.Contains(plainWelcome, "coder:qwen3.5-35b-a3b (effort: high)") {
		t.Errorf("welcome message should contain subagent with effort, got: %s", plainWelcome)
	}
}
