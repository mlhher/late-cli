package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadConfig_MissingFileCreatesDefault(t *testing.T) {
	configRoot := t.TempDir()
	setUserConfigEnv(t, configRoot)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg == nil {
		t.Fatal("LoadConfig() returned nil config")
	}
	if !cfg.EnabledTools["read_file"] || !cfg.EnabledTools["bash"] {
		t.Fatalf("LoadConfig() missing default enabled tools: %#v", cfg.EnabledTools)
	}

	configPath := lateConfigPath(t)
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("expected config file to be created at %s: %v", configPath, err)
	}
	if cfg.OpenAIBaseURL != "" || cfg.OpenAIAPIKey != "" || cfg.OpenAIModel != "" {
		t.Fatal("expected default OpenAI fields to be empty")
	}

	if runtime.GOOS != "windows" {
		dirInfo, err := os.Stat(filepath.Dir(configPath))
		if err != nil {
			t.Fatalf("failed to stat config directory: %v", err)
		}
		if got := dirInfo.Mode().Perm(); got != 0o700 {
			t.Fatalf("config dir permissions = %o, want %o", got, 0o700)
		}

		fileInfo, err := os.Stat(configPath)
		if err != nil {
			t.Fatalf("failed to stat config file: %v", err)
		}
		if got := fileInfo.Mode().Perm(); got != 0o600 {
			t.Fatalf("config file permissions = %o, want %o", got, 0o600)
		}
	}
}

func TestLoadConfig_ExistingFileTightensPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not reliably comparable on Windows")
	}

	configRoot := t.TempDir()
	setUserConfigEnv(t, configRoot)
	configPath := lateConfigPath(t)
	configDir := filepath.Dir(configPath)

	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"enabled_tools":{"bash":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg == nil {
		t.Fatal("LoadConfig() returned nil config")
	}

	dirInfo, err := os.Stat(configDir)
	if err != nil {
		t.Fatalf("failed to stat config directory: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("config dir permissions = %o, want %o", got, 0o700)
	}

	fileInfo, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("failed to stat config file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("config file permissions = %o, want %o", got, 0o600)
	}
}

func TestLoadConfig_ParsesLegacyConfig(t *testing.T) {
	configRoot := t.TempDir()
	setUserConfigEnv(t, configRoot)
	configPath := lateConfigPath(t)

	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"enabled_tools":{"bash":false,"read_file":true}}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.EnabledTools["bash"] {
		t.Fatal("expected bash to be disabled from legacy config")
	}
	if !cfg.EnabledTools["read_file"] {
		t.Fatal("expected read_file to remain enabled from legacy config")
	}
	if cfg.OpenAIBaseURL != "" || cfg.OpenAIAPIKey != "" || cfg.OpenAIModel != "" {
		t.Fatal("expected legacy config to leave OpenAI fields empty")
	}
}

func TestLoadConfig_ParsesOpenAIFields(t *testing.T) {
	configRoot := t.TempDir()
	setUserConfigEnv(t, configRoot)
	configPath := lateConfigPath(t)

	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatal(err)
	}
	content := `{
		"enabled_tools": {"bash": true},
		"openai_base_url": "https://example.test/v1",
		"openai_api_key": "secret",
		"openai_model": "gpt-test",
		"late_subagent_base_url": "https://subagent.example/v1",
		"late_subagent_api_key": "sub-secret",
		"late_subagent_model": "qwen-sub"
	}`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.OpenAIBaseURL != "https://example.test/v1" {
		t.Fatalf("OpenAIBaseURL = %q", cfg.OpenAIBaseURL)
	}
	if cfg.OpenAIAPIKey != "secret" {
		t.Fatalf("OpenAIAPIKey = %q", cfg.OpenAIAPIKey)
	}
	if cfg.OpenAIModel != "gpt-test" {
		t.Fatalf("OpenAIModel = %q", cfg.OpenAIModel)
	}
	if cfg.LateSubagentBaseURL != "https://subagent.example/v1" {
		t.Fatalf("LateSubagentBaseURL = %q", cfg.LateSubagentBaseURL)
	}
	if cfg.LateSubagentAPIKey != "sub-secret" {
		t.Fatalf("LateSubagentAPIKey = %q", cfg.LateSubagentAPIKey)
	}
	if cfg.LateSubagentModel != "qwen-sub" {
		t.Fatalf("LateSubagentModel = %q", cfg.LateSubagentModel)
	}
}

func TestLoadConfig_OpenAIOnlyConfigDefaultsEnabledTools(t *testing.T) {
	configRoot := t.TempDir()
	setUserConfigEnv(t, configRoot)
	configPath := lateConfigPath(t)

	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{
		"openai_base_url": "https://example.test/v1",
		"openai_api_key": "secret",
		"openai_model": "gpt-test"
	}`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg == nil {
		t.Fatal("LoadConfig() returned nil config")
	}

	if cfg.OpenAIBaseURL != "https://example.test/v1" {
		t.Fatalf("OpenAIBaseURL = %q", cfg.OpenAIBaseURL)
	}
	if cfg.OpenAIAPIKey != "secret" {
		t.Fatalf("OpenAIAPIKey = %q", cfg.OpenAIAPIKey)
	}
	if cfg.OpenAIModel != "gpt-test" {
		t.Fatalf("OpenAIModel = %q", cfg.OpenAIModel)
	}

	if cfg.EnabledTools == nil {
		t.Fatal("EnabledTools is nil")
	}

	for toolName, wantEnabled := range defaultConfig().EnabledTools {
		gotEnabled, ok := cfg.EnabledTools[toolName]
		if !ok {
			t.Fatalf("expected default tool %q to be present", toolName)
		}
		if gotEnabled != wantEnabled {
			t.Fatalf("EnabledTools[%q] = %v, want %v", toolName, gotEnabled, wantEnabled)
		}
	}
}

func TestLoadConfig_MalformedFileFallsBackWithError(t *testing.T) {
	configRoot := t.TempDir()
	setUserConfigEnv(t, configRoot)
	configPath := lateConfigPath(t)

	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"enabled_tools":`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig()
	if err == nil {
		t.Fatal("expected parse error for malformed config")
	}
	if cfg == nil {
		t.Fatal("expected fallback config despite parse error")
	}
	if !cfg.EnabledTools["write_file"] || !cfg.EnabledTools["target_edit"] {
		t.Fatalf("expected fallback default tools, got %#v", cfg.EnabledTools)
	}
}

func TestLoadConfig_ReadErrorFallsBackWithError(t *testing.T) {
	configRoot := t.TempDir()
	setUserConfigEnv(t, configRoot)
	configPath := lateConfigPath(t)

	if err := os.MkdirAll(configPath, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig()
	if err == nil {
		t.Fatal("expected read error when config path is a directory")
	}
	if cfg == nil {
		t.Fatal("expected fallback config despite read error")
	}
	if !cfg.EnabledTools["read_file"] || !cfg.EnabledTools["bash"] {
		t.Fatalf("expected fallback default tools, got %#v", cfg.EnabledTools)
	}
}

func TestLoadConfig_DefaultCreateFailureFallsBackWithError(t *testing.T) {
	configRoot := t.TempDir()
	blockingPath := filepath.Join(configRoot, "not-a-dir")
	if err := os.WriteFile(blockingPath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	setUserConfigEnv(t, blockingPath)

	cfg, err := LoadConfig()
	if err == nil {
		t.Fatal("expected error when config directory cannot be created")
	}
	if cfg == nil {
		t.Fatal("expected fallback config despite creation failure")
	}
	if !cfg.EnabledTools["read_file"] || !cfg.EnabledTools["bash"] {
		t.Fatalf("expected fallback default tools, got %#v", cfg.EnabledTools)
	}
}

func setUserConfigEnv(t *testing.T, configRoot string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("APPDATA", configRoot)
	if runtime.GOOS != "windows" {
		t.Setenv("HOME", configRoot)
	}
}

func lateConfigPath(t *testing.T) string {
	t.Helper()

	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir() error = %v", err)
	}

	return filepath.Join(configDir, "late", "config.json")
}

func TestResolveOpenAISettings(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *Config
		env     map[string]string
		present map[string]bool
		want    OpenAISettings
	}{
		{
			name: "env only",
			env: map[string]string{
				"OPENAI_BASE_URL": "https://env.example",
				"OPENAI_API_KEY":  "env-key",
				"OPENAI_MODEL":    "env-model",
			},
			present: map[string]bool{
				"OPENAI_BASE_URL": true,
				"OPENAI_API_KEY":  true,
				"OPENAI_MODEL":    true,
			},
			want: OpenAISettings{BaseURL: "https://env.example", APIKey: "env-key", Model: "env-model"},
		},
		{
			name: "config only",
			cfg: &Config{
				OpenAIBaseURL: "https://config.example",
				OpenAIAPIKey:  "config-key",
				OpenAIModel:   "config-model",
			},
			want: OpenAISettings{BaseURL: "https://config.example", APIKey: "config-key", Model: "config-model"},
		},
		{
			name: "env wins over config",
			cfg: &Config{
				OpenAIBaseURL: "https://config.example",
				OpenAIAPIKey:  "config-key",
				OpenAIModel:   "config-model",
			},
			env: map[string]string{
				"OPENAI_BASE_URL": "https://env.example",
				"OPENAI_API_KEY":  "env-key",
				"OPENAI_MODEL":    "env-model",
			},
			present: map[string]bool{
				"OPENAI_BASE_URL": true,
				"OPENAI_API_KEY":  true,
				"OPENAI_MODEL":    true,
			},
			want: OpenAISettings{BaseURL: "https://env.example", APIKey: "env-key", Model: "env-model"},
		},
		{
			name: "none set uses default URL",
			want: OpenAISettings{BaseURL: DefaultOpenAIBaseURL},
		},
		{
			name: "empty env falls back to config",
			cfg: &Config{
				OpenAIBaseURL: "https://config.example",
				OpenAIAPIKey:  "config-key",
				OpenAIModel:   "config-model",
			},
			env: map[string]string{
				"OPENAI_BASE_URL": "",
				"OPENAI_API_KEY":  "",
				"OPENAI_MODEL":    "",
			},
			present: map[string]bool{
				"OPENAI_BASE_URL": true,
				"OPENAI_API_KEY":  true,
				"OPENAI_MODEL":    true,
			},
			want: OpenAISettings{BaseURL: "https://config.example", APIKey: "config-key", Model: "config-model"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveOpenAISettingsWithEnv(tt.cfg, func(key string) (string, bool) {
				value, ok := tt.env[key]
				if tt.present != nil {
					ok = tt.present[key]
				}
				return value, ok
			})

			if got.BaseURL != tt.want.BaseURL {
				t.Fatalf("BaseURL = %q, want %q", got.BaseURL, tt.want.BaseURL)
			}
			if got.APIKey != tt.want.APIKey {
				t.Fatalf("APIKey = %q, want %q", got.APIKey, tt.want.APIKey)
			}
			if got.Model != tt.want.Model {
				t.Fatalf("Model = %q, want %q", got.Model, tt.want.Model)
			}
		})
	}
}

func TestResolveSubagentSettings(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *Config
		openAI  OpenAISettings
		env     map[string]string
		present map[string]bool
		want    SubagentSettings
	}{
		{
			name:   "env only",
			openAI: OpenAISettings{BaseURL: "https://openai.example", APIKey: "openai-key", Model: "openai-model"},
			env: map[string]string{
				"LATE_SUBAGENT_BASE_URL": "https://env-sub.example",
				"LATE_SUBAGENT_API_KEY":  "env-sub-key",
				"LATE_SUBAGENT_MODEL":    "env-sub-model",
			},
			present: map[string]bool{
				"LATE_SUBAGENT_BASE_URL": true,
				"LATE_SUBAGENT_API_KEY":  true,
				"LATE_SUBAGENT_MODEL":    true,
			},
			want: SubagentSettings{BaseURL: "https://env-sub.example", APIKey: "env-sub-key", Model: "env-sub-model"},
		},
		{
			name: "config only",
			cfg: &Config{
				LateSubagentBaseURL: "https://config-sub.example",
				LateSubagentAPIKey:  "config-sub-key",
				LateSubagentModel:   "config-sub-model",
			},
			openAI: OpenAISettings{BaseURL: "https://openai.example", APIKey: "openai-key", Model: "openai-model"},
			want:   SubagentSettings{BaseURL: "https://config-sub.example", APIKey: "config-sub-key", Model: "config-sub-model"},
		},
		{
			name: "env wins over config",
			cfg: &Config{
				LateSubagentBaseURL: "https://config-sub.example",
				LateSubagentAPIKey:  "config-sub-key",
				LateSubagentModel:   "config-sub-model",
			},
			openAI: OpenAISettings{BaseURL: "https://openai.example", APIKey: "openai-key", Model: "openai-model"},
			env: map[string]string{
				"LATE_SUBAGENT_BASE_URL": "https://env-sub.example",
				"LATE_SUBAGENT_API_KEY":  "env-sub-key",
				"LATE_SUBAGENT_MODEL":    "env-sub-model",
			},
			present: map[string]bool{
				"LATE_SUBAGENT_BASE_URL": true,
				"LATE_SUBAGENT_API_KEY":  true,
				"LATE_SUBAGENT_MODEL":    true,
			},
			want: SubagentSettings{BaseURL: "https://env-sub.example", APIKey: "env-sub-key", Model: "env-sub-model"},
		},
		{
			name: "empty env falls back to config",
			cfg: &Config{
				LateSubagentBaseURL: "https://config-sub.example",
				LateSubagentAPIKey:  "config-sub-key",
				LateSubagentModel:   "config-sub-model",
			},
			openAI: OpenAISettings{BaseURL: "https://openai.example", APIKey: "openai-key", Model: "openai-model"},
			env: map[string]string{
				"LATE_SUBAGENT_BASE_URL": "",
				"LATE_SUBAGENT_API_KEY":  "",
				"LATE_SUBAGENT_MODEL":    "",
			},
			present: map[string]bool{
				"LATE_SUBAGENT_BASE_URL": true,
				"LATE_SUBAGENT_API_KEY":  true,
				"LATE_SUBAGENT_MODEL":    true,
			},
			want: SubagentSettings{BaseURL: "https://config-sub.example", APIKey: "config-sub-key", Model: "config-sub-model"},
		},
		{
			name: "openai fallback for base and api key",
			cfg: &Config{
				LateSubagentModel: "config-sub-model",
			},
			openAI: OpenAISettings{BaseURL: "https://openai.example", APIKey: "openai-key", Model: "openai-model"},
			want:   SubagentSettings{BaseURL: "https://openai.example", APIKey: "openai-key", Model: "config-sub-model"},
		},
		{
			name:   "openai fallback for model",
			openAI: OpenAISettings{BaseURL: "https://openai.example", APIKey: "openai-key", Model: "openai-model"},
			want:   SubagentSettings{BaseURL: "https://openai.example", APIKey: "openai-key", Model: "openai-model"},
		},
		{
			name: "legacy config support",
			cfg: &Config{
				SubagentBaseURL: "https://legacy-sub.example",
				SubagentAPIKey:  "legacy-sub-key",
				SubagentModel:   "legacy-sub-model",
			},
			openAI: OpenAISettings{BaseURL: "https://openai.example", APIKey: "openai-key", Model: "openai-model"},
			want:   SubagentSettings{BaseURL: "https://legacy-sub.example", APIKey: "legacy-sub-key", Model: "legacy-sub-model"},
		},
		{
			name: "new config overrides legacy",
			cfg: &Config{
				SubagentBaseURL:     "https://legacy-sub.example",
				LateSubagentBaseURL: "https://new-sub.example",
			},
			openAI: OpenAISettings{BaseURL: "https://openai.example", APIKey: "openai-key", Model: "openai-model"},
			want:   SubagentSettings{BaseURL: "https://new-sub.example", APIKey: "openai-key", Model: "openai-model"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveSubagentSettingsWithEnv(tt.cfg, tt.openAI, func(key string) (string, bool) {
				value, ok := tt.env[key]
				if tt.present != nil {
					ok = tt.present[key]
				}
				return value, ok
			})

			if got.BaseURL != tt.want.BaseURL {
				t.Fatalf("BaseURL = %q, want %q", got.BaseURL, tt.want.BaseURL)
			}
			if got.APIKey != tt.want.APIKey {
				t.Fatalf("APIKey = %q, want %q", got.APIKey, tt.want.APIKey)
			}
			if got.Model != tt.want.Model {
				t.Fatalf("Model = %q, want %q", got.Model, tt.want.Model)
			}
		})
	}
}

func TestResolveSaveSubagentHistories(t *testing.T) {
	enabled := true
	disabled := false
	tests := []struct {
		name            string
		cfg             *Config
		cliExplicit     bool
		cliValue        bool
		savedPreference *bool
		want            bool
	}{
		{
			name:            "explicit flag on wins over config off",
			cfg:             &Config{SaveSubagentHistories: false},
			cliExplicit:     true,
			cliValue:        true,
			savedPreference: &disabled,
			want:            true,
		},
		{
			name:            "explicit flag off wins over config on",
			cfg:             &Config{SaveSubagentHistories: true},
			cliExplicit:     true,
			cliValue:        false,
			savedPreference: &enabled,
			want:            false,
		},
		{
			name:            "saved preference wins over config",
			cfg:             &Config{SaveSubagentHistories: true},
			savedPreference: &disabled,
			want:            false,
		},
		{
			name:            "saved enabled preference wins over config",
			cfg:             &Config{SaveSubagentHistories: false},
			savedPreference: &enabled,
			want:            true,
		},
		{
			name:        "no flag uses config on",
			cfg:         &Config{SaveSubagentHistories: true},
			cliExplicit: false,
			cliValue:    false,
			want:        true,
		},
		{
			name:        "no flag uses config off",
			cfg:         &Config{SaveSubagentHistories: false},
			cliExplicit: false,
			cliValue:    false,
			want:        false,
		},
		{
			name:        "no flag and nil config defaults to off",
			cfg:         nil,
			cliExplicit: false,
			cliValue:    false,
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveSaveSubagentHistories(tt.cfg, tt.cliExplicit, tt.cliValue, tt.savedPreference); got != tt.want {
				t.Fatalf("ResolveSaveSubagentHistories() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfig_GetModelForAgent(t *testing.T) {
	cfg := &Config{
		Models: []ModelSetting{
			{URL: "http://localhost:8080", Key: "key-1", Model: "model-1"},
			{URL: "http://localhost:9090", Key: "key-2", Model: "model-2"},
		},
		AgentModels: map[string]string{
			"orchestrator": "model-1",
			"coder":        "model-2",
			"unknown":      "model-3",
		},
	}

	tests := []struct {
		agentType string
		wantModel string
		wantOk    bool
	}{
		{"orchestrator", "model-1", true},
		{"coder", "model-2", true},
		{"unknown", "", false},
		{"missing", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			got, ok := cfg.GetModelForAgent(tt.agentType)
			if ok != tt.wantOk {
				t.Errorf("GetModelForAgent(%q) ok = %v, want %v", tt.agentType, ok, tt.wantOk)
			}
			if ok && got.Model != tt.wantModel {
				t.Errorf("GetModelForAgent(%q) got model = %q, want %q", tt.agentType, got.Model, tt.wantModel)
			}
		})
	}
}

func TestConfig_ResolveShowTodoPane(t *testing.T) {
	closed := false
	open := true

	var absentEntry Config
	if err := json.Unmarshal([]byte(`{"theme":"late"}`), &absentEntry); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	tests := []struct {
		name string
		cfg  *Config
		want bool
	}{
		{"nil config defaults to open", nil, true},
		{"absent show-todo-pane entry defaults to open", &absentEntry, true},
		{"zero-value config defaults to open", &Config{}, true},
		{"explicit false starts with the pane closed", &Config{ShowTodoPane: &closed}, false},
		{"explicit true keeps the pane open", &Config{ShowTodoPane: &open}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.ResolveShowTodoPane(); got != tt.want {
				t.Errorf("ResolveShowTodoPane() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfig_GetModelForAgentUsesStableID(t *testing.T) {
	cfg := &Config{
		Models: []ModelSetting{
			{ID: "provider-a", URL: "https://a.example/v1", Model: "shared-model"},
			{ID: "provider-b", URL: "https://b.example/v1", Model: "shared-model"},
		},
		AgentModels: map[string]string{"orchestrator": "provider-b"},
	}

	got, ok := cfg.GetModelForAgent("orchestrator")
	if !ok {
		t.Fatal("expected model setting to resolve")
	}
	if got.URL != "https://b.example/v1" {
		t.Fatalf("resolved URL = %q, want provider B", got.URL)
	}
}

func TestSaveConfigAtomicallyReplacesFile(t *testing.T) {
	configRoot := t.TempDir()
	setUserConfigEnv(t, configRoot)

	if _, err := LoadConfig(); err != nil {
		t.Fatal(err)
	}
	configPath := lateConfigPath(t)
	before, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &Config{OpenAIModel: "replacement-model"}
	if err := SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	after, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && os.SameFile(before, after) {
		t.Fatal("config file was modified in place instead of atomically replaced")
	}
	if runtime.GOOS != "windows" && after.Mode().Perm() != configFilePerm {
		t.Fatalf("config file permissions = %o, want %o", after.Mode().Perm(), configFilePerm)
	}

	loaded, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.OpenAIModel != "replacement-model" {
		t.Fatalf("saved model = %q, want replacement-model", loaded.OpenAIModel)
	}

	entries, err := os.ReadDir(filepath.Dir(configPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".tmp" {
			t.Fatalf("temporary config was not cleaned up: %s", entry.Name())
		}
	}
}

func TestResolvePermissionMode(t *testing.T) {
	tests := []struct {
		name             string
		cfg              *Config
		askFlag          bool
		unsupervisedFlag bool
		wantMode         string
		// wantWarning nil: warning must be empty; non-nil: warning must
		// contain each substring.
		wantWarning     []string
		wantErr         bool
		wantErrContains string
	}{
		{
			name:     "no flags, empty config, nil cfg",
			cfg:      nil,
			wantMode: PermissionModeAskForUserApproval,
		},
		{
			name:     "no flags, empty config value",
			cfg:      &Config{PermissionMode: ""},
			wantMode: PermissionModeAskForUserApproval,
		},
		{
			name:     "ask flag alone",
			askFlag:  true,
			wantMode: PermissionModeAskForUserApproval,
		},
		{
			name:             "unsupervised flag alone",
			unsupervisedFlag: true,
			wantMode:         PermissionModeUnsupervised,
		},
		{
			name:     "ask flag overrides unsupervised config",
			cfg:      &Config{PermissionMode: PermissionModeUnsupervised},
			askFlag:  true,
			wantMode: PermissionModeAskForUserApproval,
		},
		{
			name:             "unsupervised flag overrides ask config",
			cfg:              &Config{PermissionMode: PermissionModeAskForUserApproval},
			unsupervisedFlag: true,
			wantMode:         PermissionModeUnsupervised,
		},
		{
			name:     "config value: ask-for-user-approval respected",
			cfg:      &Config{PermissionMode: PermissionModeAskForUserApproval},
			wantMode: PermissionModeAskForUserApproval,
		},
		{
			name:     "config value: i-promise-i-have-backups-and-will-not-file-issues respected",
			cfg:      &Config{PermissionMode: PermissionModeUnsupervised},
			wantMode: PermissionModeUnsupervised,
		},
		{
			name:        "invalid config value falls back to default with warning",
			cfg:         &Config{PermissionMode: "yolo"},
			wantMode:    PermissionModeAskForUserApproval,
			wantWarning: []string{"invalid", "yolo"},
		},
		{
			name:             "two flags set is an error",
			askFlag:          true,
			unsupervisedFlag: true,
			wantErr:          true,
			wantErrContains:  "mutually exclusive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, warning, err := ResolvePermissionMode(tt.cfg, tt.askFlag, tt.unsupervisedFlag)

			if tt.wantErr {
				if err == nil {
					t.Fatal("ResolvePermissionMode() expected an error, got nil")
				}
				if tt.wantErrContains != "" && !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Fatalf("ResolvePermissionMode() error = %q, want it to contain %q", err.Error(), tt.wantErrContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolvePermissionMode() error = %v, want nil", err)
			}
			if mode != tt.wantMode {
				t.Fatalf("ResolvePermissionMode() mode = %q, want %q", mode, tt.wantMode)
			}
			if len(tt.wantWarning) == 0 {
				if warning != "" {
					t.Fatalf("ResolvePermissionMode() warning = %q, want empty", warning)
				}
				return
			}
			if warning == "" {
				t.Fatal("ResolvePermissionMode() warning is empty, want a warning")
			}
			for _, substring := range tt.wantWarning {
				if !strings.Contains(warning, substring) {
					t.Fatalf("ResolvePermissionMode() warning = %q, want it to contain %q", warning, substring)
				}
			}
		})
	}
}

func TestConfig_PermissionModeJSONRoundTrip(t *testing.T) {
	original := Config{PermissionMode: PermissionModeUnsupervised}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded Config
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if decoded.PermissionMode != PermissionModeUnsupervised {
		t.Fatalf("PermissionMode after round trip = %q, want %q", decoded.PermissionMode, PermissionModeUnsupervised)
	}

	emptyData, err := json.Marshal(Config{})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(emptyData, &raw); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if _, ok := raw["permission-mode"]; ok {
		t.Fatalf("empty config should not marshal a permission-mode key, got %s", emptyData)
	}
}
