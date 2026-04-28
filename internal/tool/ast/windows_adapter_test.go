//go:build windows

package ast

import (
	"os/exec"
	"strings"
	"testing"
)

// TestWindowsParser_BridgeContract verifies the JSON schema contract of the
// PowerShell bridge script. It skips when pwsh is unavailable (CI without PS).
func TestWindowsParser_BridgeContract(t *testing.T) {
	if _, err := exec.LookPath("pwsh.exe"); err != nil {
		if _, err2 := exec.LookPath("powershell.exe"); err2 != nil {
			t.Skip("pwsh/powershell not available")
		}
	}

	p := &WindowsParser{}
	tests := []struct {
		command     string
		wantCmds    []string
		wantRisk    []ReasonCode
		noRisk      []ReasonCode
	}{
		{
			command:  "Get-ChildItem",
			wantCmds: []string{"get-childitem"},
			noRisk:   []ReasonCode{ReasonRedirect, ReasonSubshell, ReasonInvokeExpr},
		},
		{
			command:  "Get-ChildItem | Select-String foo",
			wantCmds: []string{"get-childitem", "select-string"},
			wantRisk: []ReasonCode{ReasonOperator},
		},
		{
			command:  "Get-Date; Get-Location",
			wantRisk: []ReasonCode{ReasonOperator},
		},
		{
			command:  "Invoke-Expression 'rm -rf /'",
			wantRisk: []ReasonCode{ReasonInvokeExpr},
		},
		{
			command:  "$x = 'foo'; Write-Output $x",
			wantRisk: []ReasonCode{ReasonExpansion},
		},
		{
			command:  "Get-ChildItem > out.txt",
			wantRisk: []ReasonCode{ReasonRedirect},
		},
		{
			command:  "Write-Output $(Get-Date)",
			wantRisk: []ReasonCode{ReasonSubshell},
		},
	}

	for _, tc := range tests {
		t.Run(tc.command, func(t *testing.T) {
			ir, err := p.Parse(tc.command)
			// Bridge errors are allowed for problematic syntax but must produce a valid IR.
			_ = err

			if ir.Version != IRVersion {
				t.Errorf("Version: got %q want %q", ir.Version, IRVersion)
			}
			if ir.Platform != PlatformWindows {
				t.Errorf("Platform: got %q want %q", ir.Platform, PlatformWindows)
			}

			for _, wantCmd := range tc.wantCmds {
				found := false
				for _, c := range ir.Commands {
					if strings.EqualFold(c, wantCmd) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected command %q in %v", wantCmd, ir.Commands)
				}
			}

			for _, wantRC := range tc.wantRisk {
				if !hasRisk(ir, wantRC) {
					t.Errorf("expected risk flag %q in %v", wantRC, ir.RiskFlags)
				}
			}

			for _, noRC := range tc.noRisk {
				if hasRisk(ir, noRC) {
					t.Errorf("unexpected risk flag %q in %v", noRC, ir.RiskFlags)
				}
			}
		})
	}
}

// TestWindowsParser_FailClosed ensures the adapter fails safely when pwsh is
// absent or the bridge emits garbage.
func TestWindowsParser_FailClosed(t *testing.T) {
	// Simulate unavailable shell by using an empty path override in a test-only way.
	// We can't easily do this without the sync.Once, so just check the IR contract
	// on a real parse with a clearly broken input.
	if _, err := exec.LookPath("pwsh.exe"); err != nil {
		if _, err2 := exec.LookPath("powershell.exe"); err2 != nil {
			t.Skip("pwsh/powershell not available")
		}
	}

	p := &WindowsParser{}
	// Malformed input — PS parser should still emit something but we verify
	// the Go layer never panics and returns a usable IR.
	ir, _ := p.Parse("if (")

	if ir.Version != IRVersion {
		t.Errorf("Version must be set even on parse error, got %q", ir.Version)
	}
	if ir.Platform != PlatformWindows {
		t.Errorf("Platform must be set even on parse error")
	}
}
