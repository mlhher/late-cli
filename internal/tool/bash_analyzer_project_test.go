package tool

import (
	"testing"
)

func TestBashAnalyzer_ProjectAllowListFlags(t *testing.T) {
	analyzer := &BashAnalyzer{
		ProjectAllowedCommands: map[string]map[string]bool{
			"git log": {
				"--oneline": true,
				"-*":        true, // Numeric wildcard
			},
			"pytest": {
				"--cov": true,
			},
			"go mod": {
				"tidy": true,
			},
			"go test": {
				"-v": true,
			},
		},
	}

	tests := []struct {
		desc          string
		command       string
		expectConfirm bool
	}{
		{"Allowed flag --oneline", "git log --oneline", false},
		{"Allowed numeric flag -20", "git log -20", false},
		{"Allowed numeric flag -5", "git log -5", false},
		{"Disallowed flag --output", "git log --output=pwned.txt", true},
		{"Disallowed flag --patch", "git log -p", true},
		{"Allowed flag --cov", "pytest --cov", false},
		{"Disallowed flag --pdb", "pytest --pdb", true},
		{"Positional arg is safe", "git log --oneline main.go", false},
		{"Multiple allowed flags", "git log --oneline -10", false},
		{"Compound allowed (&&)", "go mod tidy && go test -v ./...", false},
		{"Compound partial disallowed (||)", "go mod tidy || rm -rf /", true},
		{"Pipe allowed", "git log --oneline | head -n 5", false}, // head is in Tier 1
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			analysis := analyzer.Analyze(tc.command)
			if analysis.NeedsConfirmation != tc.expectConfirm {
				t.Errorf("%s: confirm mismatch: got %v, want %v", tc.desc, analysis.NeedsConfirmation, tc.expectConfirm)
			}
		})
	}
}

func TestParseCommandsForAllowList(t *testing.T) {
	tests := []struct {
		command string
		want    map[string][]string
	}{
		{
			"go mod tidy && go test -v ./...",
			map[string][]string{
				"go mod":  {"tidy"},
				"go test": {"-v"},
			},
		},
		{
			"git log --oneline --output=test.txt | grep foo",
			map[string][]string{
				"git log": {"--oneline", "--output"},
				"grep":    {},
			},
		},
	}

	for _, tc := range tests {
		got := ParseCommandsForAllowList(tc.command)
		if len(got) != len(tc.want) {
			t.Errorf("ParseCommandsForAllowList(%q): length mismatch: got %d, want %d", tc.command, len(got), len(tc.want))
			continue
		}
		for key, wantFlags := range tc.want {
			gotFlags, ok := got[key]
			if !ok {
				t.Errorf("ParseCommandsForAllowList(%q): missing key %q", tc.command, key)
				continue
			}
			if len(gotFlags) != len(wantFlags) {
				t.Errorf("ParseCommandsForAllowList(%q): key %q: flags length mismatch: got %d, want %d", tc.command, key, len(gotFlags), len(wantFlags))
				continue
			}
			for i, f := range wantFlags {
				if gotFlags[i] != f {
					t.Errorf("ParseCommandsForAllowList(%q): key %q: flag mismatch at %d: got %q, want %q", tc.command, key, i, gotFlags[i], f)
				}
			}
		}
	}
}
