package config

import (
	"encoding/json"
	"fmt"
	"late/internal/pathutil"
	"os"
	"path/filepath"
	"runtime"
	"time"
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

// DefaultBashTimeout is the default wall-clock budget for a single bash tool
// call, applied when neither the --bash-timeout flag nor the config.json
// "bash-timeout" entry provides a value. "0" (or negative) means unlimited.
const DefaultBashTimeout = 10 * time.Minute

// DefaultSubagentIdleTimeout is the default "truly idle" notification
// threshold for the subagent idle watchdog, applied when neither the
// --subagent-idle-timeout flag nor the config.json "subagent-idle-timeout"
// entry provides a value. "0" means off.
const DefaultSubagentIdleTimeout = 15 * time.Minute

// DefaultSubagentMaxTurns is the default maximum number of turns per
// subagent, applied when neither the --subagent-max-turns flag nor the
// config.json "subagent-max-turns" entry provides a value. 0 means
// unlimited.
const DefaultSubagentMaxTurns = 500

// DefaultMaxConcurrentLLMRequests is the default process-wide cap on
// concurrent in-flight LLM requests, applied when neither the
// --max-concurrent-llm-requests flag nor the config.json
// "max-concurrent-llm-requests" entry provides a value. 0 means unlimited.
const DefaultMaxConcurrentLLMRequests = 6

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
	SaveSubagentHistories bool `json:"save_subagent_histories,omitempty"`

	// PermissionMode selects how potentially dangerous commands are
	// supervised. One of the PermissionMode* constants; empty means the
	// default (ask-for-user-approval). Set via config file; the CLI flags
	// of the same names override it.
	PermissionMode string `json:"permission-mode,omitempty"`

	// ----------------------------------------------------------------------
	// CLI-equivalent settings (flag > config > default)
	//
	// Every field in this section mirrors a command-line flag one-to-one:
	// the JSON key is the flag name in kebab-case, exactly as the user
	// passes it (`--option-name <VALUE>` becomes "option-name": <VALUE>).
	// Resolution follows the mandatory precedence: an explicitly passed CLI
	// flag (detected via flag.Visit in main, since config.json loads after
	// flag.Parse) wins over the config entry, which wins over the built-in
	// default. Each field's doc comment states its flag equivalent, its
	// default, and its unset/zero semantics; the matching Resolve* function
	// in this file implements the precedence and is the only supported way
	// to read the setting.
	// ----------------------------------------------------------------------

	// SystemPrompt replaces the built-in system prompt with this text.
	// Flag: --system-prompt. Empty (unset) keeps the built-in prompt.
	// Priority (identical to the flags): system-prompt-file >
	// system-prompt > LATE_SYSTEM_PROMPT env > built-in prompt.
	SystemPrompt string `json:"system-prompt,omitempty"`

	// SystemPromptFile replaces the built-in system prompt with the
	// contents of this file. Flag: --system-prompt-file. Empty (unset)
	// keeps the built-in prompt. An unreadable path is a hard error, the
	// same as for the flag.
	SystemPromptFile string `json:"system-prompt-file,omitempty"`

	// AppendSystemPrompt is appended to the final system prompt (after any
	// replacement above). Flag: --append-system-prompt. Empty (unset)
	// appends nothing.
	AppendSystemPrompt string `json:"append-system-prompt,omitempty"`

	// InjectCWD replaces ${{CWD}} in the system prompt with the working
	// directory. Flag: --inject-cwd. Default true. *bool tri-state: nil
	// (absent entry) = unset → default, so an explicit false is
	// distinguishable from unset (mirrors ShowTodoPane).
	InjectCWD *bool `json:"inject-cwd,omitempty"`

	// GemmaThinking prepends the Gemma <|think|> token to the system
	// prompt. Flag: --gemma-thinking. Default false; a plain bool suffices
	// because the default is false.
	GemmaThinking bool `json:"gemma-thinking,omitempty"`

	// UseTools offers tools to the main agent at all. Flag: --use-tools.
	// Default true. *bool tri-state: nil (absent entry) = unset → default.
	UseTools *bool `json:"use-tools,omitempty"`

	// EnableBash enables the bash tool. Flag: --enable-bash. Default
	// true. *bool tri-state: nil (absent entry) = unset → default. This is
	// the MASTER switch: config.json enabled_tools.bash provides per-tool
	// granularity and is ANDed with it — either being false disables the
	// bash tool (see ResolveEnableBash).
	EnableBash *bool `json:"enable-bash,omitempty"`

	// BashTimeout is the max wall-clock time for one bash tool call.
	// Flag: --bash-timeout. Duration STRING parsed with
	// time.ParseDuration (e.g. "10m", "1h30m"); empty (unset) means
	// DefaultBashTimeout (10m); "0" (or negative) means unlimited.
	BashTimeout string `json:"bash-timeout,omitempty"`

	// EnableSqz compresses bash tool output with the external 'sqz' binary
	// when it is available. Flag: --enable-sqz. Default false.
	EnableSqz bool `json:"enable-sqz,omitempty"`

	// EnableImages force-enables image attachments even when the backend
	// does not advertise vision support. Flag: --enable-images.
	// Default false.
	EnableImages bool `json:"enable-images,omitempty"`

	// EnableSubagents allows the agent to spawn subagents.
	// Flag: --enable-subagents. Default true. *bool tri-state: nil
	// (absent entry) = unset → default.
	EnableSubagents *bool `json:"enable-subagents,omitempty"`

	// SubagentMaxTurns is the maximum number of turns per subagent.
	// Flag: --subagent-max-turns. Default DefaultSubagentMaxTurns (500).
	// *int tri-state: nil (absent entry) = unset → default; 0 = unlimited
	// (the executor treats maxTurns <= 0 as unbounded, exactly like the
	// flag); a negative value is invalid, warns, and falls back to the
	// default (see ResolveSubagentMaxTurns).
	SubagentMaxTurns *int `json:"subagent-max-turns,omitempty"`

	// SubagentIdleTimeout notifies when a subagent has been truly idle (no
	// stream progress, no in-flight tool, no nested spawn) for this long.
	// Flag: --subagent-idle-timeout. Duration STRING parsed with
	// time.ParseDuration; empty (unset) means DefaultSubagentIdleTimeout
	// (15m); "0" means off.
	SubagentIdleTimeout string `json:"subagent-idle-timeout,omitempty"`

	// SubagentIdleKillAfter kills a subagent that stays truly idle past
	// this duration. Flag: --subagent-idle-kill-after. Duration STRING
	// parsed with time.ParseDuration; empty (unset) means the built-in
	// default (0 = notify only, never kill); "0" means notify only.
	SubagentIdleKillAfter string `json:"subagent-idle-kill-after,omitempty"`

	// MaxStreamRetries is the retry budget for LLM stream errors; 0
	// disables retrying. Flag: --max-stream-retries. *int tri-state: nil
	// (absent entry) = unset → the LATE_MAX_STREAM_RETRIES env value, or
	// executor.DefaultMaxStreamRetries when the env is unset.
	// Precedence: flag > env > config > default (see
	// ResolveMaxStreamRetries).
	MaxStreamRetries *int `json:"max-stream-retries,omitempty"`

	// MaxConcurrentLLMRequests is the process-wide cap on concurrent
	// in-flight LLM requests across all agents and subagents.
	// Flag: --max-concurrent-llm-requests. Default
	// DefaultMaxConcurrentLLMRequests (6). *int tri-state: nil (absent
	// entry) = unset → default; 0 = unlimited; negative is invalid, warns,
	// and falls back to the default (see ResolveMaxConcurrentLLMRequests).
	MaxConcurrentLLMRequests *int `json:"max-concurrent-llm-requests,omitempty"`

	// SuppressThinkingWords biases anti-overthinking tokens (requires the
	// same model for the main agent and subagents). Flag:
	// --suppress-thinking-words. Default false.
	SuppressThinkingWords bool `json:"suppress-thinking-words,omitempty"`

	// LogitBias is the main-agent token bias, in the same format the
	// --logit-bias flag accepts: a JSON object or comma-separated
	// TOKEN_ID:BIAS pairs. Empty (unset) sends no bias. Parsed by the
	// caller with client.ParseLogitBias; the resolver is a pass-through.
	LogitBias string `json:"logit-bias,omitempty"`

	// SubagentLogitBias is the subagent token bias, in the same format the
	// --subagent-logit-bias flag accepts. Empty (unset) sends no bias.
	SubagentLogitBias string `json:"subagent-logit-bias,omitempty"`

	// ShowCWD shows the git branch / working directory in the status bar.
	// Flag: --show-cwd. Default true. *bool tri-state: nil (absent entry)
	// = unset → default.
	ShowCWD *bool `json:"show-cwd,omitempty"`

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
		return cfg.SaveSubagentHistories
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

// -------------------------------------------------------------------------
// CLI-equivalent setting resolvers (flag > config > default)
//
// Each resolver below implements the mandatory precedence for one setting
// that mirrors a command-line flag: an explicitly passed flag wins over the
// config.json entry, which wins over the built-in default. The caller (main)
// passes cliExplicit = true only when flag.Visit reported the flag on the
// command line — config loads after flag.Parse, so that is the only reliable
// explicit-flag signal — together with the parsed flag value. Every resolver
// returns the effective value plus an optional warning for the caller to
// surface; a nil cfg behaves like an absent entry. Boolean and plain-string
// settings have no invalid VALUE (a wrong-typed config.json entry fails the
// whole parse and is surfaced by the degraded-config guard), so their
// warning return is always empty.
// -------------------------------------------------------------------------

// resolveDurationString is the shared body of the duration-string resolvers
// (mirrors ResolveSubagentTimeout's parsing rules): raw is the config entry;
// empty means unset and the default applies; a value that parses via
// time.ParseDuration passes through as-is (callers define non-positive
// semantics — unlimited or off); an unparseable non-empty value warns and
// falls back to the default. key is the config.json key named in warnings.
func resolveDurationString(raw string, cliExplicit bool, cliValue, def time.Duration, key string) (time.Duration, string) {
	if cliExplicit {
		return cliValue, ""
	}
	if raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			return parsed, ""
		}
		return def, fmt.Sprintf("ignoring invalid config.json %s %q; using default %s", key, raw, def)
	}
	return def, ""
}

// ResolveSystemPrompt resolves the system-prompt replacement text
// (flag: --system-prompt). Precedence: explicitly passed flag (even empty,
// so -system-prompt="" can undo a config entry for one run) > config.json
// "system-prompt" > "" (keep the built-in prompt).
func ResolveSystemPrompt(cfg *Config, cliExplicit bool, cliValue string) (string, string) {
	if cliExplicit {
		return cliValue, ""
	}
	if cfg != nil && cfg.SystemPrompt != "" {
		return cfg.SystemPrompt, ""
	}
	return "", ""
}

// ResolveSystemPromptFile resolves the system-prompt replacement file path
// (flag: --system-prompt-file). Precedence: explicitly passed flag >
// config.json "system-prompt-file" > "" (no replacement). The caller reads
// the file and keeps the flag's hard-error behavior for an unreadable path.
func ResolveSystemPromptFile(cfg *Config, cliExplicit bool, cliValue string) (string, string) {
	if cliExplicit {
		return cliValue, ""
	}
	if cfg != nil && cfg.SystemPromptFile != "" {
		return cfg.SystemPromptFile, ""
	}
	return "", ""
}

// ResolveAppendSystemPrompt resolves the text appended to the final system
// prompt (flag: --append-system-prompt). Precedence: explicitly passed flag
// > config.json "append-system-prompt" > "" (append nothing). Appending
// always happens after the file/text replacement resolution — the same
// order the flags use.
func ResolveAppendSystemPrompt(cfg *Config, cliExplicit bool, cliValue string) (string, string) {
	if cliExplicit {
		return cliValue, ""
	}
	if cfg != nil && cfg.AppendSystemPrompt != "" {
		return cfg.AppendSystemPrompt, ""
	}
	return "", ""
}

// ResolveUseTools resolves whether tools are offered to the main agent at
// all (flag: --use-tools). Precedence: explicitly passed flag >
// config.json "use-tools" > true. The config entry is a *bool: nil (absent)
// = unset.
func ResolveUseTools(cfg *Config, cliExplicit bool, cliValue bool) (bool, string) {
	if cliExplicit {
		return cliValue, ""
	}
	if cfg != nil && cfg.UseTools != nil {
		return *cfg.UseTools, ""
	}
	return true, ""
}

// ResolveEnableBash resolves the bash tool's MASTER switch (flag:
// --enable-bash). Precedence: explicitly passed flag > config.json
// "enable-bash" > true. The config entry is a *bool: nil (absent) = unset.
// enabled_tools.bash in config.json provides per-tool granularity and is
// ANDed with this switch by the caller — either being false disables the
// bash tool, exactly as the flag's false always has.
func ResolveEnableBash(cfg *Config, cliExplicit bool, cliValue bool) (bool, string) {
	if cliExplicit {
		return cliValue, ""
	}
	if cfg != nil && cfg.EnableBash != nil {
		return *cfg.EnableBash, ""
	}
	return true, ""
}

// ResolveInjectCWD resolves whether ${{CWD}} is replaced with the working
// directory in the system prompt (flag: --inject-cwd). Precedence:
// explicitly passed flag > config.json "inject-cwd" > true. The config
// entry is a *bool: nil (absent) = unset.
func ResolveInjectCWD(cfg *Config, cliExplicit bool, cliValue bool) (bool, string) {
	if cliExplicit {
		return cliValue, ""
	}
	if cfg != nil && cfg.InjectCWD != nil {
		return *cfg.InjectCWD, ""
	}
	return true, ""
}

// ResolveEnableSubagents resolves whether the agent may spawn subagents
// (flag: --enable-subagents). Precedence: explicitly passed flag >
// config.json "enable-subagents" > true. The config entry is a *bool: nil
// (absent) = unset.
func ResolveEnableSubagents(cfg *Config, cliExplicit bool, cliValue bool) (bool, string) {
	if cliExplicit {
		return cliValue, ""
	}
	if cfg != nil && cfg.EnableSubagents != nil {
		return *cfg.EnableSubagents, ""
	}
	return true, ""
}

// ResolveShowCWD resolves whether the status bar shows the git branch /
// working directory (flag: --show-cwd). Precedence: explicitly passed flag >
// config.json "show-cwd" > true. The config entry is a *bool: nil (absent)
// = unset.
func ResolveShowCWD(cfg *Config, cliExplicit bool, cliValue bool) (bool, string) {
	if cliExplicit {
		return cliValue, ""
	}
	if cfg != nil && cfg.ShowCWD != nil {
		return *cfg.ShowCWD, ""
	}
	return true, ""
}

// ResolveGemmaThinking resolves whether the Gemma <|think|> token is
// prepended to the system prompt (flag: --gemma-thinking). Precedence:
// explicitly passed flag > config.json "gemma-thinking" > false.
func ResolveGemmaThinking(cfg *Config, cliExplicit bool, cliValue bool) (bool, string) {
	if cliExplicit {
		return cliValue, ""
	}
	return cfg != nil && cfg.GemmaThinking, ""
}

// ResolveEnableSqz resolves whether bash tool output is compressed with the
// external 'sqz' binary when available (flag: --enable-sqz). Precedence:
// explicitly passed flag > config.json "enable-sqz" > false.
func ResolveEnableSqz(cfg *Config, cliExplicit bool, cliValue bool) (bool, string) {
	if cliExplicit {
		return cliValue, ""
	}
	return cfg != nil && cfg.EnableSqz, ""
}

// ResolveEnableImages resolves whether image attachments are force-enabled
// even when the backend does not advertise vision support (flag:
// --enable-images). Precedence: explicitly passed flag > config.json
// "enable-images" > false.
func ResolveEnableImages(cfg *Config, cliExplicit bool, cliValue bool) (bool, string) {
	if cliExplicit {
		return cliValue, ""
	}
	return cfg != nil && cfg.EnableImages, ""
}

// ResolveSuppressThinkingWords resolves whether anti-overthinking tokens
// are biased (flag: --suppress-thinking-words; requires the same model for
// the main agent and subagents, which the caller validates). Precedence:
// explicitly passed flag > config.json "suppress-thinking-words" > false.
func ResolveSuppressThinkingWords(cfg *Config, cliExplicit bool, cliValue bool) (bool, string) {
	if cliExplicit {
		return cliValue, ""
	}
	return cfg != nil && cfg.SuppressThinkingWords, ""
}

// ResolveBashTimeout resolves the max wall-clock time for one bash tool
// call (flag: --bash-timeout). Precedence: explicitly passed flag >
// config.json "bash-timeout" > DefaultBashTimeout (10m). The config entry
// is a time.ParseDuration string ("10m", "1h30m"); "0" (or negative)
// passes through — the shell tool treats any non-positive timeout as
// unlimited, exactly as for the flag.
func ResolveBashTimeout(cfg *Config, cliExplicit bool, cliValue time.Duration) (time.Duration, string) {
	var raw string
	if cfg != nil {
		raw = cfg.BashTimeout
	}
	return resolveDurationString(raw, cliExplicit, cliValue, DefaultBashTimeout, "bash-timeout")
}

// ResolveSubagentIdleTimeout resolves the "truly idle" notification
// threshold of the subagent idle watchdog (flag: --subagent-idle-timeout).
// Precedence: explicitly passed flag > config.json "subagent-idle-timeout" >
// DefaultSubagentIdleTimeout (15m). The config entry is a
// time.ParseDuration string; "0" (or negative) passes through — the
// watchdog treats a non-positive threshold as off.
func ResolveSubagentIdleTimeout(cfg *Config, cliExplicit bool, cliValue time.Duration) (time.Duration, string) {
	var raw string
	if cfg != nil {
		raw = cfg.SubagentIdleTimeout
	}
	return resolveDurationString(raw, cliExplicit, cliValue, DefaultSubagentIdleTimeout, "subagent-idle-timeout")
}

// ResolveSubagentIdleKillAfter resolves the sustained-idle duration past
// which an idle subagent is killed (flag: --subagent-idle-kill-after).
// Precedence: explicitly passed flag > config.json
// "subagent-idle-kill-after" > 0 (notify only, never kill). The config
// entry is a time.ParseDuration string; "0" passes through as notify-only.
func ResolveSubagentIdleKillAfter(cfg *Config, cliExplicit bool, cliValue time.Duration) (time.Duration, string) {
	var raw string
	if cfg != nil {
		raw = cfg.SubagentIdleKillAfter
	}
	return resolveDurationString(raw, cliExplicit, cliValue, 0, "subagent-idle-kill-after")
}

// ResolveSubagentMaxTurns resolves the maximum number of turns per subagent
// (flag: --subagent-max-turns). Precedence: explicitly passed flag >
// config.json "subagent-max-turns" > DefaultSubagentMaxTurns (500). The
// config entry is a *int: nil (absent) = unset; 0 = unlimited (the
// executor treats maxTurns <= 0 as unbounded, exactly like the flag); a
// negative value is invalid, warns, and falls back to the default.
func ResolveSubagentMaxTurns(cfg *Config, cliExplicit bool, cliValue int) (int, string) {
	if cliExplicit {
		return cliValue, ""
	}
	if cfg != nil && cfg.SubagentMaxTurns != nil {
		if v := *cfg.SubagentMaxTurns; v >= 0 {
			return v, ""
		}
		return DefaultSubagentMaxTurns,
			fmt.Sprintf("ignoring invalid config.json subagent-max-turns %d; using default %d", *cfg.SubagentMaxTurns, DefaultSubagentMaxTurns)
	}
	return DefaultSubagentMaxTurns, ""
}

// ResolveMaxStreamRetries resolves the LLM stream-error retry budget (flag:
// --max-stream-retries; 0 disables). Precedence: explicitly passed flag >
// LATE_MAX_STREAM_RETRIES env > config.json "max-stream-retries" >
// executor.DefaultMaxStreamRetries. The caller passes the env layer
// pre-resolved: envSet is true only when the env var is present and parses
// as an integer (main warns and ignores an unparseable value), and envValue
// is then the parsed budget — executor.DefaultMaxStreamRetries when the
// env is unset. The config entry is a *int: nil (absent) = unset; 0 =
// disabled; a negative value is invalid, warns, and falls back to envValue.
func ResolveMaxStreamRetries(cfg *Config, cliExplicit bool, cliValue int, envSet bool, envValue int) (int, string) {
	if cliExplicit {
		return cliValue, ""
	}
	if envSet {
		return envValue, ""
	}
	if cfg != nil && cfg.MaxStreamRetries != nil {
		if v := *cfg.MaxStreamRetries; v >= 0 {
			return v, ""
		}
		return envValue,
			fmt.Sprintf("ignoring invalid config.json max-stream-retries %d; using %d", *cfg.MaxStreamRetries, envValue)
	}
	return envValue, ""
}

// ResolveMaxConcurrentLLMRequests resolves the process-wide cap on
// concurrent in-flight LLM requests (flag: --max-concurrent-llm-requests).
// Precedence: explicitly passed flag > config.json
// "max-concurrent-llm-requests" > DefaultMaxConcurrentLLMRequests (6). The
// config entry is a *int: nil (absent) = unset; 0 = unlimited; a negative
// value is invalid, warns, and falls back to the default.
func ResolveMaxConcurrentLLMRequests(cfg *Config, cliExplicit bool, cliValue int) (int, string) {
	if cliExplicit {
		return cliValue, ""
	}
	if cfg != nil && cfg.MaxConcurrentLLMRequests != nil {
		if v := *cfg.MaxConcurrentLLMRequests; v >= 0 {
			return v, ""
		}
		return DefaultMaxConcurrentLLMRequests,
			fmt.Sprintf("ignoring invalid config.json max-concurrent-llm-requests %d; using default %d", *cfg.MaxConcurrentLLMRequests, DefaultMaxConcurrentLLMRequests)
	}
	return DefaultMaxConcurrentLLMRequests, ""
}

// ResolveLogitBias resolves the main-agent token bias string (flag:
// --logit-bias). Precedence: explicitly passed flag > config.json
// "logit-bias" > "" (no bias). The value is a pass-through in the same
// format the flag accepts (JSON object or comma-separated TOKEN_ID:BIAS
// pairs); the caller parses it with client.ParseLogitBias and decides the
// error handling per source.
func ResolveLogitBias(cfg *Config, cliExplicit bool, cliValue string) (string, string) {
	if cliExplicit {
		return cliValue, ""
	}
	if cfg != nil && cfg.LogitBias != "" {
		return cfg.LogitBias, ""
	}
	return "", ""
}

// ResolveSubagentLogitBias resolves the subagent token bias string (flag:
// --subagent-logit-bias). Precedence: explicitly passed flag > config.json
// "subagent-logit-bias" > "" (no bias). Pass-through, like ResolveLogitBias.
func ResolveSubagentLogitBias(cfg *Config, cliExplicit bool, cliValue string) (string, string) {
	if cliExplicit {
		return cliValue, ""
	}
	if cfg != nil && cfg.SubagentLogitBias != "" {
		return cfg.SubagentLogitBias, ""
	}
	return "", ""
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
