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

// ArchiveCompactionConfig holds optional session archive compaction settings.
type ArchiveCompactionConfig struct {
	Enabled                     bool `json:"enabled"`
	CompactionThresholdMessages int  `json:"compaction_threshold_messages,omitempty"`
	KeepRecentMessages          int  `json:"keep_recent_messages,omitempty"`
	ArchiveChunkSize            int  `json:"archive_chunk_size,omitempty"`
	ArchiveSearchMaxResults     int  `json:"archive_search_max_results,omitempty"`
	ArchiveSearchCaseSensitive  bool `json:"archive_search_case_sensitive,omitempty"`
	LockStaleAfterSeconds       int  `json:"archive_compaction_lock_stale_after_seconds,omitempty"`
}

// defaultArchiveCompactionConfig returns sensible defaults for archive compaction.
func defaultArchiveCompactionConfig() ArchiveCompactionConfig {
	return ArchiveCompactionConfig{
		Enabled:                     false,
		CompactionThresholdMessages: 100,
		KeepRecentMessages:          20,
		ArchiveChunkSize:            50,
		ArchiveSearchMaxResults:     10,
		ArchiveSearchCaseSensitive:  false,
		LockStaleAfterSeconds:       300,
	}
}

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

	// ArchiveCompaction holds optional archive compaction configuration.
	// When nil or Enabled=false, all archive behavior is disabled.
	ArchiveCompaction *ArchiveCompactionConfig `json:"archive_compaction,omitempty"`
}

// IsArchiveCompactionEnabled returns true iff archive compaction is explicitly enabled.
func (c *Config) IsArchiveCompactionEnabled() bool {
	if c == nil || c.ArchiveCompaction == nil {
		return false
	}
	return c.ArchiveCompaction.Enabled
}

// ArchiveCompactionSettings returns the effective archive compaction config with defaults
// applied for any zero-value optional fields. Only valid when IsArchiveCompactionEnabled
// returns true.
func (c *Config) ArchiveCompactionSettings() ArchiveCompactionConfig {
	defaults := defaultArchiveCompactionConfig()
	if c == nil || c.ArchiveCompaction == nil {
		return defaults
	}
	out := *c.ArchiveCompaction
	if out.CompactionThresholdMessages <= 0 {
		out.CompactionThresholdMessages = defaults.CompactionThresholdMessages
	}
	if out.KeepRecentMessages <= 0 {
		out.KeepRecentMessages = defaults.KeepRecentMessages
	}
	if out.ArchiveChunkSize <= 0 {
		out.ArchiveChunkSize = defaults.ArchiveChunkSize
	}
	if out.ArchiveSearchMaxResults <= 0 {
		out.ArchiveSearchMaxResults = defaults.ArchiveSearchMaxResults
	}
	if out.LockStaleAfterSeconds <= 0 {
		out.LockStaleAfterSeconds = defaults.LockStaleAfterSeconds
	}
	return out
}

// ArchiveCompactionDefaultsApplied returns whether defaults were applied (i.e. the config
// block was present but optional numeric fields were zero/missing).
func (c *Config) ArchiveCompactionDefaultsApplied() bool {
	if c == nil || c.ArchiveCompaction == nil {
		return false
	}
	s := c.ArchiveCompaction
	return s.CompactionThresholdMessages == 0 ||
		s.KeepRecentMessages == 0 ||
		s.ArchiveChunkSize == 0 ||
		s.ArchiveSearchMaxResults == 0 ||
		s.LockStaleAfterSeconds == 0
}

// ValidateArchiveCompaction returns an error if any archive compaction field is out of range.
// Numeric fields may be 0 (meaning "use default"), but must not be negative.
func (c *Config) ValidateArchiveCompaction() error {
	if c == nil || c.ArchiveCompaction == nil || !c.ArchiveCompaction.Enabled {
		return nil
	}
	s := c.ArchiveCompaction
	if s.CompactionThresholdMessages < 0 {
		return fmt.Errorf("archive_compaction: compaction_threshold_messages must be >= 0, got %d", s.CompactionThresholdMessages)
	}
	if s.KeepRecentMessages < 0 {
		return fmt.Errorf("archive_compaction: keep_recent_messages must be >= 0, got %d", s.KeepRecentMessages)
	}
	if s.ArchiveChunkSize < 0 {
		return fmt.Errorf("archive_compaction: archive_chunk_size must be >= 0, got %d", s.ArchiveChunkSize)
	}
	if s.ArchiveSearchMaxResults < 0 {
		return fmt.Errorf("archive_compaction: archive_search_max_results must be >= 0, got %d", s.ArchiveSearchMaxResults)
	}
	if s.LockStaleAfterSeconds < 0 {
		return fmt.Errorf("archive_compaction: archive_compaction_lock_stale_after_seconds must be >= 0, got %d", s.LockStaleAfterSeconds)
	}
	settings := c.ArchiveCompactionSettings()
	if settings.KeepRecentMessages >= settings.CompactionThresholdMessages {
		return fmt.Errorf("archive_compaction: keep_recent_messages (%d) must be less than compaction_threshold_messages (%d)",
			settings.KeepRecentMessages, settings.CompactionThresholdMessages)
	}
	return nil
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
