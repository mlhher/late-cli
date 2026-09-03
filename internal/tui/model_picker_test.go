package tui

import (
	"late/internal/config"
	"late/internal/pathutil"
	"os"
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestModelPickerAppliesOrchestratorModelImmediately(t *testing.T) {
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
			{URL: "https://example.test/v1", Key: "secret", Model: "new-model"},
		},
	}
	model := NewModel(&mockOrchestrator{}, nil, cfg)
	model.Mode = ViewModelPicker
	model.ModelPickerAgents = []string{"orchestrator"}
	model.ModelPickerModels = []string{"default", "new-model"}
	model.ModelPickerAgentSelections = map[string]int{"orchestrator": 1}

	var applied config.ModelSetting
	model.ApplyOrchestratorModel = func(setting config.ModelSetting) tea.Cmd {
		applied = setting
		return nil
	}

	updated, _ := model.updateChat(mockKey{code: '\r', text: "enter"})

	if !reflect.DeepEqual(applied, cfg.Models[0]) {
		t.Fatalf("applied model = %#v, want %#v", applied, cfg.Models[0])
	}
	if updated.ModelName != "new-model" {
		t.Fatalf("displayed model = %q, want %q", updated.ModelName, "new-model")
	}
	if updated.Mode != ViewChat {
		t.Fatalf("mode = %v, want ViewChat", updated.Mode)
	}
}

func TestModelPickerPersistsStableModelID(t *testing.T) {
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
			{ID: "provider-a", URL: "https://a.example/v1", Model: "shared-model"},
			{ID: "provider-b", URL: "https://b.example/v1", Model: "shared-model"},
		},
	}
	model := NewModel(&mockOrchestrator{}, nil, cfg)
	model.Mode = ViewModelPicker
	model.ModelPickerAgents = []string{"orchestrator"}
	model.ModelPickerModels = []string{"default", "provider-a", "provider-b"}
	model.ModelPickerAgentSelections = map[string]int{"orchestrator": 2}

	updated, _ := model.updateChat(mockKey{code: '\r', text: "enter"})

	if got := cfg.AgentModels["orchestrator"].Model; got != "provider-b" {
		t.Fatalf("configured model reference = %q, want provider-b", got)
	}
	if updated.ModelName != "shared-model" {
		t.Fatalf("displayed model = %q, want shared-model", updated.ModelName)
	}
}

func TestModelPickerUnavailableWhileAgentIsActive(t *testing.T) {
	model := NewModel(&mockOrchestrator{}, nil, &config.Config{})
	model.GetAgentState("active-child").State = StateThinking
	model.Input.SetValue("> /model")

	updated, _ := model.updateChat(mockKey{code: '\r', text: "enter"})

	if updated.Mode != ViewChat {
		t.Fatalf("mode = %v, want ViewChat", updated.Mode)
	}
	if !updated.ToastWarning {
		t.Fatal("expected a warning toast")
	}
	if updated.ToastMessage != "Models can be changed when all agents are idle" {
		t.Fatalf("toast = %q", updated.ToastMessage)
	}
}

func TestModelPickerDoesNotSaveWhileAgentBecomesActive(t *testing.T) {
	cfg := &config.Config{
		Models: []config.ModelSetting{
			{URL: "https://example.test/v1", Model: "new-model"},
		},
		AgentModels: map[string]config.AgentModelSetting{"orchestrator": {Model: "old-model"}},
	}
	model := NewModel(&mockOrchestrator{}, nil, cfg)
	model.Mode = ViewModelPicker
	model.ModelPickerAgents = []string{"orchestrator"}
	model.ModelPickerModels = []string{"default", "new-model"}
	model.ModelPickerAgentSelections = map[string]int{"orchestrator": 1}
	model.GetAgentState("active-child").State = StateStreaming

	applied := false
	model.ApplyOrchestratorModel = func(config.ModelSetting) tea.Cmd {
		applied = true
		return nil
	}

	updated, _ := model.updateChat(mockKey{code: '\r', text: "enter"})

	if updated.Mode != ViewModelPicker {
		t.Fatalf("mode = %v, want ViewModelPicker", updated.Mode)
	}
	if got := cfg.AgentModels["orchestrator"].Model; got != "old-model" {
		t.Fatalf("configured model = %q, want old-model", got)
	}
	if applied {
		t.Fatal("model was applied while an agent was active")
	}
}

func TestModelPickerPublishesOnlyAfterSaveSucceeds(t *testing.T) {
	configHome := t.TempDir()
	blockingPath := configHome + "/not-a-directory"
	if err := os.WriteFile(blockingPath, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", blockingPath)
	t.Setenv("HOME", configHome)
	t.Setenv("APPDATA", configHome)

	cfg := &config.Config{
		Models: []config.ModelSetting{
			{URL: "https://example.test/v1", Model: "new-model"},
		},
		AgentModels: map[string]config.AgentModelSetting{"orchestrator": {Model: "old-model"}},
	}
	model := NewModel(&mockOrchestrator{}, nil, cfg)
	model.Mode = ViewModelPicker
	model.ModelPickerAgents = []string{"orchestrator"}
	model.ModelPickerModels = []string{"default", "new-model"}
	model.ModelPickerAgentSelections = map[string]int{"orchestrator": 1}

	applied := false
	model.ApplyOrchestratorModel = func(config.ModelSetting) tea.Cmd {
		applied = true
		return nil
	}

	updated, _ := model.updateChat(mockKey{code: '\r', text: "enter"})

	if updated.Err == nil {
		t.Fatal("expected config save to fail")
	}
	if got := cfg.AgentModels["orchestrator"].Model; got != "old-model" {
		t.Fatalf("configured model = %q, want old-model", got)
	}
	if applied {
		t.Fatal("model was applied after config save failed")
	}
}

func TestModelPickerPreservesReasoningEffortWhenModelSetToDefault(t *testing.T) {
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
			{URL: "https://example.test/v1", Model: "new-model"},
		},
		AgentModels: map[string]config.AgentModelSetting{
			"orchestrator": {Model: "new-model", Effort: "high"},
		},
	}
	model := NewModel(&mockOrchestrator{}, nil, cfg)
	model.Mode = ViewModelPicker
	model.ModelPickerAgents = []string{"orchestrator"}
	model.ModelPickerModels = []string{"default", "new-model"}
	model.ModelPickerAgentSelections = map[string]int{"orchestrator": 0} // Select "default"

	updated, _ := model.updateChat(mockKey{code: '\r', text: "enter"})
	if updated.Mode != ViewChat {
		t.Fatalf("mode = %v, want ViewChat", updated.Mode)
	}
	if got := cfg.AgentModels["orchestrator"].Model; got != "" {
		t.Fatalf("model = %q, want empty", got)
	}
	if got := cfg.AgentModels["orchestrator"].Effort; got != "high" {
		t.Fatalf("effort = %q, want high", got)
	}

	loaded, err := config.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if got := loaded.AgentModels["orchestrator"].Effort; got != "high" {
		t.Fatalf("persisted effort = %q, want high", got)
	}
}
