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

// --- Phase 1: Archive compaction config tests ---

func TestArchiveCompaction_DisabledByDefault(t *testing.T) {
	configRoot := t.TempDir()
	setUserConfigEnv(t, configRoot)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.IsArchiveCompactionEnabled() {
		t.Fatal("expected archive compaction to be disabled by default")
	}
	if cfg.ArchiveCompaction != nil {
		t.Fatal("expected ArchiveCompaction block to be nil when not configured")
	}
}

func TestArchiveCompaction_EnabledFlagOnly(t *testing.T) {
	configRoot := t.TempDir()
	setUserConfigEnv(t, configRoot)
	configPath := lateConfigPath(t)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"archive_compaction":{"enabled":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if !cfg.IsArchiveCompactionEnabled() {
		t.Fatal("expected archive compaction to be enabled")
	}
	// Defaults applied for zero-value optional fields.
	if !cfg.ArchiveCompactionDefaultsApplied() {
		t.Fatal("expected defaults applied when only enabled flag provided")
	}
	settings := cfg.ArchiveCompactionSettings()
	defaults := defaultArchiveCompactionConfig()
	if settings.CompactionThresholdMessages != defaults.CompactionThresholdMessages {
		t.Fatalf("CompactionThresholdMessages = %d, want %d", settings.CompactionThresholdMessages, defaults.CompactionThresholdMessages)
	}
	if settings.KeepRecentMessages != defaults.KeepRecentMessages {
		t.Fatalf("KeepRecentMessages = %d, want %d", settings.KeepRecentMessages, defaults.KeepRecentMessages)
	}
}

func TestArchiveCompaction_FullConfig(t *testing.T) {
	configRoot := t.TempDir()
	setUserConfigEnv(t, configRoot)
	configPath := lateConfigPath(t)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{
		"archive_compaction": {
			"enabled": true,
			"compaction_threshold_messages": 200,
			"keep_recent_messages": 30,
			"archive_chunk_size": 75,
			"archive_search_max_results": 5,
			"archive_search_case_sensitive": true,
			"archive_compaction_lock_stale_after_seconds": 120
		}
	}`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if !cfg.IsArchiveCompactionEnabled() {
		t.Fatal("expected archive compaction to be enabled")
	}
	settings := cfg.ArchiveCompactionSettings()
	if settings.CompactionThresholdMessages != 200 {
		t.Fatalf("CompactionThresholdMessages = %d, want 200", settings.CompactionThresholdMessages)
	}
	if settings.KeepRecentMessages != 30 {
		t.Fatalf("KeepRecentMessages = %d, want 30", settings.KeepRecentMessages)
	}
	if settings.ArchiveChunkSize != 75 {
		t.Fatalf("ArchiveChunkSize = %d, want 75", settings.ArchiveChunkSize)
	}
	if settings.ArchiveSearchMaxResults != 5 {
		t.Fatalf("ArchiveSearchMaxResults = %d, want 5", settings.ArchiveSearchMaxResults)
	}
	if !settings.ArchiveSearchCaseSensitive {
		t.Fatal("expected ArchiveSearchCaseSensitive=true")
	}
	if settings.LockStaleAfterSeconds != 120 {
		t.Fatalf("LockStaleAfterSeconds = %d, want 120", settings.LockStaleAfterSeconds)
	}
}

func TestArchiveCompaction_UnknownFieldsTolerated(t *testing.T) {
	configRoot := t.TempDir()
	setUserConfigEnv(t, configRoot)
	configPath := lateConfigPath(t)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// Config with unknown field inside archive_compaction block (and outside).
	content := `{"unknown_future_field":"x","archive_compaction":{"enabled":false,"future_option":99}}`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("expected unknown fields to be tolerated, got error: %v", err)
	}
	if cfg.IsArchiveCompactionEnabled() {
		t.Fatal("expected archive compaction disabled")
	}
}

func TestArchiveCompaction_ValidateNegativeFields(t *testing.T) {
	negativeFields := []struct {
		name   string
		config ArchiveCompactionConfig
	}{
		{"threshold < 0", ArchiveCompactionConfig{Enabled: true, CompactionThresholdMessages: -1}},
		{"keepRecent < 0", ArchiveCompactionConfig{Enabled: true, KeepRecentMessages: -1}},
		{"chunkSize < 0", ArchiveCompactionConfig{Enabled: true, ArchiveChunkSize: -1}},
		{"maxResults < 0", ArchiveCompactionConfig{Enabled: true, ArchiveSearchMaxResults: -1}},
		{"lockStale < 0", ArchiveCompactionConfig{Enabled: true, LockStaleAfterSeconds: -1}},
	}
	for _, tc := range negativeFields {
		cfg := &Config{ArchiveCompaction: &tc.config}
		if err := cfg.ValidateArchiveCompaction(); err == nil {
			t.Errorf("%s: expected validation error, got nil", tc.name)
		}
	}
}

func TestArchiveCompaction_ValidateKeepRecentGEThreshold(t *testing.T) {
	cfg := &Config{ArchiveCompaction: &ArchiveCompactionConfig{
		Enabled:                     true,
		CompactionThresholdMessages: 10,
		KeepRecentMessages:          10, // equal → invalid
	}}
	if err := cfg.ValidateArchiveCompaction(); err == nil {
		t.Fatal("expected error when keep_recent_messages >= compaction_threshold_messages")
	}
}

func TestArchiveCompaction_ValidateDisabledAlwaysOK(t *testing.T) {
	cfg := &Config{ArchiveCompaction: &ArchiveCompactionConfig{
		Enabled:                     false,
		CompactionThresholdMessages: -99, // negative but disabled → no error
	}}
	if err := cfg.ValidateArchiveCompaction(); err != nil {
		t.Fatalf("disabled config should always pass validation, got: %v", err)
	}
}

func TestArchiveCompaction_ValidateNilOK(t *testing.T) {
	cfg := &Config{}
	if err := cfg.ValidateArchiveCompaction(); err != nil {
		t.Fatalf("nil archive config should pass validation, got: %v", err)
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
