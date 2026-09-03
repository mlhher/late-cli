package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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

func TestConfig_GetModelForAgent(t *testing.T) {
	cfg := &Config{
		Models: []ModelSetting{
			{URL: "http://localhost:8080", Key: "key-1", Model: "model-1"},
			{URL: "http://localhost:9090", Key: "key-2", Model: "model-2"},
		},
		AgentModels: map[string]AgentModelSetting{
			"orchestrator": {Model: "model-1"},
			"coder":        {Model: "model-2"},
			"unknown":      {Model: "model-3"},
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
		AgentModels: map[string]AgentModelSetting{"orchestrator": {Model: "provider-b"}},
	}

	got, ok := cfg.GetModelForAgent("orchestrator")
	if !ok {
		t.Fatal("expected model setting to resolve")
	}
	if got.URL != "https://b.example/v1" {
		t.Fatalf("resolved URL = %q, want provider B", got.URL)
	}
}

func TestConfig_UnmarshalJSON_AgentModels(t *testing.T) {
	jsonBlob := `{
		"models": [
			{
				"url": "http://localhost:8080",
				"key": "",
				"model": "qwen3.5-35b-a3b",
				"reasoning_mapping": {
					"none": "none",
					"low": "low",
					"med": "medium",
					"high": "high",
					"xhigh": "xhigh"
				}
			},
			{
				"url": "https://api.deepseek.com/v1",
				"key": "sk-test",
				"model": "deepseek-v4-flash",
				"reasoning_mapping": {
					"none": "none",
					"low": "low",
					"med": "medium",
					"high": "high",
					"xhigh": "high"
				}
			}
		],
		"agent_models": {
			"coder": {
				"model": "qwen3.5-35b-a3b",
				"effort": "high"
			},
			"orchestrator": {
				"model": "deepseek-v4-flash",
				"effort": "low"
			},
			"researcher": {
				"model": "qwen3.5-35b-a3b",
				"effort": "med"
			},
			"legacy_agent": "deepseek-v4-flash"
		}
	}`

	var cfg Config
	if err := json.Unmarshal([]byte(jsonBlob), &cfg); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if cfg.AgentModels["coder"].Model != "qwen3.5-35b-a3b" || cfg.AgentModels["coder"].Effort != "high" {
		t.Errorf("unexpected coder agent config: %#v", cfg.AgentModels["coder"])
	}
	if cfg.AgentModels["orchestrator"].Model != "deepseek-v4-flash" || cfg.AgentModels["orchestrator"].Effort != "low" {
		t.Errorf("unexpected orchestrator agent config: %#v", cfg.AgentModels["orchestrator"])
	}
	if cfg.AgentModels["researcher"].Model != "qwen3.5-35b-a3b" || cfg.AgentModels["researcher"].Effort != "med" {
		t.Errorf("unexpected researcher agent config: %#v", cfg.AgentModels["researcher"])
	}
	if cfg.AgentModels["legacy_agent"].Model != "deepseek-v4-flash" || cfg.AgentModels["legacy_agent"].Effort != "" {
		t.Errorf("unexpected legacy_agent config: %#v", cfg.AgentModels["legacy_agent"])
	}

	// Test Resolution Flow
	if effort := cfg.ResolveAgentReasoningEffort("coder"); effort != "high" {
		t.Errorf("coder effort = %q, want %q", effort, "high")
	}
	if effort := cfg.ResolveAgentReasoningEffort("orchestrator"); effort != "low" {
		t.Errorf("orchestrator effort = %q, want %q", effort, "low")
	}
	if effort := cfg.ResolveAgentReasoningEffort("researcher"); effort != "medium" {
		t.Errorf("researcher effort = %q, want %q", effort, "medium")
	}
	if effort := cfg.ResolveAgentReasoningEffort("legacy_agent"); effort != "" {
		t.Errorf("legacy_agent effort = %q, want empty string", effort)
	}
}

func TestConfig_ResolveAgentReasoningEffort_Fallbacks(t *testing.T) {
	cfg := &Config{
		Models: []ModelSetting{
			{
				Model: "standard-model",
				URL:   "http://localhost:8080",
			},
			{
				Model: "reasoning-model",
				URL:   "http://localhost:8080",
				ReasoningMapping: map[string]string{
					"low":  "low",
					"high": "high",
				},
			},
		},
		AgentModels: map[string]AgentModelSetting{
			"agent-no-effort": {
				Model: "reasoning-model",
			},
			"agent-standard-model": {
				Model:  "standard-model",
				Effort: "high",
			},
			"agent-unmapped-effort": {
				Model:  "reasoning-model",
				Effort: "ultra",
			},
			"agent-missing-model": {
				Model:  "nonexistent-model",
				Effort: "high",
			},
		},
	}

	tests := []struct {
		agent      string
		wantEffort string
	}{
		{"agent-no-effort", ""},
		{"agent-standard-model", ""},
		{"agent-unmapped-effort", ""},
		{"agent-missing-model", ""},
		{"nonexistent-agent", ""},
	}

	for _, tt := range tests {
		t.Run(tt.agent, func(t *testing.T) {
			if got := cfg.ResolveAgentReasoningEffort(tt.agent); got != tt.wantEffort {
				t.Errorf("ResolveAgentReasoningEffort(%q) = %q, want %q", tt.agent, got, tt.wantEffort)
			}
		})
	}

	// Test nil config
	var nilCfg *Config
	if got := nilCfg.ResolveAgentReasoningEffort("coder"); got != "" {
		t.Errorf("nil config effort = %q, want empty", got)
	}
}

func TestConfig_ResolveAgent(t *testing.T) {
	cfg := &Config{
		Models: []ModelSetting{
			{
				ID:    "deepseek",
				Model: "deepseek-v4-flash",
				URL:   "https://api.deepseek.com/v1",
				ReasoningMapping: map[string]string{
					"low": "low",
				},
			},
		},
		AgentModels: map[string]AgentModelSetting{
			"orchestrator": {
				Model:  "deepseek",
				Effort: "low",
			},
		},
	}

	setting, effort, ok := cfg.ResolveAgent("orchestrator")
	if !ok {
		t.Fatal("expected ResolveAgent to return ok = true")
	}
	if setting.Model != "deepseek-v4-flash" {
		t.Errorf("setting.Model = %q, want deepseek-v4-flash", setting.Model)
	}
	if effort != "low" {
		t.Errorf("effort = %q, want low", effort)
	}

	_, _, ok = cfg.ResolveAgent("unknown")
	if ok {
		t.Error("expected ResolveAgent for unknown to return ok = false")
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
