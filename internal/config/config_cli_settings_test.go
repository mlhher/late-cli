package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The tests in this file cover the CLI-equivalent settings: every field that
// mirrors a command-line flag one-to-one (kebab-case JSON key == flag name)
// and its Resolve* function implementing the mandatory precedence
// explicitly-passed flag > config.json entry > built-in default.

func boolPtr(v bool) *bool { return &v }
func intPtr(v int) *int    { return &v }

// TestResolveSystemPrompt covers the string group: system-prompt,
// system-prompt-file, and append-system-prompt (all resolved identically).
func TestResolveSystemPrompt(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *Config
		cliExplicit bool
		cliValue    string
		want        string
	}{
		{
			name:        "flag explicit wins over config",
			cfg:         &Config{SystemPrompt: "from config"},
			cliExplicit: true,
			cliValue:    "from flag",
			want:        "from flag",
		},
		{
			// -system-prompt="" is explicit: it undoes a config entry for
			// the run and falls back to the built-in prompt.
			name:        "flag explicitly empty wins over config",
			cfg:         &Config{SystemPrompt: "from config"},
			cliExplicit: true,
			cliValue:    "",
			want:        "",
		},
		{
			name: "config wins over empty default",
			cfg:  &Config{SystemPrompt: "from config"},
			want: "from config",
		},
		{
			name: "empty config entry uses default",
			cfg:  &Config{},
			want: "",
		},
		{
			name: "nil config uses default",
			cfg:  nil,
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warning := ResolveSystemPrompt(tt.cfg, tt.cliExplicit, tt.cliValue)
			if got != tt.want || warning != "" {
				t.Fatalf("ResolveSystemPrompt() = (%q, %q), want (%q, \"\")", got, warning, tt.want)
			}
		})
	}
}

func TestResolveSystemPromptFile(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *Config
		cliExplicit bool
		cliValue    string
		want        string
	}{
		{"flag explicit wins over config", &Config{SystemPromptFile: "/cfg.md"}, true, "/flag.md", "/flag.md"},
		{"config wins over empty default", &Config{SystemPromptFile: "/cfg.md"}, false, "", "/cfg.md"},
		{"empty config uses default", &Config{}, false, "", ""},
		{"nil config uses default", nil, false, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warning := ResolveSystemPromptFile(tt.cfg, tt.cliExplicit, tt.cliValue)
			if got != tt.want || warning != "" {
				t.Fatalf("ResolveSystemPromptFile() = (%q, %q), want (%q, \"\")", got, warning, tt.want)
			}
		})
	}
}

func TestResolveAppendSystemPrompt(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *Config
		cliExplicit bool
		cliValue    string
		want        string
	}{
		{"flag explicit wins over config", &Config{AppendSystemPrompt: "cfg"}, true, "flag", "flag"},
		{"config wins over empty default", &Config{AppendSystemPrompt: "cfg"}, false, "", "cfg"},
		{"empty config uses default", &Config{}, false, "", ""},
		{"nil config uses default", nil, false, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warning := ResolveAppendSystemPrompt(tt.cfg, tt.cliExplicit, tt.cliValue)
			if got != tt.want || warning != "" {
				t.Fatalf("ResolveAppendSystemPrompt() = (%q, %q), want (%q, \"\")", got, warning, tt.want)
			}
		})
	}
}

// TestResolveDefaultTrueBools covers the *bool tri-state group whose flag
// default is true: use-tools, enable-bash, inject-cwd, enable-subagents,
// show-cwd. All five resolve with identical semantics; the shared table runs
// every case against each resolver via its field accessor.
func TestResolveDefaultTrueBools(t *testing.T) {
	resolvers := map[string]struct {
		resolve func(*Config, bool, bool) (bool, string)
		field   func(*Config) **bool
	}{
		"use-tools": {
			resolve: ResolveUseTools,
			field:   func(c *Config) **bool { return &c.UseTools },
		},
		"enable-bash": {
			resolve: ResolveEnableBash,
			field:   func(c *Config) **bool { return &c.EnableBash },
		},
		"inject-cwd": {
			resolve: ResolveInjectCWD,
			field:   func(c *Config) **bool { return &c.InjectCWD },
		},
		"enable-subagents": {
			resolve: ResolveEnableSubagents,
			field:   func(c *Config) **bool { return &c.EnableSubagents },
		},
		"show-cwd": {
			resolve: ResolveShowCWD,
			field:   func(c *Config) **bool { return &c.ShowCWD },
		},
	}

	for name, r := range resolvers {
		t.Run(name, func(t *testing.T) {
			tests := []struct {
				name        string
				cfg         *Config
				cliExplicit bool
				cliValue    bool
				want        bool
			}{
				{
					name:        "flag explicit true wins over config false",
					cfg:         withBoolField(r.field, false),
					cliExplicit: true,
					cliValue:    true,
					want:        true,
				},
				{
					name:        "flag explicit false wins over config true",
					cfg:         withBoolField(r.field, true),
					cliExplicit: true,
					cliValue:    false,
					want:        false,
				},
				{
					name: "config explicit false is honored (not unset)",
					cfg:  withBoolField(r.field, false),
					want: false,
				},
				{
					name: "config explicit true is honored",
					cfg:  withBoolField(r.field, true),
					want: true,
				},
				{
					name: "absent config entry (nil pointer) uses default",
					cfg:  &Config{},
					want: true,
				},
				{
					name: "nil config uses default",
					cfg:  nil,
					want: true,
				},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					got, warning := r.resolve(tt.cfg, tt.cliExplicit, tt.cliValue)
					if got != tt.want || warning != "" {
						t.Fatalf("resolve() = (%v, %q), want (%v, \"\")", got, warning, tt.want)
					}
				})
			}
		})
	}
}

// withBoolField returns a config whose named *bool field is set to v (an
// explicit entry — the tri-state "set" case, distinct from a nil/unset one).
func withBoolField(field func(*Config) **bool, v bool) *Config {
	cfg := &Config{}
	*field(cfg) = boolPtr(v)
	return cfg
}

// TestResolveDefaultFalseBools covers the plain-bool group whose flag default
// is false: gemma-thinking, enable-sqz, enable-images,
// suppress-thinking-words.
func TestResolveDefaultFalseBools(t *testing.T) {
	resolvers := map[string]struct {
		resolve func(*Config, bool, bool) (bool, string)
		set     func(*Config, bool)
	}{
		"gemma-thinking": {
			resolve: ResolveGemmaThinking,
			set:     func(c *Config, v bool) { c.GemmaThinking = v },
		},
		"enable-sqz": {
			resolve: ResolveEnableSqz,
			set:     func(c *Config, v bool) { c.EnableSqz = v },
		},
		"enable-images": {
			resolve: ResolveEnableImages,
			set:     func(c *Config, v bool) { c.EnableImages = v },
		},
		"suppress-thinking-words": {
			resolve: ResolveSuppressThinkingWords,
			set:     func(c *Config, v bool) { c.SuppressThinkingWords = v },
		},
	}

	for name, r := range resolvers {
		t.Run(name, func(t *testing.T) {
			tests := []struct {
				name        string
				cfg         *Config
				cliExplicit bool
				cliValue    bool
				want        bool
			}{
				{
					name:        "flag explicit false wins over config true",
					cfg:         withPlainBool(r.set, true),
					cliExplicit: true,
					cliValue:    false,
					want:        false,
				},
				{
					name:        "flag explicit true wins over config false",
					cfg:         withPlainBool(r.set, false),
					cliExplicit: true,
					cliValue:    true,
					want:        true,
				},
				{
					name: "config true honored",
					cfg:  withPlainBool(r.set, true),
					want: true,
				},
				{
					// false = unset for a default-false plain bool.
					name: "config false is the unset default",
					cfg:  withPlainBool(r.set, false),
					want: false,
				},
				{
					name: "nil config defaults false",
					cfg:  nil,
					want: false,
				},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					got, warning := r.resolve(tt.cfg, tt.cliExplicit, tt.cliValue)
					if got != tt.want || warning != "" {
						t.Fatalf("resolve() = (%v, %q), want (%v, \"\")", got, warning, tt.want)
					}
				})
			}
		})
	}
}

// withPlainBool returns a config with the named plain-bool field set to v.
func withPlainBool(set func(*Config, bool), v bool) *Config {
	cfg := &Config{}
	set(cfg, v)
	return cfg
}

// TestResolveDurationSettings covers the duration-string group: bash-timeout,
// subagent-idle-timeout, subagent-idle-kill-after. All mirror
// ResolveSubagentTimeout's parsing rules: empty = unset (default), a
// parseable value passes through (0/negative keep their per-setting
// semantics), an unparseable value warns and falls back to the default.
func TestResolveDurationSettings(t *testing.T) {
	resolvers := map[string]struct {
		resolve func(*Config, bool, time.Duration) (time.Duration, string)
		set     func(*Config, string)
		def     time.Duration
	}{
		"bash-timeout": {
			resolve: ResolveBashTimeout,
			set:     func(c *Config, v string) { c.BashTimeout = v },
			def:     DefaultBashTimeout,
		},
		"subagent-idle-timeout": {
			resolve: ResolveSubagentIdleTimeout,
			set:     func(c *Config, v string) { c.SubagentIdleTimeout = v },
			def:     DefaultSubagentIdleTimeout,
		},
		"subagent-idle-kill-after": {
			resolve: ResolveSubagentIdleKillAfter,
			set:     func(c *Config, v string) { c.SubagentIdleKillAfter = v },
			def:     0,
		},
	}

	for name, r := range resolvers {
		t.Run(name, func(t *testing.T) {
			tests := []struct {
				name        string
				raw         string
				cliExplicit bool
				cliValue    time.Duration
				want        time.Duration
				wantWarning string
			}{
				{
					name:        "flag explicit wins over config",
					raw:         "1h",
					cliExplicit: true,
					cliValue:    2 * time.Minute,
					want:        2 * time.Minute,
				},
				{
					name: "config parses to the configured duration",
					raw:  "45m",
					want: 45 * time.Minute,
				},
				{
					name: "config zero passes through (unlimited/off)",
					raw:  "0",
					want: 0,
				},
				{
					name: "config negative passes through",
					raw:  "-5m",
					want: -5 * time.Minute,
				},
				{
					name:        "unparseable config warns and falls back to default",
					raw:         "garbage",
					want:        r.def,
					wantWarning: `ignoring invalid config.json ` + name + ` "garbage"; using default`,
				},
				{
					name:        "unitless config value warns and falls back to default",
					raw:         "5",
					want:        r.def,
					wantWarning: `ignoring invalid config.json ` + name + ` "5"; using default`,
				},
				{
					name: "empty config entry uses default",
					raw:  "",
					want: r.def,
				},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					cfg := &Config{}
					r.set(cfg, tt.raw)
					got, warning := r.resolve(cfg, tt.cliExplicit, tt.cliValue)
					if got != tt.want {
						t.Fatalf("resolve() = %v, want %v", got, tt.want)
					}
					if !strings.HasPrefix(warning, tt.wantWarning) {
						t.Fatalf("resolve() warning = %q, want prefix %q", warning, tt.wantWarning)
					}
				})
			}
			// nil config uses the default.
			if got, warning := r.resolve(nil, false, 0); got != r.def || warning != "" {
				t.Fatalf("resolve(nil) = (%v, %q), want (%v, \"\")", got, warning, r.def)
			}
		})
	}
}

func TestResolveSubagentMaxTurns(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *Config
		cliExplicit bool
		cliValue    int
		want        int
		wantWarning string
	}{
		{
			name:        "flag explicit wins over config",
			cfg:         &Config{SubagentMaxTurns: intPtr(42)},
			cliExplicit: true,
			cliValue:    7,
			want:        7,
		},
		{
			name:        "explicit zero flag wins over config (unlimited)",
			cfg:         &Config{SubagentMaxTurns: intPtr(42)},
			cliExplicit: true,
			cliValue:    0,
			want:        0,
		},
		{
			name: "config set is honored",
			cfg:  &Config{SubagentMaxTurns: intPtr(42)},
			want: 42,
		},
		{
			name: "config zero means unlimited (passes through)",
			cfg:  &Config{SubagentMaxTurns: intPtr(0)},
			want: 0,
		},
		{
			name:        "negative config warns and falls back to default",
			cfg:         &Config{SubagentMaxTurns: intPtr(-3)},
			want:        DefaultSubagentMaxTurns,
			wantWarning: "ignoring invalid config.json subagent-max-turns -3; using default 500",
		},
		{
			name: "nil entry uses default",
			cfg:  &Config{},
			want: DefaultSubagentMaxTurns,
		},
		{
			name: "nil config uses default",
			cfg:  nil,
			want: DefaultSubagentMaxTurns,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warning := ResolveSubagentMaxTurns(tt.cfg, tt.cliExplicit, tt.cliValue)
			if got != tt.want {
				t.Fatalf("ResolveSubagentMaxTurns() = %d, want %d", got, tt.want)
			}
			if warning != tt.wantWarning {
				t.Fatalf("ResolveSubagentMaxTurns() warning = %q, want %q", warning, tt.wantWarning)
			}
		})
	}
}

// TestResolveMaxStreamRetries pins the four-layer precedence: flag > env >
// config > default. The caller passes the env layer pre-resolved (envSet,
// envValue); envValue is executor.DefaultMaxStreamRetries when the env is
// unset.
func TestResolveMaxStreamRetries(t *testing.T) {
	const envBudget = 3  // stands in for a parsed LATE_MAX_STREAM_RETRIES
	const defBudget = 10 // stands in for executor.DefaultMaxStreamRetries

	tests := []struct {
		name        string
		cfg         *Config
		cliExplicit bool
		cliValue    int
		envSet      bool
		want        int
		wantWarning string
	}{
		{
			name:        "flag explicit wins over env and config",
			cfg:         &Config{MaxStreamRetries: intPtr(9)},
			cliExplicit: true,
			cliValue:    5,
			envSet:      true,
			want:        5,
		},
		{
			name:   "env wins over config",
			cfg:    &Config{MaxStreamRetries: intPtr(9)},
			envSet: true,
			want:   envBudget,
		},
		{
			name: "config wins when env unset",
			cfg:  &Config{MaxStreamRetries: intPtr(9)},
			want: 9,
		},
		{
			name: "config zero disables retries",
			cfg:  &Config{MaxStreamRetries: intPtr(0)},
			want: 0,
		},
		{
			name:        "negative config warns and falls back to env/default value",
			cfg:         &Config{MaxStreamRetries: intPtr(-2)},
			want:        defBudget,
			wantWarning: "ignoring invalid config.json max-stream-retries -2; using 10",
		},
		{
			name: "nil entry falls back to env/default value",
			cfg:  &Config{},
			want: defBudget,
		},
		{
			name: "nil config falls back to env/default value",
			cfg:  nil,
			want: defBudget,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// envValue mirrors main: the parsed env budget when the env is
			// set, the executor default when it is unset.
			envValue := defBudget
			if tt.envSet {
				envValue = envBudget
			}
			got, warning := ResolveMaxStreamRetries(tt.cfg, tt.cliExplicit, tt.cliValue, tt.envSet, envValue)
			if got != tt.want {
				t.Fatalf("ResolveMaxStreamRetries() = %d, want %d", got, tt.want)
			}
			if warning != tt.wantWarning {
				t.Fatalf("ResolveMaxStreamRetries() warning = %q, want %q", warning, tt.wantWarning)
			}
		})
	}
}

func TestResolveMaxConcurrentLLMRequests(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *Config
		cliExplicit bool
		cliValue    int
		want        int
		wantWarning string
	}{
		{
			name:        "flag explicit wins over config",
			cfg:         &Config{MaxConcurrentLLMRequests: intPtr(2)},
			cliExplicit: true,
			cliValue:    8,
			want:        8,
		},
		{
			name: "config set is honored",
			cfg:  &Config{MaxConcurrentLLMRequests: intPtr(2)},
			want: 2,
		},
		{
			name: "config zero means unlimited (passes through)",
			cfg:  &Config{MaxConcurrentLLMRequests: intPtr(0)},
			want: 0,
		},
		{
			name:        "negative config warns and falls back to default",
			cfg:         &Config{MaxConcurrentLLMRequests: intPtr(-1)},
			want:        DefaultMaxConcurrentLLMRequests,
			wantWarning: "ignoring invalid config.json max-concurrent-llm-requests -1; using default 6",
		},
		{
			name: "nil entry uses default",
			cfg:  &Config{},
			want: DefaultMaxConcurrentLLMRequests,
		},
		{
			name: "nil config uses default",
			cfg:  nil,
			want: DefaultMaxConcurrentLLMRequests,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warning := ResolveMaxConcurrentLLMRequests(tt.cfg, tt.cliExplicit, tt.cliValue)
			if got != tt.want {
				t.Fatalf("ResolveMaxConcurrentLLMRequests() = %d, want %d", got, tt.want)
			}
			if warning != tt.wantWarning {
				t.Fatalf("ResolveMaxConcurrentLLMRequests() warning = %q, want %q", warning, tt.wantWarning)
			}
		})
	}
}

// TestResolveLogitBias covers the pass-through bias strings: no format
// validation in the resolver (client.ParseLogitBias stays the caller's job).
func TestResolveLogitBias(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *Config
		cliExplicit bool
		cliValue    string
		want        string
	}{
		{"flag explicit wins over config", &Config{LogitBias: `{"5":-1}`}, true, "5:-1", "5:-1"},
		{"config wins over empty default", &Config{LogitBias: `{"5":-1}`}, false, "", `{"5":-1}`},
		{"empty config uses default", &Config{}, false, "", ""},
		{"nil config uses default", nil, false, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warning := ResolveLogitBias(tt.cfg, tt.cliExplicit, tt.cliValue)
			if got != tt.want || warning != "" {
				t.Fatalf("ResolveLogitBias() = (%q, %q), want (%q, \"\")", got, warning, tt.want)
			}
		})
	}
}

func TestResolveSubagentLogitBias(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *Config
		cliExplicit bool
		cliValue    string
		want        string
	}{
		{"flag explicit wins over config", &Config{SubagentLogitBias: "5:-1"}, true, "5:2", "5:2"},
		{"config wins over empty default", &Config{SubagentLogitBias: "5:-1"}, false, "", "5:-1"},
		{"empty config uses default", &Config{}, false, "", ""},
		{"nil config uses default", nil, false, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warning := ResolveSubagentLogitBias(tt.cfg, tt.cliExplicit, tt.cliValue)
			if got != tt.want || warning != "" {
				t.Fatalf("ResolveSubagentLogitBias() = (%q, %q), want (%q, \"\")", got, warning, tt.want)
			}
		})
	}
}

// TestConfig_CLIEquivalentKeysJSONRoundTrip guards the user's mapping rule —
// the JSON key of every CLI-equivalent setting is its flag name in kebab-case
// — in BOTH directions:
//
//  1. Unmarshal: a config.json containing every new kebab-case key populates
//     the matching struct field (a typo'd tag would leave the field zero).
//  2. Marshal: a fully populated struct serializes each field under its
//     kebab-case key.
func TestConfig_CLIEquivalentKeysJSONRoundTrip(t *testing.T) {
	// Every kebab-case key, one per new CLI-equivalent setting.
	jsonLiteral := `{
		"system-prompt": "sp",
		"system-prompt-file": "/tmp/sp.md",
		"append-system-prompt": "asp",
		"inject-cwd": false,
		"gemma-thinking": true,
		"use-tools": false,
		"enable-bash": false,
		"bash-timeout": "90s",
		"enable-sqz": true,
		"enable-images": true,
		"enable-subagents": false,
		"subagent-max-turns": 42,
		"subagent-idle-timeout": "5m",
		"subagent-idle-kill-after": "1m",
		"max-stream-retries": 3,
		"max-concurrent-llm-requests": 2,
		"suppress-thinking-words": true,
		"logit-bias": "{\"5\":-1}",
		"subagent-logit-bias": "5:2",
		"show-cwd": false
	}`

	var cfg Config
	if err := json.Unmarshal([]byte(jsonLiteral), &cfg); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	checks := []struct {
		name string
		ok   bool
		want string
	}{
		{"SystemPrompt", cfg.SystemPrompt == "sp", "sp"},
		{"SystemPromptFile", cfg.SystemPromptFile == "/tmp/sp.md", "/tmp/sp.md"},
		{"AppendSystemPrompt", cfg.AppendSystemPrompt == "asp", "asp"},
		{"InjectCWD", cfg.InjectCWD != nil && !*cfg.InjectCWD, "explicit false"},
		{"GemmaThinking", cfg.GemmaThinking, "true"},
		{"UseTools", cfg.UseTools != nil && !*cfg.UseTools, "explicit false"},
		{"EnableBash", cfg.EnableBash != nil && !*cfg.EnableBash, "explicit false"},
		{"BashTimeout", cfg.BashTimeout == "90s", "90s"},
		{"EnableSqz", cfg.EnableSqz, "true"},
		{"EnableImages", cfg.EnableImages, "true"},
		{"EnableSubagents", cfg.EnableSubagents != nil && !*cfg.EnableSubagents, "explicit false"},
		{"SubagentMaxTurns", cfg.SubagentMaxTurns != nil && *cfg.SubagentMaxTurns == 42, "42"},
		{"SubagentIdleTimeout", cfg.SubagentIdleTimeout == "5m", "5m"},
		{"SubagentIdleKillAfter", cfg.SubagentIdleKillAfter == "1m", "1m"},
		{"MaxStreamRetries", cfg.MaxStreamRetries != nil && *cfg.MaxStreamRetries == 3, "3"},
		{"MaxConcurrentLLMRequests", cfg.MaxConcurrentLLMRequests != nil && *cfg.MaxConcurrentLLMRequests == 2, "2"},
		{"SuppressThinkingWords", cfg.SuppressThinkingWords, "true"},
		{"LogitBias", cfg.LogitBias == `{"5":-1}`, `{"5":-1}`},
		{"SubagentLogitBias", cfg.SubagentLogitBias == "5:2", "5:2"},
		{"ShowCWD", cfg.ShowCWD != nil && !*cfg.ShowCWD, "explicit false"},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("key for %s did not round-trip into the struct (want %s) — tag typo?", c.name, c.want)
		}
	}

	// Marshal direction: a fully populated struct must emit every kebab-case
	// key, including the explicit zeros (a 0 *int, an explicit-false *bool)
	// that omitempty must keep because the pointer is non-nil.
	data, err := json.Marshal(&cfg)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	out := string(data)
	for _, key := range []string{
		"system-prompt", "system-prompt-file", "append-system-prompt",
		"inject-cwd", "gemma-thinking", "use-tools", "enable-bash",
		"bash-timeout", "enable-sqz", "enable-images", "enable-subagents",
		"subagent-max-turns", "subagent-idle-timeout", "subagent-idle-kill-after",
		"max-stream-retries", "max-concurrent-llm-requests",
		"suppress-thinking-words", "logit-bias", "subagent-logit-bias", "show-cwd",
	} {
		if !strings.Contains(out, `"`+key+`":`) {
			t.Errorf("Marshal output missing kebab-case key %q: %s", key, out)
		}
	}

	// And the marshaled form unmarshals to an identical struct (full cycle).
	var back Config
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("re-Unmarshal() error = %v", err)
	}
	if !reflect.DeepEqual(cfg, back) {
		t.Fatalf("round-trip changed the config:\nwant %#v\ngot  %#v", cfg, back)
	}
}
