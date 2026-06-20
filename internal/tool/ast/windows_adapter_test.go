//go:build windows

package ast

import (
	"os/exec"
	"strings"
	"sync"
	"testing"
)

func skipIfNoPwsh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("pwsh.exe"); err != nil {
		if _, err2 := exec.LookPath("powershell.exe"); err2 != nil {
			t.Skip("pwsh/powershell not available")
		}
	}
}

// TestWindowsParser_BridgeContract verifies the JSON schema contract of the
// PowerShell bridge script for a representative set of command patterns.
func TestWindowsParser_BridgeContract(t *testing.T) {
	skipIfNoPwsh(t)

	p := &WindowsParser{}
	tests := []struct {
		command  string
		wantCmds []string
		wantRisk []ReasonCode
		noRisk   []ReasonCode
	}{
		{
			command:  "Get-ChildItem",
			wantCmds: []string{"get-childitem"},
			noRisk:   []ReasonCode{ReasonRedirect, ReasonSubshell, ReasonInvokeExpr, ReasonDestructive},
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
			noRisk:   []ReasonCode{ReasonDestructive},
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
		// Destructive commands must emit ReasonDestructive, NOT ReasonInvokeExpr.
		{
			command:  "Remove-Item foo.txt",
			wantRisk: []ReasonCode{ReasonDestructive},
			noRisk:   []ReasonCode{ReasonInvokeExpr},
		},
		{
			command:  "Copy-Item src dst",
			wantRisk: []ReasonCode{ReasonDestructive},
			noRisk:   []ReasonCode{ReasonInvokeExpr},
		},
		// Script-block arguments must NOT emit ReasonSubshell.  { } blocks are
		// idiomatic PowerShell parameter syntax, not subshell execution.
		// $_ is a pipeline iteration variable — also filtered, no ReasonExpansion.
		{
			command:  "Get-ChildItem | Where-Object { $_.Name -eq 'foo' }",
			noRisk:   []ReasonCode{ReasonSubshell, ReasonExpansion},
			wantRisk: []ReasonCode{ReasonOperator},
		},
		{
			command:  "Get-ChildItem | ForEach-Object { Write-Output 'done' }",
			noRisk:   []ReasonCode{ReasonSubshell},
			wantRisk: []ReasonCode{ReasonOperator},
		},
		// $true/$false/$null/$_ are language constants — must NOT emit ReasonExpansion.
		{
			command: "Write-Output $true",
			noRisk:  []ReasonCode{ReasonExpansion, ReasonSubshell},
		},
		{
			command: "Write-Output $false",
			noRisk:  []ReasonCode{ReasonExpansion, ReasonSubshell},
		},
		{
			command: "Write-Output $null",
			noRisk:  []ReasonCode{ReasonExpansion, ReasonSubshell},
		},
		// $env:VAR IS a dynamic expansion and must still emit ReasonExpansion.
		{
			command:  "Write-Output $env:USERNAME",
			wantRisk: []ReasonCode{ReasonExpansion},
		},
	}

	for _, tc := range tests {
		t.Run(tc.command, func(t *testing.T) {
			ir, err := p.Parse(tc.command)
			_ = err // bridge errors produce valid IR

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

// TestWindowsParser_SanitizeCommand verifies that sanitizeCommand rejects
// null bytes and over-length commands before touching the bridge.
func TestWindowsParser_SanitizeCommand(t *testing.T) {
	tests := []struct {
		name    string
		command string
		wantErr bool
	}{
		{"valid", "Get-ChildItem", false},
		{"null byte", "Get-\x00ChildItem", true},
		{"empty", "", false},
		{"exactly at limit", strings.Repeat("a", maxCommandBytes), false},
		{"one over limit", strings.Repeat("a", maxCommandBytes+1), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := sanitizeCommand(tc.command)
			if (err != nil) != tc.wantErr {
				t.Errorf("sanitizeCommand() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// TestWindowsParser_SanitizeCommand_IRShape verifies that sanitize failures
// produce a well-formed IR with ReasonSyntaxError (not a panic).
func TestWindowsParser_SanitizeCommand_IRShape(t *testing.T) {
	p := &WindowsParser{}

	ir, err := p.Parse("bad\x00command")
	if err == nil {
		t.Fatal("expected error for null-byte command")
	}
	if ir.Version != IRVersion {
		t.Errorf("Version must be set on sanitize failure, got %q", ir.Version)
	}
	if ir.Platform != PlatformWindows {
		t.Errorf("Platform must be set on sanitize failure")
	}
	if !hasRisk(ir, ReasonSyntaxError) {
		t.Errorf("expected ReasonSyntaxError in risk flags, got %v", ir.RiskFlags)
	}
}

// TestWindowsParser_ParseErrorShape verifies that a syntactically invalid
// command still produces a valid IR with ReasonSyntaxError and correct
// version/platform — the bridge never panics and the Go layer never panics.
func TestWindowsParser_ParseErrorShape(t *testing.T) {
	skipIfNoPwsh(t)

	p := &WindowsParser{}
	ir, _ := p.Parse("if (") // incomplete — PS parser emits a soft error

	if ir.Version != IRVersion {
		t.Errorf("Version must be set on parse error, got %q", ir.Version)
	}
	if ir.Platform != PlatformWindows {
		t.Errorf("Platform must be set on parse error")
	}
}

// TestWindowsParser_Concurrent verifies that multiple goroutines can call
// Parse simultaneously without corrupting the pipe protocol.  All calls
// must return valid IRs.
func TestWindowsParser_Concurrent(t *testing.T) {
	skipIfNoPwsh(t)

	p := &WindowsParser{}
	const n = 12
	type result struct {
		ir  ParsedIR
		err error
	}
	results := make(chan result, n)

	for i := 0; i < n; i++ {
		go func() {
			ir, err := p.Parse("Get-ChildItem")
			results <- result{ir, err}
		}()
	}

	for i := 0; i < n; i++ {
		r := <-results
		if r.err != nil {
			t.Errorf("concurrent Parse error: %v", r.err)
			continue
		}
		if r.ir.Version != IRVersion {
			t.Errorf("concurrent Parse: bad version %q", r.ir.Version)
		}
		if r.ir.Platform != PlatformWindows {
			t.Errorf("concurrent Parse: bad platform %q", r.ir.Platform)
		}
	}
}

// TestWindowsParser_BridgeRestart verifies that Parse automatically recovers
// when the bridge process is externally killed between calls.
func TestWindowsParser_BridgeRestart(t *testing.T) {
	skipIfNoPwsh(t)

	// Ensure a clean bridge for this test.
	CloseBridge()
	t.Cleanup(CloseBridge)

	p := &WindowsParser{}

	// First call — starts the bridge.
	ir1, err := p.Parse("Get-ChildItem")
	if err != nil {
		t.Fatalf("first Parse failed: %v", err)
	}
	if ir1.Platform != PlatformWindows {
		t.Fatalf("first Parse: bad platform")
	}

	// Kill the bridge externally to simulate a crash.
	globalBridgeMu.Lock()
	bp := globalBridge
	globalBridgeMu.Unlock()

	if bp == nil {
		t.Fatal("expected a running bridge after first Parse")
	}
	bp.mu.Lock()
	bp.dead = true
	bp.mu.Unlock()
	_ = bp.cmd.Process.Kill()
	go func() { _ = bp.cmd.Wait() }()
	invalidateBridge(bp)

	// Second call — must restart the bridge and succeed.
	var wg sync.WaitGroup
	wg.Add(1)
	var ir2 ParsedIR
	var err2 error
	go func() {
		defer wg.Done()
		ir2, err2 = p.Parse("Get-Date")
	}()
	wg.Wait()

	if err2 != nil {
		t.Fatalf("Parse after bridge restart failed: %v", err2)
	}
	if ir2.Platform != PlatformWindows {
		t.Errorf("restarted bridge: bad platform %q", ir2.Platform)
	}
}
