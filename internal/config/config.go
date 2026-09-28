package config

import (
	"encoding/json"
	"fmt"
	"late/internal/pathutil"
	"os"
	"path/filepath"
	"runtime"
)

const DefaultOpenAIBaseURL = "http://localhost:8080"

// Permission modes for supervising potentially dangerous commands.
// The effective mode is resolved by ResolvePermissionMode:
// explicitly set CLI flag > config.json permission-mode entry >
// PermissionModeAskForUserApproval.
const (
	PermissionModeAskForUserApproval = "ask-for-user-approval"
	PermissionModeUnsupervised       = "i-promise-i-have-backups-and-will-not-file-issues"
)

type EnvLookup func(string) (string, bool)

type OpenAISettings struct {
	BaseURL string
	APIKey  string
	Model   string
}

type SubagentSettings struct {
	BaseURL string
	APIKey  string
	Model   string
}

type ModelSetting struct {
	ID    string `json:"id,omitempty"`
	URL   string `json:"url"`
	Key   string `json:"key"`
	Model string `json:"model"`
}

// Reference returns the stable value stored in agent_models. Model is retained
// as a fallback for configurations created before model IDs were introduced.
func (m ModelSetting) Reference() string {
	if m.ID != "" {
		return m.ID
	}
	return m.Model
}

const (
	configDirPerm  os.FileMode = 0o700
	configFilePerm os.FileMode = 0o600
)

// Config represents the application configuration.
type Config struct {
	EnabledTools        map[string]bool `json:"enabled_tools"`
	OpenAIBaseURL       string          `json:"openai_base_url,omitempty"`
	OpenAIAPIKey        string          `json:"openai_api_key,omitempty"`
	OpenAIModel         string          `json:"openai_model,omitempty"`
	LateSubagentBaseURL string          `json:"late_subagent_base_url,omitempty"`
	LateSubagentAPIKey  string          `json:"late_subagent_api_key,omitempty"`
	LateSubagentModel   string          `json:"late_subagent_model,omitempty"`

	// SaveSubagentHistories opts in to persisting subagent conversation
	// histories under <sessions>/<session-id>/subagents/. Default false.
	// Enable via config file or the --save-subagent-histories CLI flag.
	// The value is a FlexBool, so config.json accepts the on/off synonyms
	// ("yes", "on", 1, ...) alongside true/false.
	SaveSubagentHistories FlexBool `json:"save_subagent_histories,omitempty"`

	// PermissionMode selects how potentially dangerous commands are
	// supervised. One of the PermissionMode* constants; empty means the
	// default (ask-for-user-approval). Set via config file; the CLI flags
	// of the same names override it.
	PermissionMode string `json:"permission-mode,omitempty"`

	// Legacy subagent fields for backward compatibility
	SubagentBaseURL string `json:"subagent_base_url,omitempty"`
	SubagentAPIKey  string `json:"subagent_api_key,omitempty"`
	SubagentModel   string `json:"subagent_model,omitempty"`

	SkillsDir string `json:"skills_dir,omitempty"`

	Theme       string            `json:"theme,omitempty"`
	Models      []ModelSetting    `json:"models,omitempty"`
	AgentModels map[string]string `json:"agent_models,omitempty"`
}

func defaultConfig() Config {
	return Config{
		EnabledTools: map[string]bool{
			"read_file":      true,
			"write_file":     true,
			"target_edit":    true,
			"spawn_subagent": true,
			"bash":           true,
			"search_content": true,
			"find_files":     true,
			"create_todos":   true,
			"list_todos":     true,
			"finish_todo":    true,
		},
	}
}

// LoadConfig loads and strictly parses config.json.
//
// Behavior contract:
//
//   - Missing file (fresh install): a default config is written and returned
//     with a nil error — late starts normally.
//   - Any content problem (JSON syntax error, unknown top-level entry,
//     wrong-typed value, invalid enum value, invalid boolean synonym) is
//     FATAL: a rendered *ConfigParseError naming the exact file, line, and
//     column is returned together with a nil config, and the caller (main)
//     must print it and exit instead of starting on fallback defaults
//     (strict-config rules R2/R3 — no fallback-to-defaults startup).
//   - The file existing but being unreadable, or the config directory being
//     uncreatable, is likewise fatal (nil config, non-nil error).
//   - A failed post-load permission hardening is NOT fatal: the config
//     content is fully valid, so it is returned together with the
//     permission error for the caller to surface as a warning.
func LoadConfig() (*Config, error) {
	lateConfigDir, err := pathutil.LateConfigDir()
	if err != nil {
		return nil, err
	}
	configPath := filepath.Join(lateConfigDir, "config.json")

	content, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Fresh install: pre-populate with a default config that
			// enables everything. A missing file is not a config error.
			fallback := defaultConfig()
			defaultData, _ := json.MarshalIndent(fallback, "", "  ")

			// Ensure directory exists
			if err := os.MkdirAll(lateConfigDir, configDirPerm); err != nil {
				return nil, fmt.Errorf("failed to create config directory: %w", err)
			}

			if err := os.WriteFile(configPath, defaultData, configFilePerm); err != nil {
				return nil, fmt.Errorf("failed to write default config: %w", err)
			}

			if err := ensureSecureConfigPermissions(lateConfigDir, configPath); err != nil {
				// The default config was written successfully; a failed
				// permission hardening must not abort the fresh install.
				return &fallback, err
			}

			return &fallback, nil
		}

		// The file exists but cannot be read (e.g. it is a directory).
		// Starting on fallback defaults would silently ignore every user
		// setting, so this is fatal under the strict-config rules.
		return nil, fmt.Errorf("failed to read %s: %w", configPath, err)
	}

	permErr := ensureSecureConfigPermissions(lateConfigDir, configPath)

	cfg, err := parseConfigContent(configPath, content)
	if err != nil {
		// Strict config: a broken config.json never yields a fallback
		// config. The caller must render the error and abort.
		return nil, err
	}

	if cfg.EnabledTools == nil {
		cfg.EnabledTools = defaultConfig().EnabledTools
	} else {
		// Merge missing defaults for backward compatibility
		// so existing config files get new tools automatically.
		defaults := defaultConfig().EnabledTools
		for k, v := range defaults {
			if _, exists := cfg.EnabledTools[k]; !exists {
				cfg.EnabledTools[k] = v
			}
		}
	}

	if permErr != nil {
		// Permission hardening failed, but the config content is fully
		// valid: return it so the caller can warn and continue.
		return cfg, permErr
	}

	return cfg, nil
}

func ResolveOpenAISettings(cfg *Config) OpenAISettings {
	return ResolveOpenAISettingsWithEnv(cfg, os.LookupEnv)
}

func ResolveOpenAISettingsWithEnv(cfg *Config, lookup EnvLookup) OpenAISettings {
	resolved := OpenAISettings{BaseURL: DefaultOpenAIBaseURL}

	if cfg != nil {
		if cfg.OpenAIBaseURL != "" {
			resolved.BaseURL = cfg.OpenAIBaseURL
		}
		resolved.APIKey = cfg.OpenAIAPIKey
		resolved.Model = cfg.OpenAIModel
	}

	if value, ok := nonEmptyEnv(lookup, "OPENAI_BASE_URL"); ok {
		resolved.BaseURL = value
	}
	if value, ok := nonEmptyEnv(lookup, "OPENAI_API_KEY"); ok {
		resolved.APIKey = value
	}
	if value, ok := nonEmptyEnv(lookup, "OPENAI_MODEL"); ok {
		resolved.Model = value
	}

	return resolved
}

func ResolveSubagentSettings(cfg *Config, openAI OpenAISettings) SubagentSettings {
	return ResolveSubagentSettingsWithEnv(cfg, openAI, os.LookupEnv)
}

func ResolveSubagentSettingsWithEnv(cfg *Config, openAI OpenAISettings, lookup EnvLookup) SubagentSettings {
	resolved := SubagentSettings(openAI)

	if cfg != nil {
		// Check legacy fields first
		if cfg.SubagentBaseURL != "" {
			resolved.BaseURL = cfg.SubagentBaseURL
		}
		if cfg.SubagentAPIKey != "" {
			resolved.APIKey = cfg.SubagentAPIKey
		}
		if cfg.SubagentModel != "" {
			resolved.Model = cfg.SubagentModel
		}

		// New fields override legacy fields
		if cfg.LateSubagentBaseURL != "" {
			resolved.BaseURL = cfg.LateSubagentBaseURL
		}
		if cfg.LateSubagentAPIKey != "" {
			resolved.APIKey = cfg.LateSubagentAPIKey
		}
		if cfg.LateSubagentModel != "" {
			resolved.Model = cfg.LateSubagentModel
		}
	}

	if value, ok := nonEmptyEnv(lookup, "LATE_SUBAGENT_BASE_URL"); ok {
		resolved.BaseURL = value
	}
	if value, ok := nonEmptyEnv(lookup, "LATE_SUBAGENT_API_KEY"); ok {
		resolved.APIKey = value
	}
	if value, ok := nonEmptyEnv(lookup, "LATE_SUBAGENT_MODEL"); ok {
		resolved.Model = value
	}

	return resolved
}

// ResolveSaveSubagentHistories determines whether subagent history
// persistence is enabled. Precedence: explicit CLI flag > saved session
// preference > config file. There is intentionally no environment-variable
// override.
func ResolveSaveSubagentHistories(cfg *Config, cliExplicit bool, cliValue bool, savedPreference *bool) bool {
	if cliExplicit {
		return cliValue
	}
	if savedPreference != nil {
		return *savedPreference
	}
	if cfg != nil {
		return cfg.SaveSubagentHistories.Bool()
	}
	return false
}

// ResolvePermissionMode returns the effective permission mode.
// Precedence: exactly one explicitly-set CLI flag > config.json
// permission-mode entry > PermissionModeAskForUserApproval. The flags
// are mutually exclusive: setting more than one is an error. An
// unrecognized config.json value yields a warning and falls back to
// the safe default.
func ResolvePermissionMode(cfg *Config, askFlag, unsupervisedFlag bool) (mode string, warning string, err error) {
	set := 0
	for _, v := range []bool{askFlag, unsupervisedFlag} {
		if v {
			set++
		}
	}
	if set > 1 {
		return "", "", fmt.Errorf("permission flags are mutually exclusive; pass at most one of -%s, -%s",
			PermissionModeAskForUserApproval, PermissionModeUnsupervised)
	}
	switch {
	case askFlag:
		return PermissionModeAskForUserApproval, "", nil
	case unsupervisedFlag:
		return PermissionModeUnsupervised, "", nil
	}
	if cfg != nil && cfg.PermissionMode != "" {
		switch cfg.PermissionMode {
		case PermissionModeAskForUserApproval, PermissionModeUnsupervised:
			return cfg.PermissionMode, "", nil
		default:
			return PermissionModeAskForUserApproval,
				fmt.Sprintf("ignoring invalid config.json permission-mode %q; using %q", cfg.PermissionMode, PermissionModeAskForUserApproval),
				nil
		}
	}
	return PermissionModeAskForUserApproval, "", nil
}

func nonEmptyEnv(lookup EnvLookup, key string) (string, bool) {
	if lookup == nil {
		return "", false
	}

	value, ok := lookup(key)
	if !ok || value == "" {
		return "", false
	}

	return value, true
}

func ensureSecureConfigPermissions(configDir, configPath string) error {
	if runtime.GOOS == "windows" {
		return nil
	}

	if err := tightenPermission(configDir, configDirPerm); err != nil {
		return fmt.Errorf("failed to set config directory permissions: %w", err)
	}

	if err := tightenPermission(configPath, configFilePerm); err != nil {
		return fmt.Errorf("failed to set config file permissions: %w", err)
	}

	return nil
}

func tightenPermission(path string, required os.FileMode) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	if info.Mode().Perm() == required {
		return nil
	}

	return os.Chmod(path, required)
}

// GetModelForAgent returns the ModelSetting for a given agent type.
// If not found, it returns false.
func (cfg *Config) GetModelForAgent(agentType string) (ModelSetting, bool) {
	if cfg == nil || cfg.AgentModels == nil || cfg.Models == nil {
		return ModelSetting{}, false
	}
	modelRef, exists := cfg.AgentModels[agentType]
	if !exists {
		return ModelSetting{}, false
	}
	// Prefer stable IDs so providers exposing the same model name remain
	// distinguishable.
	for _, m := range cfg.Models {
		if m.ID != "" && m.ID == modelRef {
			return m, true
		}
	}
	// Backward compatibility for existing name-based agent_models entries.
	for _, m := range cfg.Models {
		if m.Model == modelRef {
			return m, true
		}
	}
	return ModelSetting{}, false
}

// SaveConfig atomically writes the configuration back to config.json.
func SaveConfig(cfg *Config) error {
	lateConfigDir, err := pathutil.LateConfigDir()
	if err != nil {
		return err
	}
	if err := tightenConfigDirPermission(lateConfigDir); err != nil {
		return err
	}

	configPath := filepath.Join(lateConfigDir, "config.json")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp(lateConfigDir, ".config-*.json.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temporary config: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if err := tmpFile.Chmod(configFilePerm); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to secure temporary config: %w", err)
	}
	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to write temporary config: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to sync temporary config: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temporary config: %w", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		return fmt.Errorf("failed to replace config: %w", err)
	}
	return ensureSecureConfigPermissions(lateConfigDir, configPath)
}

func tightenConfigDirPermission(configDir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	if err := tightenPermission(configDir, configDirPerm); err != nil {
		return fmt.Errorf("failed to set config directory permissions: %w", err)
	}
	return nil
}
