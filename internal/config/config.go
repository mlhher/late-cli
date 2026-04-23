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

const (
	configDirPerm  os.FileMode = 0o700
	configFilePerm os.FileMode = 0o600
)

// Config represents the application configuration.
type Config struct {
	EnabledTools     map[string]bool `json:"enabled_tools"`
	OpenAIBaseURL    string          `json:"openai_base_url,omitempty"`
	OpenAIAPIKey     string          `json:"openai_api_key,omitempty"`
	OpenAIModel      string          `json:"openai_model,omitempty"`
	SubagentBaseURL  string          `json:"subagent_base_url,omitempty"`
	SubagentAPIKey   string          `json:"subagent_api_key,omitempty"`
	SubagentModel    string          `json:"subagent_model,omitempty"`
}

func defaultConfig() Config {
	return Config{
		EnabledTools: map[string]bool{
			"read_file":      true,
			"write_file":     true,
			"target_edit":    true,
			"spawn_subagent": true,
			"bash":           true,
		},
	}
}

func LoadConfig() (*Config, error) {
	lateConfigDir, err := pathutil.LateConfigDir()
	if err != nil {
		return nil, err
	}
	configPath := filepath.Join(lateConfigDir, "config.json")

	content, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Pre-populate with a default config that enables everything
			fallback := defaultConfig()
			defaultData, _ := json.MarshalIndent(fallback, "", "  ")

			// Ensure directory exists
			if err := os.MkdirAll(lateConfigDir, configDirPerm); err != nil {
				return &fallback, fmt.Errorf("failed to create config directory: %w", err)
			}

			if err := os.WriteFile(configPath, defaultData, configFilePerm); err != nil {
				return &fallback, fmt.Errorf("failed to write default config: %w", err)
			}

			if err := ensureSecureConfigPermissions(lateConfigDir, configPath); err != nil {
				return &fallback, err
			}

			return &fallback, nil
		}

		fallback := defaultConfig()
		return &fallback, err
	}

	permErr := ensureSecureConfigPermissions(lateConfigDir, configPath)

	var cfg Config
	if err := json.Unmarshal(content, &cfg); err != nil {
		fallback := defaultConfig()
		return &fallback, err
	}

	if cfg.EnabledTools == nil {
		cfg.EnabledTools = defaultConfig().EnabledTools
	}

	if permErr != nil {
		return &cfg, permErr
	}

	return &cfg, nil
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
	resolved := SubagentSettings{
		BaseURL: openAI.BaseURL,
		APIKey:  openAI.APIKey,
	}

	if cfg != nil {
		if cfg.SubagentBaseURL != "" {
			resolved.BaseURL = cfg.SubagentBaseURL
		}
		if cfg.SubagentAPIKey != "" {
			resolved.APIKey = cfg.SubagentAPIKey
		}
		resolved.Model = cfg.SubagentModel
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
