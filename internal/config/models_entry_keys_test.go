package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// lineOf returns the 1-based line of the first occurrence of needle in
// content (mirrors the positioned-error tests' expectations: the walk reports
// the key's editor line).
func lineOf(content, needle string) int {
	byteIdx := strings.Index(content, needle)
	if byteIdx < 0 {
		return -1
	}
	return strings.Count(content[:byteIdx], "\n") + 1
}

// runeColumn returns the 1-based rune column of the first occurrence of
// needle in content (columns count runes, so UTF-8 content reports the
// position an editor shows).
func runeColumn(content, needle string) int {
	byteIdx := strings.Index(content, needle)
	if byteIdx < 0 {
		return -1
	}
	lineStart := strings.LastIndex(content[:byteIdx], "\n") + 1
	return utf8.RuneCountInString(content[lineStart:byteIdx]) + 1
}

// parseForModelsWalk is the test entry point for the models[] key walk: the
// typed decode plus the walk, the same pair LoadConfig runs.
func parseForModelsWalk(t *testing.T, path, content string) (*Config, error) {
	t.Helper()
	var cfg Config
	if err := json.Unmarshal([]byte(content), &cfg); err != nil {
		return nil, err
	}
	return &cfg, checkModelsEntryKeys(path, []byte(content))
}

// TestModelsEntryKeyUnknownIsRejected pins the models[] walk: an unknown key
// inside an entry is a located did-you-mean error naming the entry — the
// strictness the plain decode cannot provide. "urll" is a realistic typo of
// the known entry key "url".
func TestModelsEntryKeyUnknownIsRejected(t *testing.T) {
	content := `{"models": [{"url": "http://p:8080", "key": "k", "model": "m", "urll": "typo"}]}`
	_, err := parseForModelsWalk(t, "/x/config.json", content)
	if err == nil {
		t.Fatal("expected an unknown nested entry error")
	}
	want := `error in /x/config.json at line 1, column ` + strconv.Itoa(runeColumn(content, `"urll"`)) +
		`: models[m] entry "urll" is not a valid entry key. Did you mean "url"?`
	if err.Error() != want {
		t.Fatalf("error = %q\nwant  %q", err.Error(), want)
	}
}

// TestModelsEntryKeyExactMessage pins the multi-line rendering: the reported
// line/column is the unknown key's position in the user's editor, and the
// entry is named by its id.
func TestModelsEntryKeyExactMessage(t *testing.T) {
	content := `{
  "models": [
    {
      "id": "local",
      "urll": "http://localhost:8080",
      "key": "",
      "model": "qwen3.6-35b-a3b"
    }
  ]
}`
	_, err := parseForModelsWalk(t, "/Users/u/config.json", content)
	if err == nil {
		t.Fatalf("parseForModelsWalk() = _, nil, want an unknown models-entry-key error")
	}
	want := `error in /Users/u/config.json at line ` + strconv.Itoa(lineOf(content, `"urll"`)) +
		`, column ` + strconv.Itoa(runeColumn(content, `"urll"`)) +
		`: models[local] entry "urll" is not a valid entry key. Did you mean "url"?`
	if err.Error() != want {
		t.Fatalf("error = %q\nwant  %q", err.Error(), want)
	}
}

// TestModelsEntryKeySuggestsAutocompactKey pins the underscore typo of the
// per-model key: the walk knows "jev-autocompact-percent" (and only that
// spelling), so a hand-edited "jev-autocompact_percent" inside an entry
// suggests the kebab-case key.
func TestModelsEntryKeySuggestsAutocompactKey(t *testing.T) {
	content := `{
  "models": [
    {
      "id": "frontier",
      "url": "https://api.deepseek.com",
      "key": "sk-x",
      "model": "deepseek-flash",
      "jev-autocompact_percent": 55
    }
  ]
}`
	_, err := parseForModelsWalk(t, "/x/config.json", content)
	if err == nil {
		t.Fatal("expected an unknown models-entry-key error")
	}
	want := `error in /x/config.json at line ` + strconv.Itoa(lineOf(content, `"jev-autocompact_percent"`)) +
		`, column ` + strconv.Itoa(runeColumn(content, `"jev-autocompact_percent"`)) +
		`: models[frontier] entry "jev-autocompact_percent" is not a valid entry key. Did you mean "jev-autocompact-percent"?`
	if err.Error() != want {
		t.Fatalf("error = %q\nwant  %q", err.Error(), want)
	}
}

// TestModelsEntryKeyNoSuggestionListsValidKeys pins the no-reasonable-
// suggestion branch of the walk: the full sorted valid-entry-keys list,
// still positioned at the offending key and naming the entry.
func TestModelsEntryKeyNoSuggestionListsValidKeys(t *testing.T) {
	content := `{"models": [{"id": "prov", "zzzzzzz": 1}]}`
	_, err := parseForModelsWalk(t, "/x/config.json", content)
	if err == nil {
		t.Fatal("expected an unknown models-entry-key error")
	}
	want := `error in /x/config.json at line ` + strconv.Itoa(lineOf(content, `"zzzzzzz"`)) +
		`, column ` + strconv.Itoa(runeColumn(content, `"zzzzzzz"`)) +
		`: models[prov] entry "zzzzzzz" is not a valid entry key. Valid entry keys are: id, jev-autocompact-percent, key, model, url`
	if err.Error() != want {
		t.Fatalf("error = %q\nwant  %q", err.Error(), want)
	}
}

// TestModelsEntryKeysAccepted pins the happy path of the walk: every known
// entry key — including the per-model jev-autocompact-percent override —
// parses in every entry, and the values survive the decode. (Out-of-range
// VALUES keep the warning path; strictness here covers key names only.)
func TestModelsEntryKeysAccepted(t *testing.T) {
	content := `{
  "models": [
    {"id": "a", "url": "http://a:8080", "key": "ka", "model": "ma", "jev-autocompact-percent": 55},
    {"id": "b", "url": "http://b:8080", "key": "", "model": "mb"}
  ]
}`
	cfg, err := parseForModelsWalk(t, "/x/config.json", content)
	if err != nil {
		t.Fatalf("parseForModelsWalk() error = %v", err)
	}
	if len(cfg.Models) != 2 {
		t.Fatalf("models = %#v, want 2 entries", cfg.Models)
	}
	if cfg.Models[0].JevAutocompactPercent != 55 {
		t.Fatalf("per-model override did not survive: %#v", cfg.Models[0])
	}
	if got, ok := cfg.Models[0].AutocompactPercentOverride(); !ok || got != 55 {
		t.Fatalf("AutocompactPercentOverride() = (%d, %v), want (55, true)", got, ok)
	}
	if _, ok := cfg.Models[1].AutocompactPercentOverride(); ok {
		t.Fatal("an entry without the key must not report an override")
	}
}

// TestCheckModelsEntryKeysToleratesNonArrayModels pins the boundary: a
// non-array models value is the typed decode's error, not the walk's; the
// walk must not crash on it (LoadConfig reports the decode error first).
func TestCheckModelsEntryKeysToleratesNonArrayModels(t *testing.T) {
	if err := checkModelsEntryKeys("/x/config.json", []byte(`{"models": {"id": "x"}}`)); err != nil {
		t.Fatalf("non-array models must not be the walk's error, got %v", err)
	}
	if err := checkModelsEntryKeys("/x/config.json", []byte(`{"models": null}`)); err != nil {
		t.Fatalf("null models must not error, got %v", err)
	}
	if err := checkModelsEntryKeys("/x/config.json", []byte(`{"other": 1}`)); err != nil {
		t.Fatalf("no models key must not error, got %v", err)
	}
	if err := checkModelsEntryKeys("/x/config.json", []byte(`not json at all`)); err != nil {
		t.Fatalf("syntax errors are the typed decode's job, got %v", err)
	}
}

// TestLoadConfigRejectsUnknownModelsEntryKey pins the end-to-end wiring:
// LoadConfig surfaces the walk's located error and a fallback config (the
// same shape as a decode error).
func TestLoadConfigRejectsUnknownModelsEntryKey(t *testing.T) {
	configRoot := t.TempDir()
	setUserConfigEnv(t, configRoot)
	configPath := lateConfigPath(t)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("MkdirAll(config dir) error = %v", err)
	}
	content := `{"models": [{"id": "a", "url": "http://a:8080", "key": "", "model": "ma", "jev-autocompact_percent": 55}]}`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(config.json) error = %v", err)
	}

	cfg, err := LoadConfig()
	if err == nil {
		t.Fatalf("LoadConfig() = %#v, want an unknown models-entry-key error", cfg)
	}
	if cfg == nil {
		t.Fatal("expected a fallback config alongside the error")
	}
	if !strings.Contains(err.Error(), `entry "jev-autocompact_percent" is not a valid entry key`) ||
		!strings.Contains(err.Error(), `Did you mean "jev-autocompact-percent"?`) {
		t.Fatalf("error = %q, want the located did-you-mean message", err.Error())
	}
}
