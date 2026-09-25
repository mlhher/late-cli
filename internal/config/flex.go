package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// FlexBool is a config.json boolean that accepts the everyday on/off
// synonyms in addition to the JSON literals true/false:
//
//	true side:  true, on, enabled, enable, active, activated, yes, y, 1
//	false side: false, off, disabled, disable, no, not, n, 0
//
// Synonyms are case-insensitive and tolerate surrounding whitespace
// ("  On  " is true); the JSON numbers 1 and 0 (and the strings "1"/"0")
// are accepted too. Anything else is a *ConfigParseError listing the
// accepted synonyms. MarshalJSON always emits the plain true/false
// literals, so SaveConfig round-trips a synonym back to canonical form.
type FlexBool bool

// flexBoolSynonyms is the accepted synonym table, keyed by the value each
// side denotes. The slice order is also the order used in error messages.
var flexBoolSynonyms = map[bool][]string{
	true:  {"true", "on", "enabled", "enable", "active", "activated", "yes", "y", "1"},
	false: {"false", "off", "disabled", "disable", "no", "not", "n", "0"},
}

// flexBoolLookup is the trimmed/lowercased token → value lookup built once
// from flexBoolSynonyms.
var flexBoolLookup = func() map[string]bool {
	lookup := make(map[string]bool, len(flexBoolSynonyms[true])+len(flexBoolSynonyms[false]))
	for value, synonyms := range flexBoolSynonyms {
		for _, synonym := range synonyms {
			lookup[synonym] = value
		}
	}
	return lookup
}()

// flexBoolAcceptedValues renders the synonym table for error messages.
func flexBoolAcceptedValues() string {
	return fmt.Sprintf("%s (true) or %s (false)",
		strings.Join(flexBoolSynonyms[true], ", "),
		strings.Join(flexBoolSynonyms[false], ", "))
}

// UnmarshalJSON implements json.Unmarshaler. It accepts a JSON boolean, the
// synonym strings of flexBoolSynonyms (trimmed, case-insensitive), and the
// JSON numbers 1/0; null leaves the field at its zero value (the
// encoding/json convention for Unmarshaler types). Anything else — including
// an unknown synonym like "actve" — returns a *ConfigParseError carrying a
// byte offset (relative to the value it was given) so the caller can render
// line/column, with a message listing the accepted synonyms.
func (b *FlexBool) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	value, ok := parseFlexBoolValue(data)
	if !ok {
		return newConfigParseError("", data, 0,
			"%s is not a valid boolean value. Accepted values are: %s; case-insensitive, surrounding whitespace allowed",
			quoteJSONToken(data), flexBoolAcceptedValues())
	}
	*b = FlexBool(value)
	return nil
}

// MarshalJSON emits the plain true/false literals so a saved config.json
// never carries a synonym back to disk.
func (b FlexBool) MarshalJSON() ([]byte, error) {
	if b {
		return []byte("true"), nil
	}
	return []byte("false"), nil
}

// Bool converts FlexBool to a plain bool. It is the supported way to read a
// FlexBool at the resolvers' boundary (Go does not auto-convert named bool
// types in expressions like `!cfg.Field`).
func (b FlexBool) Bool() bool { return bool(b) }

// parseFlexBoolValue parses one JSON token as a FlexBool. It reports ok=false
// (without an error) for anything outside the accepted table; the caller
// owns the error presentation.
func parseFlexBoolValue(data []byte) (value, ok bool) {
	raw := strings.TrimSpace(string(data))

	// A quoted string holds a synonym token (trimmed, case-insensitive).
	if strings.HasPrefix(raw, `"`) {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			// Malformed string token (LoadConfig's syntax pass rejects the
			// document before this point; standalone callers get the
			// invalid-boolean message).
			return false, false
		}
		if value, ok := flexBoolLookup[strings.ToLower(strings.TrimSpace(s))]; ok {
			return value, true
		}
		return false, false
	}

	// A JSON number: 1 = true, 0 = false (1.0/0.0 spellings included).
	if raw != "" && (raw[0] == '-' || (raw[0] >= '0' && raw[0] <= '9')) {
		number, err := parseJSONNumber(raw)
		if err != nil || (number != 0 && number != 1) {
			return false, false
		}
		return number == 1, true
	}

	// Bare literals and nothing else.
	switch raw {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

// parseJSONNumber parses a JSON number token to a float64. Any token that
// reached here came from valid JSON, so the strconv error only guards the
// standalone-caller case.
func parseJSONNumber(raw string) (float64, error) {
	return strconv.ParseFloat(raw, 64)
}

// quoteJSONToken renders a raw JSON token for an error message, truncating
// oversized values rune-safely.
func quoteJSONToken(data []byte) string {
	token := strings.TrimSpace(string(data))
	const maxTokenRunes = 64
	runes := []rune(token)
	if len(runes) > maxTokenRunes {
		token = string(runes[:maxTokenRunes]) + "…"
	}
	return token
}

// flexPtr returns a *FlexBool pointing at v — the tri-state "explicitly set"
// helper used by tests and in-package construction.
func flexPtr(v bool) *FlexBool {
	value := FlexBool(v)
	return &value
}
