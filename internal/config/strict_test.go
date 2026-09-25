package config

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestLineCol pins the byte-offset → line/column helper: 1-based lines
// counting '\n', 1-based columns counting runes since the line start, and
// clamping past the end. Content bytes: '{'=0, '\n'=1, two spaces=2-3,
// `"a"`=4-6, ':'=7, ' '=8, '"..."'=9..18 with each 🚀 spanning 4 bytes
// (10-13 and 14-17), '\n'=19, '}'=20.
func TestLineCol(t *testing.T) {
	content := []byte("{\n  \"a\": \"🚀🚀\"\n}")
	cases := []struct {
		offset   int
		wantLine int
		wantCol  int
	}{
		{offset: 0, wantLine: 1, wantCol: 1},
		{offset: 1, wantLine: 1, wantCol: 2}, // the '\n' itself belongs to line 1
		{offset: 2, wantLine: 2, wantCol: 1},
		{offset: 5, wantLine: 2, wantCol: 4},  // 'a'
		{offset: 9, wantLine: 2, wantCol: 8},  // opening quote of the value
		{offset: 10, wantLine: 2, wantCol: 9}, // first 🚀 (4 bytes, 1 rune)
		{offset: 14, wantLine: 2, wantCol: 10},
		{offset: 18, wantLine: 2, wantCol: 11},
		{offset: 19, wantLine: 2, wantCol: 12},
		{offset: 20, wantLine: 3, wantCol: 1}, // '}'
		{offset: 21, wantLine: 3, wantCol: 2}, // clamped end
		{offset: 999, wantLine: 3, wantCol: 2},
	}
	for _, tc := range cases {
		line, col := lineCol(content, tc.offset)
		if line != tc.wantLine || col != tc.wantCol {
			t.Errorf("lineCol(%d) = (%d, %d), want (%d, %d)", tc.offset, line, col, tc.wantLine, tc.wantCol)
		}
	}
}

// runeColumn returns the 1-based rune column of the first occurrence of
// needle in content (used by the position assertions below).
func runeColumn(content, needle string) int {
	byteIdx := strings.Index(content, needle)
	if byteIdx < 0 {
		return -1
	}
	lineStart := strings.LastIndex(content[:byteIdx], "\n") + 1
	return utf8RuneCount(content[lineStart:byteIdx]) + 1
}

func utf8RuneCount(s string) int {
	count := 0
	for range s {
		count++
	}
	return count
}

// lineOf returns the 1-based line of the first occurrence of needle.
func lineOf(content, needle string) int {
	byteIdx := strings.Index(content, needle)
	if byteIdx < 0 {
		return -1
	}
	return strings.Count(content[:byteIdx], "\n") + 1
}

// TestParseConfigContent_SyntaxErrorLineCol pins R2: a JSON syntax error on
// a later line renders the exact line and column of the offending character
// and aborts the parse.
func TestParseConfigContent_SyntaxErrorLineCol(t *testing.T) {
	// The stray ']' sits on line 4; two leading spaces put it in column 3.
	content := "{\n" +
		"  \"theme\": \"late\",\n" +
		"  \"save_subagent_histories\": \"on\",\n" +
		"  \"bogus\"]\n" +
		"}"

	cfg, err := parseConfigContent("/Users/u/config.json", []byte(content))
	if err == nil {
		t.Fatalf("parseConfigContent() = %#v, want a syntax error", cfg)
	}
	if cfg != nil {
		t.Fatal("parseConfigContent() returned a config alongside the syntax error")
	}
	// The stray ']' ends line 4 (`  "bogus"]`): ten leading bytes put it in
	// column 11.
	want := `error in /Users/u/config.json at line 4, column 11: invalid character ']' after object key`
	if err.Error() != want {
		t.Fatalf("error = %q\nwant  %q", err.Error(), want)
	}
	var parseErr *ConfigParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("error = %T, want *ConfigParseError", err)
	}
}

// TestParseConfigContent_TrailingCommaLineCol pins the classic
// hand-editing mistake: a trailing comma in a multi-line object.
func TestParseConfigContent_TrailingCommaLineCol(t *testing.T) {
	content := "{\n" +
		"  \"theme\": \"late\",\n" +
		"  \"skills_dir\": \"/x\",\n" +
		"}"
	// encoding/json's scanner flags the trailing comma itself: the reported
	// offset is the character AFTER the comma (end of line 3), one column
	// past the comma's own position.
	_, err := parseConfigContent("/x/config.json", []byte(content))
	if err == nil {
		t.Fatal("expected a syntax error for the trailing comma")
	}
	commaCol := runeColumn(content, ",\n}")
	want := `error in /x/config.json at line ` + strconv.Itoa(lineOf(content, ",\n}")) +
		`, column ` + strconv.Itoa(commaCol+1) +
		`: invalid character ',' looking for beginning of value`
	if err.Error() != want {
		t.Fatalf("error = %q\nwant  %q", err.Error(), want)
	}
}

// TestParseConfigContent_EmptyFileIsPositioned pins that an empty config
// file reports a positioned error instead of a bare failure.
func TestParseConfigContent_EmptyFileIsPositioned(t *testing.T) {
	_, err := parseConfigContent("/x/config.json", nil)
	if err == nil {
		t.Fatal("expected an error for an empty config file")
	}
	want := "error in /x/config.json at line 1, column 1: unexpected end of JSON input"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

// TestParseConfigContent_TopLevelNotAnObject pins the guard for a
// syntactically valid document whose top level is not an object.
func TestParseConfigContent_TopLevelNotAnObject(t *testing.T) {
	for _, content := range []string{`[]`, `"late"`, `42`, `null`, `true`} {
		_, err := parseConfigContent("/x/config.json", []byte(content))
		if err == nil {
			t.Fatalf("parseConfigContent(%s) expected an error", content)
		}
		if !strings.Contains(err.Error(), "config.json must contain a JSON object") {
			t.Fatalf("parseConfigContent(%s) error = %q, want the top-level-object message", content, err.Error())
		}
	}
}

// TestParseConfigContent_UnknownKeyExactR3Message pins the R3 error text
// byte-for-byte: full path, exact line/column of the key, the entry name,
// and the closest known key as a Did-you-mean suggestion.
func TestParseConfigContent_UnknownKeyExactR3Message(t *testing.T) {
	content := "{\n  \"save_subagent_history\": true\n}\n"
	cfg, err := parseConfigContent("/Users/u/Library/Application Support/late/config.json", []byte(content))
	if err == nil {
		t.Fatalf("parseConfigContent() = %#v, want an unknown-key error", cfg)
	}
	want := `error in /Users/u/Library/Application Support/late/config.json at line 2, column 3: "save_subagent_history" is not a valid config.json entry. Did you mean "save_subagent_histories"?`
	if err.Error() != want {
		t.Fatalf("error = %q\nwant  %q", err.Error(), want)
	}
}

// TestParseConfigContent_UnknownKeyGibberishListsValidEntries pins the
// no-reasonable-suggestion branch: the full sorted known-key list.
func TestParseConfigContent_UnknownKeyGibberishListsValidEntries(t *testing.T) {
	content := `{"zzzzzzz": 1}`
	_, err := parseConfigContent("/x/config.json", []byte(content))
	if err == nil {
		t.Fatal("expected an unknown-key error")
	}
	prefix := `"zzzzzzz" is not a valid config.json entry. Valid entries are: `
	if !strings.Contains(err.Error(), prefix) {
		t.Fatalf("error = %q, want the valid-entries list", err.Error())
	}
	names := make([]string, 0, 48)
	for name := range knownConfigKeys() {
		names = append(names, name)
	}
	sort.Strings(names)
	if !strings.HasSuffix(err.Error(), strings.Join(names, ", ")) {
		t.Fatalf("error = %q, want it to end with the full sorted key list", err.Error())
	}
}

// TestParseConfigContent_UnknownKeySuggestionDistance pins the
// reasonably-similar threshold: distance ≤ 3 suggests, distance > 3 lists.
func TestParseConfigContent_UnknownKeySuggestionDistance(t *testing.T) {
	cases := []struct {
		key        string
		wantSugges string // empty: no suggestion expected
	}{
		{key: "save_subagent_historie", wantSugges: "save_subagent_histories"},
		{key: "permission_mode", wantSugges: "permission-mode"},
		{key: "SkillsDir", wantSugges: "skills_dir"},
		{key: "EnabledTools", wantSugges: "enabled_tools"},
		{key: "totally-unrelated-option", wantSugges: ""},
	}
	for _, tc := range cases {
		_, err := parseConfigContent("/x/config.json", []byte(`{"`+tc.key+`": 1}`))
		if err == nil {
			t.Fatalf("key %q: expected an unknown-key error", tc.key)
		}
		if tc.wantSugges == "" {
			if !strings.Contains(err.Error(), "Valid entries are: ") {
				t.Fatalf("key %q: error = %q, want the valid-entries list", tc.key, err.Error())
			}
			continue
		}
		want := `"` + tc.key + `" is not a valid config.json entry. Did you mean "` + tc.wantSugges + `"?`
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("key %q: error = %q, want it to contain %q", tc.key, err.Error(), want)
		}
	}
}

// TestParseConfigContent_PermissionModeEnum pins the permission-mode enum:
// the two known values parse; an unknown one errors at the value offset.
func TestParseConfigContent_PermissionModeEnum(t *testing.T) {
	for _, mode := range []string{PermissionModeAskForUserApproval, PermissionModeUnsupervised} {
		_, err := parseConfigContent("/x/config.json", []byte(`{"permission-mode":"`+mode+`"}`))
		if err != nil {
			t.Fatalf("permission-mode %q: unexpected error %v", mode, err)
		}
	}

	_, err := parseConfigContent("/x/config.json", []byte(`{"permission-mode":"yolo"}`))
	if err == nil {
		t.Fatal("expected an invalid permission-mode error")
	}
	if !strings.Contains(err.Error(), `"yolo" is not a valid permission-mode value`) {
		t.Fatalf("error = %q, want the R3 enum message", err.Error())
	}
	if !strings.Contains(err.Error(), "Valid values are: ask-for-user-approval") {
		t.Fatalf("error = %q, want the sorted valid-values list", err.Error())
	}
}

// TestParseConfigContent_TypeErrorLineCol pins R3 for wrong-typed values:
// the type error renders the field, the expected and found kinds, and the
// exact value position.
func TestParseConfigContent_TypeErrorLineCol(t *testing.T) {
	content := "{\n  \"theme\": \"late\",\n  \"enabled_tools\": \"yes\"\n}"
	_, err := parseConfigContent("/x/config.json", []byte(content))
	if err == nil {
		t.Fatal("expected a type error")
	}
	want := `error in /x/config.json at line ` + strconv.Itoa(lineOf(content, `"yes"`)) +
		`, column ` + strconv.Itoa(runeColumn(content, `"yes"`)) +
		`: "enabled_tools" must be an object, found a string`
	if err.Error() != want {
		t.Fatalf("error = %q\nwant  %q", err.Error(), want)
	}
}

// TestParseConfigContent_WrongTypedBoolean pins that a wrong-typed boolean
// entry reports the FlexBool synonyms message (not a raw type error).
func TestParseConfigContent_WrongTypedBoolean(t *testing.T) {
	content := `{"save_subagent_histories": [1, 2]}`
	_, err := parseConfigContent("/x/config.json", []byte(content))
	if err == nil {
		t.Fatal("expected an invalid boolean value error")
	}
	if !strings.Contains(err.Error(), `"save_subagent_histories"`) || !strings.Contains(err.Error(), "Accepted values are:") {
		t.Fatalf("error = %q, want the synonyms message naming the entry", err.Error())
	}
}

// TestParseConfigContent_FlexBoolSynonymErrorPositioned pins R3(1): an
// invalid boolean synonym surfaces at the value's offset with the entry
// name and the accepted synonyms.
func TestParseConfigContent_FlexBoolSynonymErrorPositioned(t *testing.T) {
	content := "{\n  \"save_subagent_histories\": \"actve\",\n  \"theme\": \"late\"\n}"
	_, err := parseConfigContent("/x/config.json", []byte(content))
	if err == nil {
		t.Fatal("expected an invalid boolean value error")
	}
	want := `error in /x/config.json at line ` + strconv.Itoa(lineOf(content, `"actve"`)) +
		`, column ` + strconv.Itoa(runeColumn(content, `"actve"`)) +
		`: "actve" is not a valid boolean value for "save_subagent_histories". Accepted values are: ` +
		flexBoolAcceptedValues() + `; case-insensitive, surrounding whitespace allowed`
	if err.Error() != want {
		t.Fatalf("error = %q\nwant  %q", err.Error(), want)
	}
}

// TestParseConfigContent_AllKeysWithSynonymsRoundTrip pins the full schema:
// a config using every current key — with synonym boolean values — parses
// strictly, re-marshals to plain true/false, and parses again cleanly.
func TestParseConfigContent_AllKeysWithSynonymsRoundTrip(t *testing.T) {
	content := `{
  "enabled_tools": {"bash": true, "read_file": false},
  "openai_base_url": "http://localhost:8080",
  "openai_api_key": "key",
  "openai_model": "model",
  "late_subagent_base_url": "http://localhost:8081",
  "late_subagent_api_key": "subkey",
  "late_subagent_model": "submodel",
  "save_subagent_histories": "on",
  "permission-mode": "ask-for-user-approval",
  "subagent_base_url": "http://legacy:8080",
  "subagent_api_key": "legacykey",
  "subagent_model": "legacy",
  "skills_dir": "/tmp/skills",
  "theme": "late",
  "models": [{"id": "prov", "url": "http://p:8080", "key": "k", "model": "m"}],
  "agent_models": {"orchestrator": "prov"}
}`
	cfg, err := parseConfigContent("/x/config.json", []byte(content))
	if err != nil {
		t.Fatalf("parseConfigContent() error = %v", err)
	}

	// Spot-check the synonym conversion.
	if !cfg.SaveSubagentHistories.Bool() {
		t.Fatalf(`save_subagent_histories "on" = %#v, want true`, cfg.SaveSubagentHistories)
	}

	// Re-marshal: every boolean entry must come back as a plain JSON
	// true/false literal (SaveConfig's canonical form), then parse
	// cleanly again.
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var canonical map[string]any
	if err := json.Unmarshal(data, &canonical); err != nil {
		t.Fatalf("re-Unmarshal() error = %v", err)
	}
	boolWant := map[string]bool{
		"save_subagent_histories": true,
	}
	for key, want := range boolWant {
		got, ok := canonical[key].(bool)
		if !ok {
			t.Fatalf("re-marshaled %s = %#v, want a plain %v literal (no synonyms)", key, canonical[key], want)
		}
		if got != want {
			t.Fatalf("re-marshaled %s = %v, want %v", key, got, want)
		}
	}
	if _, err := parseConfigContent("/x/config.json", data); err != nil {
		t.Fatalf("re-parsing the canonical config failed: %v\n%s", err, data)
	}
}

// TestParseConfigContent_UnknownNestedEntryIsRejected pins the
// DisallowUnknownFields backstop for nested sections (models entries).
func TestParseConfigContent_UnknownNestedEntryIsRejected(t *testing.T) {
	content := `{"models": [{"url": "http://p:8080", "key": "k", "model": "m", "urll": "typo"}]}`
	_, err := parseConfigContent("/x/config.json", []byte(content))
	if err == nil {
		t.Fatal("expected an unknown nested entry error")
	}
	if !strings.Contains(err.Error(), `"urll" is not a valid config.json entry`) {
		t.Fatalf("error = %q, want the nested unknown-entry message", err.Error())
	}
}

// TestSaveConfig_WritesPlainBooleans pins the SaveConfig side of the
// contract: FlexBool fields persist as true/false literals and the file
// reloads cleanly under the strict parser.
func TestSaveConfig_WritesPlainBooleans(t *testing.T) {
	configRoot := t.TempDir()
	setUserConfigEnv(t, configRoot)
	configPath := lateConfigPath(t)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.SaveSubagentHistories = FlexBool(true)
	if err := SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	if !strings.Contains(out, `"save_subagent_histories": true`) {
		t.Fatalf("saved config %s does not contain %q", out, `"save_subagent_histories": true`)
	}

	// The plain FlexBool false is omitted by omitempty (same as the old
	// plain-bool behavior).
	cfg.SaveSubagentHistories = FlexBool(false)
	if err := SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	raw, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "save_subagent_histories") {
		t.Fatalf("saved config %s must omit the zero-valued save_subagent_histories entry", raw)
	}

	reloaded, err := LoadConfig()
	if err != nil {
		t.Fatalf("strict reload of the saved config failed: %v", err)
	}
	if reloaded.SaveSubagentHistories.Bool() {
		t.Fatal("reloaded save_subagent_histories must be false")
	}
}

// TestParseConfigContent_MissingFileStillFreshInstall pins that strictness
// does not touch the missing-file path: defaults are written and the load
// succeeds.
func TestParseConfigContent_MissingFileStillFreshInstall(t *testing.T) {
	configRoot := t.TempDir()
	setUserConfigEnv(t, configRoot)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg == nil {
		t.Fatal("LoadConfig() returned nil config")
	}
	if !cfg.EnabledTools["bash"] {
		t.Fatalf("expected default enabled tools, got %#v", cfg.EnabledTools)
	}
	if _, err := os.Stat(lateConfigPath(t)); err != nil {
		t.Fatalf("expected the default config to be written: %v", err)
	}
}
