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

type ModelSetting struct {
	ID               string            `json:"id,omitempty"`
	URL              string            `json:"url"`
	Key              string            `json:"key"`
	Model            string            `json:"model"`
	ReasoningMapping map[string]string `json:"reasoning_mapping,omitempty"`
}

type AgentModelSetting struct {
	Model  string `json:"model"`
	Effort string `json:"effort,omitempty"`
}

func (a *AgentModelSetting) UnmarshalJSON(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		a.Model = s
		a.Effort = ""
		return nil
	}
	type Alias AgentModelSetting
	var alias Alias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	*a = AgentModelSetting(alias)
	return nil
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

	// Legacy subagent fields for backward compatibility
	SubagentBaseURL string `json:"subagent_base_url,omitempty"`
	SubagentAPIKey  string `json:"subagent_api_key,omitempty"`
	SubagentModel   string `json:"subagent_model,omitempty"`

	SkillsDir string `json:"skills_dir,omitempty"`

	Models      []ModelSetting               `json:"models,omitempty"`
	AgentModels map[string]AgentModelSetting `json:"agent_models,omitempty"`
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
		Model:   openAI.Model,
	}

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
	agentSetting, exists := cfg.AgentModels[agentType]
	if !exists {
		return ModelSetting{}, false
	}
	modelRef := agentSetting.Model
	if modelRef == "" {
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

// ResolveAgentReasoningEffort returns the backend reasoning effort string mapped for the agent's configured effort level.
// If the agent definition lacks an effort key, or if the model config lacks reasoning_mapping or the effort mapping,
// it returns an empty string.
func (cfg *Config) ResolveAgentReasoningEffort(agentType string) string {
	if cfg == nil || cfg.AgentModels == nil || cfg.Models == nil {
		return ""
	}
	agentSetting, exists := cfg.AgentModels[agentType]
	if !exists || agentSetting.Effort == "" {
		return ""
	}
	modelDef, ok := cfg.GetModelForAgent(agentType)
	if !ok || modelDef.ReasoningMapping == nil {
		return ""
	}
	return modelDef.ReasoningMapping[agentSetting.Effort]
}

// ResolveAgent returns the ModelSetting and resolved reasoning effort string for a given agent type.
// If the agent is not configured or resolved, it returns (ModelSetting{}, "", false).
func (cfg *Config) ResolveAgent(agentType string) (setting ModelSetting, effort string, ok bool) {
	if cfg == nil {
		return ModelSetting{}, "", false
	}
	setting, ok = cfg.GetModelForAgent(agentType)
	if !ok {
		return ModelSetting{}, "", false
	}
	effort = cfg.ResolveAgentReasoningEffort(agentType)
	return setting, effort, true
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
