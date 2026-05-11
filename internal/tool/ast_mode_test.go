//go:build windows

package tool

import (
	"os/exec"
	"testing"
)

func skipIfNoPwshTool(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("pwsh.exe"); err != nil {
		if _, err2 := exec.LookPath("powershell.exe"); err2 != nil {
			t.Skip("pwsh/powershell not available")
		}
	}
}

// TestAST_SafeCommandAutoApproves verifies that a known-safe cmdlet
// auto-approves (no confirmation required) under the AST pipeline.
func TestAST_SafeCommandAutoApproves(t *testing.T) {
	skipIfNoPwshTool(t)

	tool := &ShellTool{}
	blocked, _, confirm := tool.analyzeBashCommand("Get-ChildItem", t.TempDir())
	if blocked || confirm {
		t.Errorf("Get-ChildItem should auto-approve: blocked=%v confirm=%v", blocked, confirm)
	}
}

// TestAST_RiskyCommandRequiresConfirm verifies that a destructive cmdlet
// requires confirmation (not blocked).
func TestAST_RiskyCommandRequiresConfirm(t *testing.T) {
	skipIfNoPwshTool(t)

	tool := &ShellTool{}
	blocked, _, confirm := tool.analyzeBashCommand("Remove-Item foo.txt", t.TempDir())
	if blocked {
		t.Errorf("Remove-Item should not be hard-blocked, only NeedsConfirmation")
	}
	if !confirm {
		t.Errorf("Remove-Item should require confirmation")
	}
}

// TestAST_CdIsBlocked verifies the hard-block path.
func TestAST_CdIsBlocked(t *testing.T) {
	skipIfNoPwshTool(t)

	tool := &ShellTool{}
	blocked, blockReason, _ := tool.analyzeBashCommand("cd C:\\tmp", t.TempDir())
	if !blocked {
		t.Errorf("cd should be hard-blocked")
	}
	if blockReason == nil {
		t.Errorf("cd hard block must carry a non-nil BlockReason")
	}
}

// TestAST_ConstantVarNoConfirm verifies that $true/$false/$null do not trigger
// confirmation (false-positive regression test).
func TestAST_ConstantVarNoConfirm(t *testing.T) {
	skipIfNoPwshTool(t)

	tool := &ShellTool{}
	for _, cmd := range []string{
		"Write-Output $true",
		"Write-Output $false",
		"Write-Output $null",
	} {
		blocked, _, confirm := tool.analyzeBashCommand(cmd, t.TempDir())
		if blocked || confirm {
			t.Errorf("%q should auto-approve (constant var, not dynamic expansion): blocked=%v confirm=%v",
				cmd, blocked, confirm)
		}
	}
}
