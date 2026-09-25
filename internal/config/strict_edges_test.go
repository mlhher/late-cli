package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tests in this file pin the adversarial edges of the strict parser that
// the hand-editing scenarios most often hit: trailing data after the
// top-level object, a UTF-8 byte-order mark, duplicate keys, CRLF line
// endings, keys nested inside the map-typed sections, and JSON null values
// for the FlexBool-typed fields.

// TestParseConfigContent_TrailingDataAfterTopLevelObject pins R2 for the
// "two documents in one file" hand-editing mistake: everything after the
// top-level object's closing '}' — a second object, an array, a number,
// null, garbage, or a stray brace — must abort the parse with a trailing
// data error, never be silently ignored.
func TestParseConfigContent_TrailingDataAfterTopLevelObject(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"second object", `{"theme":"late"}{"theme":"dark"}`},
		{"array", `{"theme":"late"} [1, 2]`},
		{"number", `{"theme":"late"} 5`},
		{"null", `{"theme":"late"} null`},
		{"garbage", `{"theme":"late"} garbage`},
		{"stray closing brace", `{"theme":"late"} }`},
		{"stray closing bracket", `{"theme":"late"} ]`},
		{"garbage on a later line", "{\n  \"theme\": \"late\"\n}\nextra"},
		{"second object after blank lines", "{\n  \"theme\": \"late\"\n}\n\n  {\"a\": 1}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parseConfigContent("/x/config.json", []byte(tc.content))
			if err == nil {
				t.Fatalf("parseConfigContent(%q) = %#v, want a trailing-data error", tc.content, cfg)
			}
			if cfg != nil {
				t.Fatalf("parseConfigContent(%q) returned a config alongside the error", tc.content)
			}
			if !strings.Contains(err.Error(), "trailing") {
				t.Fatalf("error = %q, want it to mention trailing data", err.Error())
			}
		})
	}
}

// TestParseConfigContent_TrailingWhitespaceOnlyIsAccepted pins that valid
// trailing whitespace after the top-level object does NOT error.
func TestParseConfigContent_TrailingWhitespaceOnlyIsAccepted(t *testing.T) {
	for _, ws := range []string{"", "\n", "  \n\n \t ", "\r\n\r\n"} {
		cfg, err := parseConfigContent("/x/config.json", []byte(`{"theme":"late"}`+ws))
		if err != nil {
			t.Fatalf("trailing whitespace %q: unexpected error %v", ws, err)
		}
		if cfg == nil || cfg.Theme != "late" {
			t.Fatalf("trailing whitespace %q: cfg = %#v, want theme late", ws, cfg)
		}
	}
}

// TestParseConfigContent_BOMIsTolerated pins that a UTF-8 byte-order mark —
// which Windows editors (Notepad "UTF-8 with BOM") and PowerShell
// Out-File happily prepend — does not brick startup: the BOM carries no
// information and must be stripped before parsing.
func TestParseConfigContent_BOMIsTolerated(t *testing.T) {
	content := "\xef\xbb\xbf{\n  \"theme\": \"late\",\n  \"save_subagent_histories\": \"on\"\n}"
	cfg, err := parseConfigContent("/x/config.json", []byte(content))
	if err != nil {
		t.Fatalf("parseConfigContent(BOM-prefixed config) error = %v; a BOM must not abort startup", err)
	}
	if cfg == nil || cfg.Theme != "late" || !cfg.SaveSubagentHistories.Bool() {
		t.Fatalf("BOM-prefixed config parsed wrong: %#v", cfg)
	}

	// An error deeper in a BOM-prefixed file must still render the position
	// the editor shows: line 3 holds the unknown key and the BOM must not
	// shift line or column.
	broken := "\xef\xbb\xbf{\n  \"theme\": \"late\",\n  \"save_subagent_history\": true\n}"
	_, err = parseConfigContent("/x/config.json", []byte(broken))
	if err == nil {
		t.Fatal("expected the unknown-key error to still fire inside a BOM-prefixed file")
	}
	if !strings.Contains(err.Error(), "at line 3, column 3") {
		t.Fatalf("error = %q, want line 3, column 3 (the key's editor position)", err.Error())
	}
}

// TestParseConfigContent_WhitespaceOnlyFileIsPositioned pins that a
// whitespace-only config file is a positioned "empty document" error, not a
// panic or a silent default boot.
func TestParseConfigContent_WhitespaceOnlyFileIsPositioned(t *testing.T) {
	for _, content := range []string{" ", "\n", "\t\n  \n ", "\r\n\r\n"} {
		_, err := parseConfigContent("/x/config.json", []byte(content))
		if err == nil {
			t.Fatalf("whitespace-only content %q: expected an error", content)
		}
		if !strings.Contains(err.Error(), "line 1, column 1") {
			t.Fatalf("whitespace-only content %q: error = %q, want a line-1 column-1 position", content, err.Error())
		}
	}
}

// TestParseConfigContent_DuplicateKeysLastWins pins the encoding/json
// semantics for duplicate top-level keys: the LAST occurrence wins, matching
// what a decode produces.
func TestParseConfigContent_DuplicateKeysLastWins(t *testing.T) {
	cfg, err := parseConfigContent("/x/config.json", []byte(`{"theme":"a","theme":"b"}`))
	if err != nil {
		t.Fatalf("duplicate keys: unexpected error %v", err)
	}
	if cfg.Theme != "b" {
		t.Fatalf("duplicate theme = %q, want the last occurrence %q", cfg.Theme, "b")
	}
}

// TestParseConfigContent_DuplicateKeyInvalidValueStillErrors pins the strict
// stance for a duplicate key whose losing occurrence carries an invalid
// value: the file is hand-authored and one junk value is worth surfacing
// even though the decode would keep the winning occurrence.
func TestParseConfigContent_DuplicateKeyInvalidValueStillErrors(t *testing.T) {
	_, err := parseConfigContent("/x/config.json", []byte(`{"save_subagent_histories":"maybe","save_subagent_histories":"on"}`))
	if err == nil {
		t.Fatal("expected an error for the invalid boolean synonym in the losing duplicate")
	}
	if !strings.Contains(err.Error(), `"save_subagent_histories"`) || !strings.Contains(err.Error(), "not a valid boolean value") {
		t.Fatalf("error = %q, want the boolean-synonyms message naming the entry", err.Error())
	}
}

// TestParseConfigContent_CRLFLineEndings pins that CRLF line endings do not
// shift the reported column: the error on line 3 must report the column of
// the value on that line, not one past the CR.
func TestParseConfigContent_CRLFLineEndings(t *testing.T) {
	content := "{\r\n  \"theme\": \"late\",\r\n  \"save_subagent_histories\": \"actve\"\r\n}"
	_, err := parseConfigContent("/x/config.json", []byte(content))
	if err == nil {
		t.Fatal("expected the invalid boolean synonym error")
	}
	// `"actve"` starts right after `  "save_subagent_histories": ` — 2 spaces
	// + 25 key bytes (with quotes) + ':' + ' ' = column 30, 1-based.
	want := `error in /x/config.json at line 3, column 30: "actve" is not a valid boolean value for "save_subagent_histories".`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q\nwant it to contain %q", err.Error(), want)
	}
}

// TestParseConfigContent_SyntaxErrorLineColCRLF pins a syntax error in a
// CRLF file: the position is the offending character's column on its line.
func TestParseConfigContent_SyntaxErrorLineColCRLF(t *testing.T) {
	content := "{\r\n  \"theme\": \"late\",\r\n  \"bogus\"]\r\n}"
	_, err := parseConfigContent("/x/config.json", []byte(content))
	if err == nil {
		t.Fatal("expected a syntax error")
	}
	// The stray ']' ends line 3 (`  "bogus"]`): the ten bytes before it put it
	// in column 11 — the CR of the preceding CRLF must not shift the column.
	want := `error in /x/config.json at line 3, column 11: invalid character ']' after object key`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q\nwant it to contain %q", err.Error(), want)
	}
}

// TestParseConfigContent_KeysNestedInMapSectionsAreNotFlagged pins that the
// unknown-key walk only treats TOP-LEVEL entries as config keys: entries of
// the map-typed sections (enabled_tools, agent_models) are data, never
// unknown config keys, whatever they are named.
func TestParseConfigContent_KeysNestedInMapSectionsAreNotFlagged(t *testing.T) {
	content := `{
  "enabled_tools": {"bash": true, "some_plugin_tool": false, "not-even-kebab": true},
  "agent_models": {"orchestrator": "prov", "code-reviewer": "m2"}
}`
	cfg, err := parseConfigContent("/x/config.json", []byte(content))
	if err != nil {
		t.Fatalf("map-section entries must never be flagged as unknown config keys: %v", err)
	}
	if cfg.EnabledTools["some_plugin_tool"] {
		t.Fatal("enabled_tools value did not survive")
	}
	if cfg.AgentModels["code-reviewer"] != "m2" {
		t.Fatal("agent_models value did not survive")
	}

	// A non-boolean enabled_tools value is still a positioned TYPE error that
	// names the nested path (the walk must not report it as an unknown key).
	bad := `{"enabled_tools": {"bash": "on"}}`
	_, err = parseConfigContent("/x/config.json", []byte(bad))
	if err == nil {
		t.Fatal("expected a type error for a non-boolean enabled_tools value")
	}
	if !strings.Contains(err.Error(), `"enabled_tools.bash" must be a boolean`) {
		t.Fatalf("error = %q, want the nested-path type message", err.Error())
	}
}

// TestParseConfigContent_NullFlexBoolFieldStaysZeroValue pins the JSON null
// handling of the branch's FlexBool field: null leaves the zero value
// without erroring, exactly like an absent entry.
func TestParseConfigContent_NullFlexBoolFieldStaysZeroValue(t *testing.T) {
	cfg, err := parseConfigContent("/x/config.json", []byte(`{"save_subagent_histories": null}`))
	if err != nil {
		t.Fatalf("null save_subagent_histories must not error: %v", err)
	}
	if cfg.SaveSubagentHistories.Bool() {
		t.Fatal("null save_subagent_histories must leave the zero value (false)")
	}
}

// TestParseConfigContent_NullTriStatePointerStaysNil pins the JSON null
// handling of *FlexBool tri-state fields on a test-local struct: null leaves
// the pointer nil (unset), so the built-in default applies, exactly like an
// absent entry.
func TestParseConfigContent_NullTriStatePointerStaysNil(t *testing.T) {
	type triState struct {
		Ptr *FlexBool `json:"ptr,omitempty"`
	}
	var cfg triState
	if err := json.Unmarshal([]byte(`{"ptr": null}`), &cfg); err != nil {
		t.Fatalf("null tri-state entry must not error: %v", err)
	}
	if cfg.Ptr != nil {
		t.Fatalf("null must leave the tri-state pointer unset (nil), got %#v", *cfg.Ptr)
	}
}

// TestLoadConfig_UpstreamEraConfigStillLoads pins the old upstream config
// compatibility: a pre-existing user config.json from the upstream/main era
// — only enabled_tools, models, and agent_models (the sections that existed
// before the strict parser) — must load cleanly. New strictness must never
// orphan an existing user's settings.
func TestLoadConfig_UpstreamEraConfigStillLoads(t *testing.T) {
	configRoot := t.TempDir()
	setUserConfigEnv(t, configRoot)
	configPath := lateConfigPath(t)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	upstreamEra := `{
  "enabled_tools": {
    "bash": true,
    "read_file": true,
    "write_file": false
  },
  "models": [
    {"id": "local-prov", "url": "http://localhost:8080", "key": "sk-1", "model": "model-a"},
    {"url": "http://fallback:8080", "key": "sk-2", "model": "model-b"}
  ],
  "agent_models": {
    "orchestrator": "local-prov",
    "code-reviewer": "model-b"
  }
}`
	if err := os.WriteFile(configPath, []byte(upstreamEra), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v; the upstream-era config must still load", err)
	}
	if cfg.EnabledTools["write_file"] || !cfg.EnabledTools["bash"] {
		t.Fatalf("enabled_tools did not survive: %#v", cfg.EnabledTools)
	}
	if len(cfg.Models) != 2 || cfg.Models[0].ID != "local-prov" || cfg.Models[1].Model != "model-b" {
		t.Fatalf("models did not survive: %#v", cfg.Models)
	}
	if setting, ok := cfg.GetModelForAgent("orchestrator"); !ok || setting.URL != "http://localhost:8080" {
		t.Fatalf("agent_models orchestrator lookup failed: %#v ok=%v", setting, ok)
	}
	// agent_models referencing a bare model name keeps the legacy fallback.
	if setting, ok := cfg.GetModelForAgent("code-reviewer"); !ok || setting.Model != "model-b" {
		t.Fatalf("agent_models legacy name lookup failed: %#v ok=%v", setting, ok)
	}
	// New-tool defaults still merge in for an old config file.
	if _, ok := cfg.EnabledTools["spawn_subagent"]; !ok {
		t.Fatalf("default tools must merge into an upstream-era config: %#v", cfg.EnabledTools)
	}
}

// TestConfig_NullPlainFlexBoolKeepsZeroValue pins that a JSON null for a
// PLAIN FlexBool config field leaves the zero value without erroring.
func TestConfig_NullPlainFlexBoolKeepsZeroValue(t *testing.T) {
	cfg, err := parseConfigContent("/x/config.json", []byte(`{"save_subagent_histories": null}`))
	if err != nil {
		t.Fatalf("null plain-FlexBool entries must not error: %v", err)
	}
	if cfg.SaveSubagentHistories.Bool() {
		t.Fatal("null must leave the plain FlexBool at its zero value")
	}
}

// TestSaveConfig_NilTriStateFieldsAreOmitted pins that SaveConfig marshals
// unset *FlexBool fields as absent entries (omitempty on the nil pointer),
// never as explicit nulls that the strict parser would then have to
// re-accept — the shape is exercised on a test-local struct so the contract
// holds for any *FlexBool field — and that the branch's plain FlexBool field
// round-trips canonically.
func TestSaveConfig_NilTriStateFieldsAreOmitted(t *testing.T) {
	type triState struct {
		Ptr *FlexBool `json:"ptr,omitempty"`
	}
	data, err := json.Marshal(&triState{})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(data), "ptr") || strings.Contains(string(data), "null") {
		t.Fatalf("nil tri-state field must be omitted from the saved config, got %s", data)
	}

	// End-to-end: the branch's FlexBool field saves canonically (no null
	// entries) and reloads under the strict parser.
	configRoot := t.TempDir()
	setUserConfigEnv(t, configRoot)
	if _, err := LoadConfig(); err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	loaded, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	loaded.SaveSubagentHistories = FlexBool(true)
	if err := SaveConfig(loaded); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	raw, err := os.ReadFile(lateConfigPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"save_subagent_histories": true`) {
		t.Fatalf("saved config %s must keep the plain-true entry", raw)
	}
	if strings.Contains(string(raw), "null") {
		t.Fatalf("saved config %s must not contain null entries", raw)
	}
	reloaded, err := LoadConfig()
	if err != nil {
		t.Fatalf("strict reload failed: %v", err)
	}
	if !reloaded.SaveSubagentHistories.Bool() {
		t.Fatal("reloaded save_subagent_histories must be true")
	}
}
