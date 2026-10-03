package config

import (
	"strings"
	"testing"
)

// TestResolveCompactionBackend pins the Step 18 resolver: unset (or nil)
// config means "resolve from the environment as before", the "offline" value
// selects the scripted scorer, and an invalid value warns (naming the config
// key) while falling back to the env-based resolution. Precedence: a set
// config value WINS over the environment — JEV_API / auto-detection are only
// consulted when the entry is absent.
func TestResolveCompactionBackend(t *testing.T) {
	cases := []struct {
		name        string
		cfg         *Config
		wantBackend string
		wantWarning bool
	}{
		{"nil config", nil, "", false},
		{"zero config", &Config{}, "", false},
		{"explicit empty", &Config{CompactionBackend: ""}, "", false},
		{"offline", &Config{CompactionBackend: CompactionBackendOffline}, CompactionBackendOffline, false},
		{"invalid value", &Config{CompactionBackend: "typesafe"}, "", true},
		{"wrong case", &Config{CompactionBackend: "Offline"}, "", true},
		{"garbage", &Config{CompactionBackend: "bogus"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend, warning := ResolveCompactionBackend(tc.cfg)
			if backend != tc.wantBackend {
				t.Errorf("ResolveCompactionBackend() backend = %q, want %q", backend, tc.wantBackend)
			}
			if (warning != "") != tc.wantWarning {
				t.Errorf("ResolveCompactionBackend() warning = %q, wantWarning = %v", warning, tc.wantWarning)
			}
			if tc.wantWarning {
				if !strings.Contains(warning, "compaction-backend") {
					t.Errorf("warning %q does not name the config key", warning)
				}
				if !strings.Contains(warning, CompactionBackendOffline) {
					t.Errorf("warning %q does not name the only valid value", warning)
				}
			}
		})
	}
}

// TestIsValidCompactionBackend pins the accepted set: exactly "offline"
// (strict equality — compaction-mode validates the same way).
func TestIsValidCompactionBackend(t *testing.T) {
	if !IsValidCompactionBackend(CompactionBackendOffline) {
		t.Errorf("IsValidCompactionBackend(%q) = false, want true", CompactionBackendOffline)
	}
	for _, invalid := range []string{"", "gateway", "OFFLINE", " offline", "scripted"} {
		if IsValidCompactionBackend(invalid) {
			t.Errorf("IsValidCompactionBackend(%q) = true, want false", invalid)
		}
	}
}
