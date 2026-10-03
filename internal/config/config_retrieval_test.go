package config

import (
	"strings"
	"testing"
)

// TestResolveCompactionRetrieval pins the Step 17 resolver: default false,
// honored when set, and a warning for the invalid COMBINATION — retrieval
// enabled while compaction-mode is not "enabled" (the record store only
// fills when relocation runs, so retrieval could never inject anything).
func TestResolveCompactionRetrieval(t *testing.T) {
	cases := []struct {
		name        string
		cfg         *Config
		wantEnabled bool
		wantWarning bool
	}{
		{"nil config", nil, false, false},
		{"zero config", &Config{}, false, false},
		{"explicit false", &Config{CompactionRetrieval: false, CompactionMode: CompactionModeEnabled}, false, false},
		{"enabled mode", &Config{CompactionRetrieval: true, CompactionMode: CompactionModeEnabled}, true, false},
		{"off mode warns", &Config{CompactionRetrieval: true, CompactionMode: CompactionModeOff}, true, true},
		{"shadow mode warns", &Config{CompactionRetrieval: true, CompactionMode: CompactionModeShadow}, true, true},
		{"unset mode (shadow default) warns", &Config{CompactionRetrieval: true}, true, true},
		{"invalid mode falls back to shadow and warns", &Config{CompactionRetrieval: true, CompactionMode: "bogus"}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enabled, warning := ResolveCompactionRetrieval(tc.cfg)
			if enabled != tc.wantEnabled {
				t.Errorf("ResolveCompactionRetrieval() enabled = %v, want %v", enabled, tc.wantEnabled)
			}
			if (warning != "") != tc.wantWarning {
				t.Errorf("ResolveCompactionRetrieval() warning = %q, wantWarning = %v", warning, tc.wantWarning)
			}
			if tc.wantWarning {
				if !strings.Contains(warning, "compaction-retrieval") {
					t.Errorf("warning %q does not name the config key", warning)
				}
				if !strings.Contains(warning, CompactionModeEnabled) {
					t.Errorf("warning %q does not name the mode that makes retrieval effective", warning)
				}
			}
		})
	}
}
