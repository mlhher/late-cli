package tool

import (
	"testing"
)

func TestParseCommandsForAllowList(t *testing.T) {
	tests := []struct {
		command string
		want    map[string][]string
	}{
		{
			// Tier2 commands include subcommands in the key.
			"go mod tidy && go test -v ./...",
			map[string][]string{
				"go mod":  {},
				"go test": {"-v"},
			},
		},
		{
			// Tier2 commands include subcommands and preserve flag capture.
			"git log --oneline --output=test.txt | grep foo",
			map[string][]string{
				"git log": {"--oneline", "--output"},
				"grep":    {},
			},
		},
		{
			"git push origin main",
			map[string][]string{
				"git push": {},
			},
		},
		{
			// PowerShell: command lowercased, flags preserved (with original casing)
			"Get-ChildItem -Path C:\\Temp | Write-Output",
			map[string][]string{
				"get-childitem": {"-Path"},
				"write-output":  {},
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
