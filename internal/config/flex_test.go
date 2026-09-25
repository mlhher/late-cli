package config

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// TestFlexBoolUnmarshalSynonymMatrix pins the full R1 synonym table: every
// true-side and false-side synonym parses, in mixed case, with surrounding
// whitespace, as a JSON string; plus the JSON numbers 1/0 and the strings
// "1"/"0".
func TestFlexBoolUnmarshalSynonymMatrix(t *testing.T) {
	trueSynonyms := []string{"true", "on", "enabled", "enable", "active", "activated", "yes", "y", "1"}
	falseSynonyms := []string{"false", "off", "disabled", "disable", "no", "not", "n", "0"}

	cases := []struct {
		name string
		raw  string
		want bool
	}{
		// JSON booleans.
		{name: "json true", raw: `true`, want: true},
		{name: "json false", raw: `false`, want: false},
		// JSON numbers 1/0.
		{name: "json number 1", raw: `1`, want: true},
		{name: "json number 0", raw: `0`, want: false},
		{name: "json number 1.0", raw: `1.0`, want: true},
		{name: "json number 0.0", raw: `0.0`, want: false},
		// JSON null leaves the zero value.
		{name: "json null", raw: `null`, want: false},
	}

	for _, synonym := range trueSynonyms {
		cases = append(cases,
			struct {
				name, raw string
				want      bool
			}{name: "true synonym " + synonym, raw: strconv.Quote(synonym), want: true},
			struct {
				name, raw string
				want      bool
			}{name: "true synonym UPPER " + synonym, raw: strconv.Quote(strings.ToUpper(synonym)), want: true},
			struct {
				name, raw string
				want      bool
			}{name: "true synonym mixed case " + synonym, raw: mixedCase(synonym), want: true},
			struct {
				name, raw string
				want      bool
			}{name: "true synonym padded " + synonym, raw: strconv.Quote("  " + synonym + "\t"), want: true},
		)
	}
	for _, synonym := range falseSynonyms {
		cases = append(cases,
			struct {
				name, raw string
				want      bool
			}{name: "false synonym " + synonym, raw: strconv.Quote(synonym), want: false},
			struct {
				name, raw string
				want      bool
			}{name: "false synonym UPPER " + synonym, raw: strconv.Quote(strings.ToUpper(synonym)), want: false},
			struct {
				name, raw string
				want      bool
			}{name: "false synonym mixed case " + synonym, raw: mixedCase(synonym), want: false},
			struct {
				name, raw string
				want      bool
			}{name: "false synonym padded " + synonym, raw: strconv.Quote("\t" + synonym + "  "), want: false},
		)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var payload struct {
				Value FlexBool `json:"value"`
			}
			if err := json.Unmarshal([]byte(`{"value":`+tc.raw+`}`), &payload); err != nil {
				t.Fatalf("Unmarshal(%s) error = %v", tc.raw, err)
			}
			if payload.Value.Bool() != tc.want {
				t.Fatalf("Unmarshal(%s) = %v, want %v", tc.raw, payload.Value.Bool(), tc.want)
			}
		})
	}
}

// mixedCase builds an alternating-case variant of s to pin
// case-insensitivity beyond plain ToUpper.
func mixedCase(s string) string {
	runes := []rune(s)
	for i := range runes {
		if i%2 == 0 {
			runes[i] = []rune(strings.ToUpper(string(runes[i])))[0]
		} else {
			runes[i] = []rune(strings.ToLower(string(runes[i])))[0]
		}
	}
	return strconv.Quote(string(runes))
}

// TestFlexBoolUnmarshalRejectsUnknown pins that anything outside the
// synonym table — an unknown synonym like "actve", other numbers, arrays,
// objects — is an error whose message lists the accepted synonyms.
func TestFlexBoolUnmarshalRejectsUnknown(t *testing.T) {
	for _, raw := range []string{`"actve"`, `"maybe"`, `"truee"`, `"2"`, `2`, `0.5`, `-1`, `[1]`, `{"a":1}`, `""`, `" "`} {
		t.Run(raw, func(t *testing.T) {
			var payload struct {
				Value FlexBool `json:"value"`
			}
			err := json.Unmarshal([]byte(`{"value":`+raw+`}`), &payload)
			if err == nil {
				t.Fatalf("Unmarshal(%s) = %v, want an error", raw, payload.Value.Bool())
			}
			var parseErr *ConfigParseError
			if !asConfigParseError(err, &parseErr) {
				t.Fatalf("Unmarshal(%s) error = %T, want *ConfigParseError", raw, err)
			}
			for _, synonym := range []string{"on", "enabled", "yes", "off", "not"} {
				if !strings.Contains(err.Error(), synonym) {
					t.Fatalf("error %q does not list accepted synonym %q", err.Error(), synonym)
				}
			}
		})
	}
}

// asConfigParseError unwraps through the errors chain (a FlexBool error
// surfaces directly or wrapped by encoding/json containers).
func asConfigParseError(err error, target **ConfigParseError) bool {
	for err != nil {
		if perr, ok := err.(*ConfigParseError); ok {
			*target = perr
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// TestFlexBoolMarshalEmitsPlainBooleans pins that MarshalJSON always emits
// the true/false literals so SaveConfig round-trips a synonym back to
// canonical form.
func TestFlexBoolMarshalEmitsPlainBooleans(t *testing.T) {
	data, err := json.Marshal(struct {
		On  FlexBool `json:"on"`
		Off FlexBool `json:"off"`
	}{On: FlexBool(true), Off: FlexBool(false)})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if got := string(data); got != `{"on":true,"off":false}` {
		t.Fatalf("Marshal() = %s, want {\"on\":true,\"off\":false}", got)
	}
}

// TestFlexBoolOmitAndTriState pins the JSON surface of FlexBool-typed
// fields: a plain FlexBool false is omitted (omitempty), an explicit-false
// *FlexBool is kept, and an absent *FlexBool stays nil. The shape is
// exercised on a test-local struct so the contract holds for any config
// field using these types.
func TestFlexBoolOmitAndTriState(t *testing.T) {
	type triState struct {
		Plain FlexBool  `json:"plain,omitempty"`
		Ptr   *FlexBool `json:"ptr,omitempty"`
	}
	data, err := json.Marshal(&triState{
		Plain: FlexBool(false), // plain false: omitted by omitempty
		Ptr:   flexPtr(false),  // explicit false pointer: kept
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	out := string(data)
	if strings.Contains(out, "plain") {
		t.Fatalf("plain false FlexBool must be omitted, got %s", out)
	}
	if !strings.Contains(out, `"ptr":false`) {
		t.Fatalf("explicit false *FlexBool must marshal as false, got %s", out)
	}

	// Tri-state: the synonym "off" lands as an explicit false pointer.
	var cfg triState
	if err := json.Unmarshal([]byte(`{"ptr":"off"}`), &cfg); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if cfg.Ptr == nil || cfg.Ptr.Bool() {
		t.Fatalf(`ptr "off" = %#v, want explicit false`, cfg.Ptr)
	}

	var absent triState
	if err := json.Unmarshal([]byte(`{}`), &absent); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if absent.Ptr != nil {
		t.Fatalf("absent entry = %#v, want nil", absent.Ptr)
	}
}

// TestFlexBoolErrorCarriesOffset pins that a standalone FlexBool decode
// error is a *ConfigParseError carrying a byte offset and the accepted
// synonyms message, even without file context.
func TestFlexBoolErrorCarriesOffset(t *testing.T) {
	var fb FlexBool
	err := json.Unmarshal([]byte(`"actve"`), &fb)
	if err == nil {
		t.Fatal("expected an error for the unknown synonym \"actve\"")
	}
	var parseErr *ConfigParseError
	if !asConfigParseError(err, &parseErr) {
		t.Fatalf("error = %T, want *ConfigParseError", err)
	}
	if parseErr.Offset != 0 {
		t.Fatalf("Offset = %d, want 0 (the value's own start)", parseErr.Offset)
	}
	if !strings.Contains(err.Error(), "not a valid boolean value") || !strings.Contains(err.Error(), "Accepted values are:") {
		t.Fatalf("error = %q, want the accepted-synonyms message", err.Error())
	}
}
