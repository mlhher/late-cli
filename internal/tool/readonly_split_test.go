package tool

import (
	"testing"

	"late/internal/tool/ast"
)

// TestReadOnlySplit_AnalyzerSkipsConfirmation verifies issue #1 suggestion #2
// end-to-end through the AST analyzer the shell tool builds: read-only gh/git
// operations need no confirmation (hence no OTP re-evaluation), while
// mutating, chained-dangerous and unknown subcommands keep the gate. No
// allow-list entries are provided, proving the read-only classification
// stands on its own.
func TestReadOnlySplit_AnalyzerSkipsConfirmation(t *testing.T) {
	readOnly := []string{
		"gh pr view 42",
		"gh issue list",
		"gh api /user",
		"gh api --method GET /user",
		"gh run list | grep deploy",
		"git log --oneline",
		"git status",
		"git diff | wc -l",
	}
	for _, command := range readOnly {
		t.Run("approved/"+command, func(t *testing.T) {
			analyzer := newASTAnalyzer(ast.PlatformUnix, "", map[string]map[string]bool{})
			analysis := analyzer.Analyze(command)
			if analysis.IsBlocked {
				t.Errorf("read-only command %q unexpectedly hard-blocked: %v", command, analysis.BlockReason)
			}
			if analysis.NeedsConfirmation {
				t.Errorf("read-only command %q unexpectedly requires confirmation", command)
			}
		})
	}

	stillGated := []string{
		"gh api -X POST repos/cli/cli/issues",
		"gh pr merge 42",
		"gh repo delete cli/cli",
		"gh gist create notes.txt",
		"gh codespace list",
		"git push origin main",
		"git branch -D feat",
		"gh pr view 42 && rm -rf /tmp/x",
		"sudo gh pr view 42",
	}
	for _, command := range stillGated {
		t.Run("gated/"+command, func(t *testing.T) {
			analyzer := newASTAnalyzer(ast.PlatformUnix, "", map[string]map[string]bool{})
			analysis := analyzer.Analyze(command)
			if !analysis.NeedsConfirmation {
				t.Errorf("command %q must still require confirmation", command)
			}
		})
	}
}
