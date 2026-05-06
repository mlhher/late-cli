//go:build windows

package tool

import (
	"os/exec"
	"testing"

	"late/internal/tool/ast"
)

func skipIfNoPwshTool(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("pwsh.exe"); err != nil {
		if _, err2 := exec.LookPath("powershell.exe"); err2 != nil {
			t.Skip("pwsh/powershell not available")
		}
	}
}

// TestGetAnalyzer_WindowsAlwaysAST verifies that Windows always uses the AST
// analyzer regardless of feature flags, now that the legacy PowerShellAnalyzer
// has been fully migrated to AST.
func TestGetAnalyzer_WindowsAlwaysAST(t *testing.T) {
	t.Setenv(ast.EnvASTEnforcement, "")
	t.Setenv(ast.EnvASTShadow, "")

	tool := &ShellTool{}
	analyzer := tool.getAnalyzer(t.TempDir())

	if _, ok := analyzer.(*astAnalyzer); !ok {
		t.Errorf("expected *astAnalyzer on Windows (no legacy fallback), got %T", analyzer)
	}
}

// TestGetAnalyzer_WindowsIgnoresFeatureFlags verifies that Windows always uses
// the AST analyzer even when LATE_AST_SHADOW is set.
func TestGetAnalyzer_WindowsIgnoresFeatureFlags(t *testing.T) {
	t.Setenv(ast.EnvASTShadow, "1")
	t.Setenv(ast.EnvASTEnforcement, "")

	tool := &ShellTool{}
	analyzer := tool.getAnalyzer(t.TempDir())

	if _, ok := analyzer.(*astAnalyzer); !ok {
		t.Errorf("expected *astAnalyzer on Windows (ignoring feature flags), got %T", analyzer)
	}
}

// TestGetAnalyzer_WindowsEnforcementMode verifies that LATE_AST_ENFORCEMENT=1
// still returns an astAnalyzer on Windows (which is the default now).
func TestGetAnalyzer_WindowsEnforcementMode(t *testing.T) {
	t.Setenv(ast.EnvASTEnforcement, "1")
	t.Setenv(ast.EnvASTShadow, "")

	tool := &ShellTool{}
	analyzer := tool.getAnalyzer(t.TempDir())

	if _, ok := analyzer.(*astAnalyzer); !ok {
		t.Errorf("expected *astAnalyzer on Windows, got %T", analyzer)
	}
}

// TestGetAnalyzer_BothFlagsSet verifies that both flags set results in AST
// (which is the only option on Windows now).
func TestGetAnalyzer_BothFlagsSet(t *testing.T) {
	t.Setenv(ast.EnvASTEnforcement, "1")
	t.Setenv(ast.EnvASTShadow, "1")

	tool := &ShellTool{}
	analyzer := tool.getAnalyzer(t.TempDir())

	if _, ok := analyzer.(*astAnalyzer); !ok {
		t.Errorf("expected *astAnalyzer on Windows, got %T", analyzer)
	}
}

// TestEnforcementMode_SafeCommandAutoApproves verifies that a known-safe
// cmdlet auto-approves (no confirmation required) when the AST pipeline is
// authoritative.
func TestEnforcementMode_SafeCommandAutoApproves(t *testing.T) {
	skipIfNoPwshTool(t)
	t.Setenv(ast.EnvASTEnforcement, "1")
	t.Setenv(ast.EnvASTShadow, "")

	tool := &ShellTool{}
	blocked, _, confirm := tool.analyzeBashCommand("Get-ChildItem", t.TempDir())
	if blocked || confirm {
		t.Errorf("Get-ChildItem should auto-approve in enforcement mode: blocked=%v confirm=%v", blocked, confirm)
	}
}

// TestEnforcementMode_RiskyCommandRequiresConfirm verifies that a destructive
// cmdlet requires confirmation (not blocked) in enforcement mode.
func TestEnforcementMode_RiskyCommandRequiresConfirm(t *testing.T) {
	skipIfNoPwshTool(t)
	t.Setenv(ast.EnvASTEnforcement, "1")
	t.Setenv(ast.EnvASTShadow, "")

	tool := &ShellTool{}
	blocked, _, confirm := tool.analyzeBashCommand("Remove-Item foo.txt", t.TempDir())
	if blocked {
		t.Errorf("Remove-Item should not be hard-blocked, only NeedsConfirmation")
	}
	if !confirm {
		t.Errorf("Remove-Item should require confirmation in enforcement mode")
	}
}

// TestEnforcementMode_CdIsBlocked verifies the hard-block path in enforcement mode.
func TestEnforcementMode_CdIsBlocked(t *testing.T) {
	skipIfNoPwshTool(t)
	t.Setenv(ast.EnvASTEnforcement, "1")
	t.Setenv(ast.EnvASTShadow, "")

	tool := &ShellTool{}
	blocked, blockReason, _ := tool.analyzeBashCommand("cd C:\\tmp", t.TempDir())
	if !blocked {
		t.Errorf("cd should be hard-blocked in enforcement mode")
	}
	if blockReason == nil {
		t.Errorf("cd hard block must carry a non-nil BlockReason")
	}
}

// TestEnforcementMode_ConstantVarNoConfirm verifies that $true/$false/$null do
// not trigger confirmation in enforcement mode (false-positive regression test).
func TestEnforcementMode_ConstantVarNoConfirm(t *testing.T) {
	skipIfNoPwshTool(t)
	t.Setenv(ast.EnvASTEnforcement, "1")
	t.Setenv(ast.EnvASTShadow, "")

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

// TestShadowMode_ReturnsLegacyDecision verifies that shadow mode returns the
// legacy result (no behavior change) even when the AST pipeline is running.
func TestShadowMode_ReturnsLegacyDecision(t *testing.T) {
	skipIfNoPwshTool(t)
	t.Setenv(ast.EnvASTShadow, "1")
	t.Setenv(ast.EnvASTEnforcement, "")

	tool := &ShellTool{}
	// Get-ChildItem is safe in both legacy and AST paths.
	blocked, _, confirm := tool.analyzeBashCommand("Get-ChildItem", t.TempDir())
	if blocked || confirm {
		t.Errorf("shadow mode must return legacy result for Get-ChildItem: blocked=%v confirm=%v", blocked, confirm)
	}
	// Remove-Item is risky in both paths.
	_, _, confirm = tool.analyzeBashCommand("Remove-Item foo.txt", t.TempDir())
	if !confirm {
		t.Errorf("shadow mode must return legacy result for Remove-Item: expected confirm=true")
	}
}
