package ast

import (
	"encoding/json"
	"testing"
)

func TestMarshalUnmarshalIR_RoundTrip(t *testing.T) {
	original := ParsedIR{
		Version:     IRVersion,
		Platform:    PlatformUnix,
		Commands:    []string{"ls", "grep"},
		Operators:   []string{"|"},
		Redirects:   []string{},
		Expansions:  []string{},
		RiskFlags:   []ReasonCode{ReasonOperator},
		ParseErrors: []string{},
	}

	data, err := MarshalIR(original)
	if err != nil {
		t.Fatalf("MarshalIR: %v", err)
	}

	got, err := UnmarshalIR(data)
	if err != nil {
		t.Fatalf("UnmarshalIR: %v", err)
	}

	if got.Version != original.Version {
		t.Errorf("Version: got %q want %q", got.Version, original.Version)
	}
	if got.Platform != original.Platform {
		t.Errorf("Platform: got %q want %q", got.Platform, original.Platform)
	}
	if len(got.Commands) != len(original.Commands) {
		t.Errorf("Commands len: got %d want %d", len(got.Commands), len(original.Commands))
	}
}

func TestUnmarshalIR_VersionMismatch(t *testing.T) {
	bad := map[string]any{
		"version":  "99",
		"platform": "unix",
	}
	data, _ := json.Marshal(bad)
	if _, err := UnmarshalIR(data); err == nil {
		t.Error("expected error for version mismatch, got nil")
	}
}

func TestUnmarshalIR_MalformedJSON(t *testing.T) {
	if _, err := UnmarshalIR([]byte("{not valid json")); err == nil {
		t.Error("expected error for malformed JSON, got nil")
	}
}

func TestEmptyIR(t *testing.T) {
	ir := emptyIR(PlatformWindows)
	if ir.Commands == nil {
		t.Error("Commands must not be nil")
	}
	if ir.Operators == nil {
		t.Error("Operators must not be nil")
	}
	if ir.RiskFlags == nil {
		t.Error("RiskFlags must not be nil")
	}
	if ir.Version != IRVersion {
		t.Errorf("Version: got %q want %q", ir.Version, IRVersion)
	}
}
