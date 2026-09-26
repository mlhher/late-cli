package tool

import "testing"

// TestIsShellFailureResult covers every prefix IsShellFailureResult must
// recognize. The third case is the one ExecuteToolCalls actually sees for a
// shell TIMEOUT: ShellTool.Execute returns the timeout as an error, and
// ExecuteToolCalls wraps it with its own "Error executing tool %s: %v"
// prefix before the result ever reaches IsShellFailureResult ("bash" is the
// tool name on every platform). A timed-out command is the highest
// scope-creep risk, so it must match.
func TestIsShellFailureResult(t *testing.T) {
	cases := []struct {
		name   string
		result string
		want   bool
	}{
		{"nonzero exit", "Command failed with exit code 1\nsome output", true},
		{"exec error", "Error executing command: signal: killed", true},
		{
			"timeout via ExecuteToolCalls wrapper",
			"Error executing tool bash: command timed out after 10m0s and was killed (partial output):\npartial stdout",
			true,
		},
		{"other tool error is not a shell failure", "Error executing tool read_file: boom", false},
		{"success output", "all good\n", false},
		{"empty", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsShellFailureResult(tc.result); got != tc.want {
				t.Errorf("IsShellFailureResult(%q) = %v, want %v", tc.result, got, tc.want)
			}
		})
	}
}
