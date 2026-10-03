package config

import "testing"

// TestResolveCompactionScoreThreshold is the resolver table for the elision
// score threshold knob: precedence is explicit flag > config.json entry >
// the 0.35 default; anything out of (0,1] warns and falls back to the next
// source. flagValue = 0 models "flag not explicitly passed" (the flag.Visit
// sentinel main() uses — an explicit 0 is itself invalid, so the sentinel
// and the degenerate case resolve to the same fallback).
func TestResolveCompactionScoreThreshold(t *testing.T) {
	cases := []struct {
		name        string
		cfg         *Config
		flagValue   float64
		want        float64
		wantWarning []string
	}{
		// Default tier.
		{
			name:      "nil config, no flag, default",
			cfg:       nil,
			flagValue: 0,
			want:      DefaultCompactionThreshold,
		},
		{
			name:      "unset config, no flag, default",
			cfg:       &Config{},
			flagValue: 0,
			want:      DefaultCompactionThreshold,
		},

		// Config tier.
		{
			name:      "config wins when the flag is not passed",
			cfg:       &Config{CompactionThreshold: 0.65},
			flagValue: 0,
			want:      0.65,
		},
		{
			name:      "config 1.0 is valid",
			cfg:       &Config{CompactionThreshold: 1},
			flagValue: 0,
			want:      1,
		},
		{
			name:        "config over 1 invalid, warns, default",
			cfg:         &Config{CompactionThreshold: 1.5},
			flagValue:   0,
			want:        DefaultCompactionThreshold,
			wantWarning: []string{"invalid", "config.json", "1.5"},
		},
		{
			name:        "config negative invalid, warns, default",
			cfg:         &Config{CompactionThreshold: -0.2},
			flagValue:   0,
			want:        DefaultCompactionThreshold,
			wantWarning: []string{"invalid", "config.json", "-0.2"},
		},

		// Flag tier.
		{
			name:      "explicit flag wins over config",
			cfg:       &Config{CompactionThreshold: 0.65},
			flagValue: 0.8,
			want:      0.8,
		},
		{
			name:      "explicit flag wins over the default",
			cfg:       &Config{},
			flagValue: 0.5,
			want:      0.5,
		},
		{
			name:        "explicit flag over 1 warns and falls to config",
			cfg:         &Config{CompactionThreshold: 0.65},
			flagValue:   2,
			want:        0.65,
			wantWarning: []string{"invalid", "-compaction-threshold", "2"},
		},
		{
			name:        "explicit flag over 1 with no config falls to default",
			cfg:         &Config{},
			flagValue:   2,
			want:        DefaultCompactionThreshold,
			wantWarning: []string{"invalid", "-compaction-threshold", "2"},
		},
		{
			name:        "explicit negative flag warns and falls to default",
			cfg:         &Config{},
			flagValue:   -1,
			want:        DefaultCompactionThreshold,
			wantWarning: []string{"invalid", "-compaction-threshold", "-1"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, warning := ResolveCompactionScoreThreshold(tc.cfg, tc.flagValue)
			if got != tc.want {
				t.Fatalf("ResolveCompactionScoreThreshold(%v, %v) = %v, want %v", tc.cfg, tc.flagValue, got, tc.want)
			}
			assertResolverWarning(t, warning, tc.wantWarning)
		})
	}
}
