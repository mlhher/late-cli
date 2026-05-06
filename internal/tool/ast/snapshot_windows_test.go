//go:build windows

package ast

import "testing"

// windowsCorpus mirrors the PowerShellAnalyzer test expectations for the
// Windows policy path. Tests run through the PolicyEngine with Windows
// built-in cmdlets pre-seeded, mirroring the enforcement-mode allow-list.
var windowsCorpus = []snapshotEntry{
	// Safe read-only cmdlets — auto-approved via built-in allowlist.
	{"Get-ChildItem", false, false},
	{"Get-ChildItem -Recurse", false, false},
	{"Get-Content README.md", false, false},
	{"Get-Date", false, false},
	{"Get-Location", false, false},
	{"Write-Output hello", false, false},
	{"whoami", false, false},
	{"ls", false, false},
	{"pwd", false, false},
	// Risky: Invoke-Expression.
	{"Invoke-Expression 'rm -rf /'", false, true},
	{"iex 'bad'", false, true},
	// $true/$false/$null — language constants, must not trigger expansion risk.
	{"Write-Output $true", false, false},
	// $env:VAR IS a dynamic expansion — must require confirmation.
	{"Write-Output $env:USERNAME", false, true},
	// Risky: subshell (sub-expression).
	{"Write-Output $(Get-Date)", false, true},
	// Risky: Invoke-Command (dynamic eval). The script-block syntax itself is
	// not the risk signal — invoke-command is.
	{"Invoke-Command -ScriptBlock { rm -rf / }", false, true},
	// cd / Set-Location — hard blocked.
	{"Set-Location C:\\tmp", true, true},
	{"cd C:\\tmp", true, true},
	{"sl /tmp", true, true},
	{"Push-Location C:\\tmp", true, true},
	{"pushd C:\\tmp", true, true},
	// Redirect to file — hard blocked.
	{"Get-ChildItem > out.txt", true, true},
	// Safe pipe between known cmdlets.
	{"Get-ChildItem | Select-String foo", false, false},
	// Operator separating unknown cmdlet → confirm.
	{"Get-ChildItem; Invoke-Expression 'x'", false, true},
	// Destructive: Remove-Item — requires confirmation, not blocked.
	{"Remove-Item foo.txt", false, true},
	// Script block as argument — idiomatic PS, must NOT emit ReasonSubshell.
	{"Get-ChildItem | ForEach-Object { Write-Output 'done' }", false, true},
	// Dangerous Windows-specific flags.
	{"powershell -EncodedCommand abc", false, true},
}

// windowsBuiltinPE returns a PolicyEngine pre-seeded with the Windows
// built-in safe cmdlets, mirroring what newASTAnalyzer does in enforcement mode.
func windowsBuiltinPE() *PolicyEngine {
	builtins := []string{
		"cat", "date", "dir", "echo",
		"gc", "gci", "get-childitem", "get-content", "get-date", "get-location",
		"ls", "measure-object", "pwd",
		"select-string", "sls", "type", "whoami", "write-host", "write-output",
	}
	m := make(map[string]map[string]bool, len(builtins))
	for _, cmd := range builtins {
		m[cmd] = map[string]bool{}
	}
	// Add safe flags for commands that use them in the corpus.
	m["get-childitem"]["-Recurse"] = true
	return &PolicyEngine{AllowedCommands: m}
}

// TestWindowsCorpusSnapshot verifies the PolicyEngine + Windows built-in
// allowlist against the known-good baseline corpus.  Requires pwsh; restricted
// to Windows builds via the go:build tag.
func TestWindowsCorpusSnapshot(t *testing.T) {
	skipIfNoPwsh(t)
	pe := windowsBuiltinPE()
	p := &WindowsParser{}

	for _, tc := range windowsCorpus {
		t.Run(tc.command, func(t *testing.T) {
			ir, _ := p.Parse(tc.command)
			d := pe.Decide(ir)

			if d.IsBlocked != tc.wantBlocked {
				t.Errorf("IsBlocked: got %v want %v (flags=%v, errs=%v)",
					d.IsBlocked, tc.wantBlocked, ir.RiskFlags, ir.ParseErrors)
			}
			if d.NeedsConfirmation != tc.wantConfirm {
				t.Errorf("NeedsConfirmation: got %v want %v (flags=%v, errs=%v)",
					d.NeedsConfirmation, tc.wantConfirm, ir.RiskFlags, ir.ParseErrors)
			}
		})
	}
}
