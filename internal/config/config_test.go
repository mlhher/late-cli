package config

import (
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
		"subagent_base_url": "https://subagent.example/v1",
		"subagent_api_key": "sub-secret",
		"subagent_model": "qwen-sub"
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
	if cfg.SubagentBaseURL != "https://subagent.example/v1" {
		t.Fatalf("SubagentBaseURL = %q", cfg.SubagentBaseURL)
	}
	if cfg.SubagentAPIKey != "sub-secret" {
		t.Fatalf("SubagentAPIKey = %q", cfg.SubagentAPIKey)
	}
	if cfg.SubagentModel != "qwen-sub" {
		t.Fatalf("SubagentModel = %q", cfg.SubagentModel)
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
				SubagentBaseURL: "https://config-sub.example",
				SubagentAPIKey:  "config-sub-key",
				SubagentModel:   "config-sub-model",
			},
			openAI: OpenAISettings{BaseURL: "https://openai.example", APIKey: "openai-key", Model: "openai-model"},
			want:   SubagentSettings{BaseURL: "https://config-sub.example", APIKey: "config-sub-key", Model: "config-sub-model"},
		},
		{
			name: "env wins over config",
			cfg: &Config{
				SubagentBaseURL: "https://config-sub.example",
				SubagentAPIKey:  "config-sub-key",
				SubagentModel:   "config-sub-model",
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
				SubagentBaseURL: "https://config-sub.example",
				SubagentAPIKey:  "config-sub-key",
				SubagentModel:   "config-sub-model",
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
				SubagentModel: "config-sub-model",
			},
			openAI: OpenAISettings{BaseURL: "https://openai.example", APIKey: "openai-key", Model: "openai-model"},
			want:   SubagentSettings{BaseURL: "https://openai.example", APIKey: "openai-key", Model: "config-sub-model"},
		},
		{
			name:   "no model keeps behavior unchanged",
			openAI: OpenAISettings{BaseURL: "https://openai.example", APIKey: "openai-key", Model: "openai-model"},
			want:   SubagentSettings{BaseURL: "https://openai.example", APIKey: "openai-key", Model: ""},
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
