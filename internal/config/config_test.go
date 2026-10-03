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

func TestResolveCompactionThreshold(t *testing.T) {
	cases := []struct {
		name        string
		cfg         *Config
		want        int
		wantWarning []string
	}{
		{
			name: "nil config uses default",
			cfg:  nil,
			want: DefaultCompactionThresholdPercent,
		},
		{
			name: "unset uses default",
			cfg:  &Config{},
			want: DefaultCompactionThresholdPercent,
		},
		{
			name: "valid value honored",
			cfg:  &Config{CompactionThresholdPercent: 65},
			want: 65,
		},
		{
			name: "one is valid",
			cfg:  &Config{CompactionThresholdPercent: 1},
			want: 1,
		},
		{
			name: "hundred is valid",
			cfg:  &Config{CompactionThresholdPercent: 100},
			want: 100,
		},
		{
			name:        "negative value invalid",
			cfg:         &Config{CompactionThresholdPercent: -5},
			want:        DefaultCompactionThresholdPercent,
			wantWarning: []string{"invalid", "-5"},
		},
		{
			name:        "over hundred invalid",
			cfg:         &Config{CompactionThresholdPercent: 250},
			want:        DefaultCompactionThresholdPercent,
			wantWarning: []string{"invalid", "250"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, warning := ResolveCompactionThreshold(tc.cfg)
			if got != tc.want {
				t.Fatalf("ResolveCompactionThreshold() = %d, want %d", got, tc.want)
			}
			if len(tc.wantWarning) == 0 {
				if warning != "" {
					t.Fatalf("warning = %q, want empty", warning)
				}
				return
			}
			if warning == "" {
				t.Fatal("warning is empty, want a warning")
			}
			for _, substring := range tc.wantWarning {
				if !strings.Contains(warning, substring) {
					t.Fatalf("warning = %q, want it to contain %q", warning, substring)
				}
			}
		})
	}
}

// TestResolveCompactionMode mirrors TestResolveCompactionThreshold: the
// staged rollout modes validate to off|shadow|enabled, empty means the
// default (shadow), and anything else warns and falls back to the default.
func TestResolveCompactionMode(t *testing.T) {
	cases := []struct {
		name        string
		cfg         *Config
		want        string
		wantWarning []string
	}{
		{
			name: "nil config uses default",
			cfg:  nil,
			want: DefaultCompactionMode,
		},
		{
			name: "unset uses default",
			cfg:  &Config{},
			want: DefaultCompactionMode,
		},
		{
			name: "off honored",
			cfg:  &Config{CompactionMode: CompactionModeOff},
			want: CompactionModeOff,
		},
		{
			name: "shadow honored",
			cfg:  &Config{CompactionMode: CompactionModeShadow},
			want: CompactionModeShadow,
		},
		{
			name: "enabled honored",
			cfg:  &Config{CompactionMode: CompactionModeEnabled},
			want: CompactionModeEnabled,
		},
		{
			name:        "invalid value warns and falls back",
			cfg:         &Config{CompactionMode: "aggressive"},
			want:        DefaultCompactionMode,
			wantWarning: []string{"invalid", "aggressive", DefaultCompactionMode},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, warning := ResolveCompactionMode(tc.cfg)
			if got != tc.want {
				t.Fatalf("ResolveCompactionMode() = %q, want %q", got, tc.want)
			}
			if len(tc.wantWarning) == 0 {
				if warning != "" {
					t.Fatalf("warning = %q, want empty", warning)
				}
				return
			}
			if warning == "" {
				t.Fatal("warning is empty, want a warning")
			}
			for _, substring := range tc.wantWarning {
				if !strings.Contains(warning, substring) {
					t.Fatalf("warning = %q, want it to contain %q", warning, substring)
				}
			}
		})
	}

	for _, valid := range []string{CompactionModeOff, CompactionModeShadow, CompactionModeEnabled} {
		if !IsValidCompactionMode(valid) {
			t.Errorf("IsValidCompactionMode(%q) = false, want true", valid)
		}
	}
	for _, invalid := range []string{"", "Aggressive", "shadow ", "elided"} {
		if IsValidCompactionMode(invalid) {
			t.Errorf("IsValidCompactionMode(%q) = true, want false", invalid)
		}
	}
}

// TestResolveCompactionMaxElidePercent mirrors TestResolveCompactionThreshold
// for the elide-fraction tripwire knob: 0 (unset) means the reference default,
// 1-100 are honored, anything else warns and falls back.
func TestResolveCompactionMaxElidePercent(t *testing.T) {
	cases := []struct {
		name        string
		cfg         *Config
		want        int
		wantWarning []string
	}{
		{
			name: "nil config uses default",
			cfg:  nil,
			want: DefaultCompactionMaxElidePercent,
		},
		{
			name: "unset uses default",
			cfg:  &Config{},
			want: DefaultCompactionMaxElidePercent,
		},
		{
			name: "valid value honored",
			cfg:  &Config{CompactionMaxElidePercent: 50},
			want: 50,
		},
		{
			name: "one is valid",
			cfg:  &Config{CompactionMaxElidePercent: 1},
			want: 1,
		},
		{
			name: "hundred is valid",
			cfg:  &Config{CompactionMaxElidePercent: 100},
			want: 100,
		},
		{
			name:        "negative value invalid",
			cfg:         &Config{CompactionMaxElidePercent: -10},
			want:        DefaultCompactionMaxElidePercent,
			wantWarning: []string{"invalid", "-10"},
		},
		{
			name:        "over hundred invalid",
			cfg:         &Config{CompactionMaxElidePercent: 101},
			want:        DefaultCompactionMaxElidePercent,
			wantWarning: []string{"invalid", "101"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, warning := ResolveCompactionMaxElidePercent(tc.cfg)
			if got != tc.want {
				t.Fatalf("ResolveCompactionMaxElidePercent() = %d, want %d", got, tc.want)
			}
			assertResolverWarning(t, warning, tc.wantWarning)
		})
	}
}

// TestResolveCompactionProtectedFloor mirrors TestResolveCompactionThreshold
// for the protected-kind floor knob: 0 (unset) means the reference default,
// 1-100 are honored, anything else warns and falls back.
func TestResolveCompactionProtectedFloor(t *testing.T) {
	cases := []struct {
		name        string
		cfg         *Config
		want        int
		wantWarning []string
	}{
		{
			name: "nil config uses default",
			cfg:  nil,
			want: DefaultCompactionProtectedFloorPercent,
		},
		{
			name: "unset uses default",
			cfg:  &Config{},
			want: DefaultCompactionProtectedFloorPercent,
		},
		{
			name: "valid value honored",
			cfg:  &Config{CompactionProtectedFloor: 20},
			want: 20,
		},
		{
			name: "one is valid",
			cfg:  &Config{CompactionProtectedFloor: 1},
			want: 1,
		},
		{
			name: "hundred is valid",
			cfg:  &Config{CompactionProtectedFloor: 100},
			want: 100,
		},
		{
			name:        "negative value invalid",
			cfg:         &Config{CompactionProtectedFloor: -3},
			want:        DefaultCompactionProtectedFloorPercent,
			wantWarning: []string{"invalid", "-3"},
		},
		{
			name:        "over hundred invalid",
			cfg:         &Config{CompactionProtectedFloor: 250},
			want:        DefaultCompactionProtectedFloorPercent,
			wantWarning: []string{"invalid", "250"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, warning := ResolveCompactionProtectedFloor(tc.cfg)
			if got != tc.want {
				t.Fatalf("ResolveCompactionProtectedFloor() = %d, want %d", got, tc.want)
			}
			assertResolverWarning(t, warning, tc.wantWarning)
		})
	}
}

// assertResolverWarning checks a resolver's warning against the expected
// substrings (empty means the warning must be empty too).
func assertResolverWarning(t *testing.T, warning string, wantSubstrings []string) {
	t.Helper()
	if len(wantSubstrings) == 0 {
		if warning != "" {
			t.Fatalf("warning = %q, want empty", warning)
		}
		return
	}
	if warning == "" {
		t.Fatal("warning is empty, want a warning")
	}
	for _, substring := range wantSubstrings {
		if !strings.Contains(warning, substring) {
			t.Fatalf("warning = %q, want it to contain %q", warning, substring)
		}
	}
}

// TestLoadConfig_CompactionMode covers the config-file path: valid modes
// parse through, invalid ones survive loading so ResolveCompactionMode can
// warn and fall back to the default.
func TestLoadConfig_CompactionMode(t *testing.T) {
	t.Run("valid mode parses", func(t *testing.T) {
		configRoot := t.TempDir()
		setUserConfigEnv(t, configRoot)
		configPath := lateConfigPath(t)
		if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, []byte(`{"enabled_tools": {"bash": true}, "compaction-mode": "enabled"}`), 0o644); err != nil {
			t.Fatal(err)
		}

		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig() error = %v", err)
		}
		if cfg.CompactionMode != CompactionModeEnabled {
			t.Fatalf("CompactionMode = %q, want %q", cfg.CompactionMode, CompactionModeEnabled)
		}
	})

	t.Run("invalid mode warns via resolver", func(t *testing.T) {
		configRoot := t.TempDir()
		setUserConfigEnv(t, configRoot)
		configPath := lateConfigPath(t)
		if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, []byte(`{"enabled_tools": {"bash": true}, "compaction-mode": "yolo"}`), 0o644); err != nil {
			t.Fatal(err)
		}

		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig() error = %v", err)
		}
		mode, warning := ResolveCompactionMode(cfg)
		if mode != DefaultCompactionMode {
			t.Fatalf("resolved mode = %q, want %q", mode, DefaultCompactionMode)
		}
		if !strings.Contains(warning, "yolo") || !strings.Contains(warning, DefaultCompactionMode) {
			t.Fatalf("warning = %q, want it to name the invalid value and the fallback", warning)
		}
	})
}

// TestConfig_CompactionModeJSONRoundTrip: the field marshals under its
// config key and stays omitted when unset.
func TestConfig_CompactionModeJSONRoundTrip(t *testing.T) {
	original := Config{CompactionMode: CompactionModeEnabled}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded Config
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if decoded.CompactionMode != CompactionModeEnabled {
		t.Fatalf("CompactionMode after round trip = %q, want %q", decoded.CompactionMode, CompactionModeEnabled)
	}

	emptyData, err := json.Marshal(Config{})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(emptyData, &raw); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if _, ok := raw["compaction-mode"]; ok {
		t.Fatalf("empty config should not marshal a compaction-mode key, got %s", emptyData)
	}
}

func TestLoadConfig_ParsesCompactionThresholdPercent(t *testing.T) {
	configRoot := t.TempDir()
	setUserConfigEnv(t, configRoot)
	configPath := lateConfigPath(t)

	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatal(err)
	}
	content := `{
		"enabled_tools": {"bash": true},
		"compaction-threshold-percent": 65
	}`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.CompactionThresholdPercent != 65 {
		t.Fatalf("CompactionThresholdPercent = %d, want 65", cfg.CompactionThresholdPercent)
	}
}

func TestConfig_CompactionThresholdPercentJSONRoundTrip(t *testing.T) {
	original := Config{CompactionThresholdPercent: 65}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded Config
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if decoded.CompactionThresholdPercent != 65 {
		t.Fatalf("CompactionThresholdPercent after round trip = %d, want 65", decoded.CompactionThresholdPercent)
	}

	// Zero values must not emit keys (omitempty), keeping config.json clean
	// for users who never touched the new settings.
	emptyData, err := json.Marshal(Config{})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(emptyData, &raw); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	for _, key := range []string{"compaction-threshold-percent"} {
		if _, ok := raw[key]; ok {
			t.Fatalf("empty config should not marshal a %s key, got %s", key, emptyData)
		}
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

// TestResolveAutocompact mirrors TestResolveCompactionThreshold: the switch
// is a plain boolean (absent = disabled) and the percentage validates 1-100,
// with 0 (unset) meaning the default and anything else warning and falling
// back to the default.
func TestResolveAutocompact(t *testing.T) {
	cases := []struct {
		name             string
		cfg              *Config
		wantEnabled      bool
		wantPercent      int
		wantWarningParts []string
	}{
		{
			name:        "nil config uses defaults",
			cfg:         nil,
			wantEnabled: false,
			wantPercent: DefaultJevAutocompactPercent,
		},
		{
			name:        "unset uses defaults",
			cfg:         &Config{},
			wantEnabled: false,
			wantPercent: DefaultJevAutocompactPercent,
		},
		{
			name:        "enabled with default percent",
			cfg:         &Config{JevAutocompact: true},
			wantEnabled: true,
			wantPercent: DefaultJevAutocompactPercent,
		},
		{
			name:        "valid percent honored",
			cfg:         &Config{JevAutocompact: true, JevAutocompactPercent: 90},
			wantEnabled: true,
			wantPercent: 90,
		},
		{
			name:        "one is valid",
			cfg:         &Config{JevAutocompactPercent: 1},
			wantEnabled: false,
			wantPercent: 1,
		},
		{
			name:        "hundred is valid",
			cfg:         &Config{JevAutocompactPercent: 100},
			wantEnabled: false,
			wantPercent: 100,
		},
		{
			name:             "negative percent invalid",
			cfg:              &Config{JevAutocompact: true, JevAutocompactPercent: -5},
			wantEnabled:      true,
			wantPercent:      DefaultJevAutocompactPercent,
			wantWarningParts: []string{"invalid", "-5"},
		},
		{
			name:             "over hundred percent invalid",
			cfg:              &Config{JevAutocompactPercent: 250},
			wantEnabled:      false,
			wantPercent:      DefaultJevAutocompactPercent,
			wantWarningParts: []string{"invalid", "250"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotEnabled, gotPercent, warning := ResolveAutocompact(tc.cfg)
			if gotEnabled != tc.wantEnabled {
				t.Fatalf("ResolveAutocompact() enabled = %v, want %v", gotEnabled, tc.wantEnabled)
			}
			if gotPercent != tc.wantPercent {
				t.Fatalf("ResolveAutocompact() percent = %d, want %d", gotPercent, tc.wantPercent)
			}
			if len(tc.wantWarningParts) == 0 {
				if warning != "" {
					t.Fatalf("warning = %q, want empty", warning)
				}
				return
			}
			if warning == "" {
				t.Fatal("warning is empty, want a warning")
			}
			for _, substring := range tc.wantWarningParts {
				if !strings.Contains(warning, substring) {
					t.Fatalf("warning = %q, want it to contain %q", warning, substring)
				}
			}
		})
	}
}

func TestConfig_AutocompactJSONRoundTrip(t *testing.T) {
	original := Config{JevAutocompact: true, JevAutocompactPercent: 90}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded Config
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !decoded.JevAutocompact {
		t.Fatal("JevAutocompact after round trip = false, want true")
	}
	if decoded.JevAutocompactPercent != 90 {
		t.Fatalf("JevAutocompactPercent after round trip = %d, want 90", decoded.JevAutocompactPercent)
	}

	// Zero values must not emit keys (omitempty), keeping config.json clean
	// for users who never touched the new settings.
	emptyData, err := json.Marshal(Config{})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(emptyData, &raw); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	for _, key := range []string{"jev-autocompact", "jev-autocompact-percent"} {
		if _, ok := raw[key]; ok {
			t.Fatalf("empty config should not marshal a %s key, got %s", key, emptyData)
		}
	}
}

// TestModelSetting_AutocompactPercentOverride pins the per-model override
// accessor: 1-100 is the override, 0/unset (and anything out of range) means
// "no override — use the global".
func TestModelSetting_AutocompactPercentOverride(t *testing.T) {
	cases := []struct {
		name      string
		setting   ModelSetting
		wantValue int
		wantOK    bool
	}{
		{name: "unset is not an override", setting: ModelSetting{}, wantValue: 0, wantOK: false},
		{name: "one is the smallest override", setting: ModelSetting{JevAutocompactPercent: 1}, wantValue: 1, wantOK: true},
		{name: "fifty-five honored", setting: ModelSetting{JevAutocompactPercent: 55}, wantValue: 55, wantOK: true},
		{name: "hundred is the largest override", setting: ModelSetting{JevAutocompactPercent: 100}, wantValue: 100, wantOK: true},
		{name: "negative is ignored", setting: ModelSetting{JevAutocompactPercent: -5}, wantValue: 0, wantOK: false},
		{name: "over hundred is ignored", setting: ModelSetting{JevAutocompactPercent: 250}, wantValue: 0, wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.setting.AutocompactPercentOverride()
			if ok != tc.wantOK || got != tc.wantValue {
				t.Fatalf("AutocompactPercentOverride() = (%d, %v), want (%d, %v)", got, ok, tc.wantValue, tc.wantOK)
			}
		})
	}
}

// TestConfig_AutocompactPercentForAgent pins the resolution order for the
// JEV auto-compaction trigger: the agent's model entry override (1-100) >
// the global jev-autocompact-percent > nothing else (the caller passes the
// already-normalized global). The lookup is Config.GetModelForAgent, so
// agent_models routing (stable id or legacy model name) decides which entry
// applies.
func TestConfig_AutocompactPercentForAgent(t *testing.T) {
	cfg := &Config{
		JevAutocompactPercent: 99, // the global threshold
		Models: []ModelSetting{
			{ID: "small-ctx", URL: "http://a:8080", Key: "k", Model: "model-a", JevAutocompactPercent: 55},
			{ID: "huge-ctx", URL: "http://b:8080", Key: "k", Model: "model-b", JevAutocompactPercent: 100},
			{ID: "no-override", URL: "http://c:8080", Key: "k", Model: "model-c"},
			{ID: "bad-override", URL: "http://d:8080", Key: "k", Model: "model-d", JevAutocompactPercent: 400},
		},
		AgentModels: map[string]string{
			"orchestrator": "no-override",
			"researcher":   "small-ctx",
			"coder":        "huge-ctx",
			"reviewer":     "bad-override",
			"legacy":       "model-a", // legacy name-based routing still resolves
		},
	}

	cases := []struct {
		agentType string
		want      int
	}{
		{"researcher", 55},   // valid per-model override wins
		{"coder", 100},       // boundary values are honored too
		{"orchestrator", 99}, // no override: the global applies
		{"reviewer", 99},     // out-of-range override is ignored: the global applies
		{"legacy", 55},       // name-based agent_models routing resolves the entry
		{"unrouted", 99},     // agent type with no agent_models entry: the global
	}
	for _, tc := range cases {
		t.Run(tc.agentType, func(t *testing.T) {
			if got := cfg.AutocompactPercentForAgent(tc.agentType, 99); got != tc.want {
				t.Fatalf("AutocompactPercentForAgent(%q, 99) = %d, want %d", tc.agentType, got, tc.want)
			}
		})
	}

	// A different global flows through whenever no override applies.
	if got := cfg.AutocompactPercentForAgent("researcher", 70); got != 55 {
		t.Fatalf("override must win over a non-default global: got %d, want 55", got)
	}
	if got := cfg.AutocompactPercentForAgent("orchestrator", 70); got != 70 {
		t.Fatalf("no override must pass the global through: got %d, want 70", got)
	}
}

// TestConfig_AutocompactPercentForAgentNilSafe pins the nil-receiver and
// degenerate-input guards: a nil config, a config without the models
// sections, and an empty agent type all fall back to the global.
func TestConfig_AutocompactPercentForAgentNilSafe(t *testing.T) {
	if got := (*Config)(nil).AutocompactPercentForAgent("orchestrator", 99); got != 99 {
		t.Fatalf("nil config = %d, want the global 99", got)
	}
	if got := (&Config{}).AutocompactPercentForAgent("orchestrator", 99); got != 99 {
		t.Fatalf("empty config = %d, want the global 99", got)
	}
	cfg := &Config{Models: []ModelSetting{{ID: "m", JevAutocompactPercent: 55}}}
	if got := cfg.AutocompactPercentForAgent("orchestrator", 99); got != 99 {
		t.Fatalf("no agent_models routing = %d, want the global 99", got)
	}
	if got := cfg.AutocompactPercentForAgent("", 99); got != 99 {
		t.Fatalf("empty agent type = %d, want the global 99", got)
	}
}

// TestConfig_AutocompactWarnings pins the startup warning path for
// out-of-range per-model jev-autocompact-percent values: one warning per bad
// entry, naming the model (id, else model name) and the value, and saying
// the global applies. Valid and unset values never warn.
func TestConfig_AutocompactWarnings(t *testing.T) {
	cfg := &Config{
		Models: []ModelSetting{
			{ID: "good", Model: "model-good", JevAutocompactPercent: 55},
			{ID: "too-low", Model: "model-low", JevAutocompactPercent: -3},
			{Model: "no-id-bad", JevAutocompactPercent: 101},
			{ID: "unset", Model: "model-unset"},
		},
	}
	warnings := cfg.AutocompactWarnings()
	if len(warnings) != 2 {
		t.Fatalf("AutocompactWarnings() = %#v, want exactly 2 warnings", warnings)
	}
	first := warnings[0]
	for _, substring := range []string{"too-low", "-3", "global"} {
		if !strings.Contains(first, substring) {
			t.Fatalf("warning %q does not mention %q", first, substring)
		}
	}
	if !strings.Contains(warnings[1], "no-id-bad") || !strings.Contains(warnings[1], "101") {
		t.Fatalf("second warning %q must name the model (no id set) and the bad value 101", warnings[1])
	}

	if got := (&Config{}).AutocompactWarnings(); got != nil {
		t.Fatalf("config without models = %#v, want nil", got)
	}
	if got := (*Config)(nil).AutocompactWarnings(); got != nil {
		t.Fatalf("nil config = %#v, want nil", got)
	}
}


