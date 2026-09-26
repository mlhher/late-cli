package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestInitialBootstrapStatus guards the pre-program status-bar decision:
// a failed app-config load surfaces as a "config error: ..." warning (Step 1
// wraps the error with the exact config path, so the path must survive
// verbatim), while a clean load returns "" so main() falls back to the
// plain "Starting..." text.
func TestInitialBootstrapStatus(t *testing.T) {
	tests := []struct {
		name    string
		loadErr error
		want    string
	}{
		{
			name:    "clean load defers to the plain startup status",
			loadErr: nil,
			want:    "",
		},
		{
			name:    "load error becomes a config error warning",
			loadErr: errors.New("/Users/u/Library/Application Support/late/config.json: trailing comma at line 3"),
			want:    "config error: /Users/u/Library/Application Support/late/config.json: trailing comma at line 3",
		},
		{
			name:    "wrapped error keeps its full cause text",
			loadErr: fmt.Errorf("reading config %s: %w", "/Users/u/Library/Application Support/late/config.json", errors.New("invalid character '}' looking for beginning of value")),
			want:    "config error: reading config /Users/u/Library/Application Support/late/config.json: invalid character '}' looking for beginning of value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := initialBootstrapStatus(tt.loadErr)
			if got != tt.want {
				t.Fatalf("initialBootstrapStatus(%v) = %q, want %q", tt.loadErr, got, tt.want)
			}
			if tt.loadErr != nil {
				if !strings.HasPrefix(got, "config error: ") {
					t.Fatalf("warning must be prefixed with %q, got %q", "config error: ", got)
				}
				if !strings.Contains(got, "config.json") {
					t.Fatalf("warning must retain the config path, got %q", got)
				}
			}
		})
	}
}
