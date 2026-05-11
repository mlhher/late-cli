package ast

import (
	"testing"
)

func TestPolicyEngine_Decide_Blocked(t *testing.T) {
	pe := &PolicyEngine{}

	t.Run("cd blocks", func(t *testing.T) {
		ir := emptyIR(PlatformUnix)
		ir.RiskFlags = []ReasonCode{ReasonCd}
		d := pe.Decide(ir)
		if !d.IsBlocked {
			t.Error("expected IsBlocked for ReasonCd")
		}
		if d.BlockReason == nil {
			t.Error("expected non-nil BlockReason for ReasonCd")
		}
	})

	t.Run("redirect blocks", func(t *testing.T) {
		ir := emptyIR(PlatformUnix)
		ir.RiskFlags = []ReasonCode{ReasonRedirect}
		d := pe.Decide(ir)
		if !d.IsBlocked {
			t.Error("expected IsBlocked for ReasonRedirect")
		}
	})

	t.Run("syntax error fail-closed", func(t *testing.T) {
		ir := emptyIR(PlatformUnix)
		ir.RiskFlags = []ReasonCode{ReasonSyntaxError}
		d := pe.Decide(ir)
		if !d.NeedsConfirmation {
			t.Error("expected NeedsConfirmation for ReasonSyntaxError")
		}
		if d.IsBlocked {
			t.Error("syntax error should not be IsBlocked, only NeedsConfirmation")
		}
	})
}

func TestPolicyEngine_Decide_SoftSignals(t *testing.T) {
	pe := &PolicyEngine{}
	for _, rc := range []ReasonCode{ReasonSubshell, ReasonExpansion, ReasonInvokeExpr, ReasonDestructive} {
		t.Run(string(rc), func(t *testing.T) {
			ir := emptyIR(PlatformUnix)
			ir.RiskFlags = []ReasonCode{rc}
			d := pe.Decide(ir)
			if !d.NeedsConfirmation {
				t.Errorf("expected NeedsConfirmation for %v", rc)
			}
			if d.IsBlocked {
				t.Errorf("expected not IsBlocked for soft signal %v", rc)
			}
		})
	}
}

func TestPolicyEngine_Decide_AllowlistedAutoApprove(t *testing.T) {
	pe := &PolicyEngine{
		AllowedCommands: map[string]map[string]bool{
			"ls":   {},
			"grep": {},
		},
	}

	ir := emptyIR(PlatformUnix)
	ir.Commands = []string{"ls", "grep"}
	ir.RiskFlags = []ReasonCode{ReasonOperator}
	ir.Operators = []string{"|"}

	d := pe.Decide(ir)
	if d.NeedsConfirmation {
		t.Error("expected auto-approve for pipe between all-allowlisted commands")
	}
	if d.IsBlocked {
		t.Error("expected not blocked for allowlisted pipe")
	}
}

func TestPolicyEngine_Decide_PartialAllowlistRequiresConfirm(t *testing.T) {
	pe := &PolicyEngine{
		AllowedCommands: map[string]map[string]bool{
			"ls": {},
		},
	}

	ir := emptyIR(PlatformUnix)
	ir.Commands = []string{"ls", "rm"}
	ir.RiskFlags = []ReasonCode{ReasonOperator}

	d := pe.Decide(ir)
	if !d.NeedsConfirmation {
		t.Error("expected NeedsConfirmation: rm is not in allow-list")
	}
}

func TestPolicyEngine_Decide_VersionMismatch(t *testing.T) {
	pe := &PolicyEngine{}
	ir := emptyIR(PlatformUnix)
	ir.Version = "99"
	d := pe.Decide(ir)
	if !d.NeedsConfirmation {
		t.Error("expected NeedsConfirmation for version mismatch")
	}
}

func TestPolicyEngine_Decide_EmptyCommandsNoConfirm(t *testing.T) {
	pe := &PolicyEngine{}
	ir := emptyIR(PlatformUnix)
	// No commands, no risk flags — unusual but should not confirm.
	d := pe.Decide(ir)
	if d.NeedsConfirmation {
		t.Error("empty IR with no risk flags should not require confirmation")
	}
}

func TestPolicyEngine_Decide_UnknownCommandRequiresConfirm(t *testing.T) {
	pe := &PolicyEngine{}
	ir := emptyIR(PlatformUnix)
	ir.Commands = []string{"wget"}
	d := pe.Decide(ir)
	if !d.NeedsConfirmation {
		t.Error("expected NeedsConfirmation for unknown command")
	}
}

// TestPolicyEngine_FlagEnforcement verifies that the policy engine validates
// flags against the stored allow-list, mirroring legacy BashAnalyzer behaviour.
// A previously-approved "find ." must NOT silently permit "find . -exec rm -rf".
func TestPolicyEngine_FlagEnforcement(t *testing.T) {
	t.Run("unapproved flag blocks auto-approve", func(t *testing.T) {
		// Only "find" with no flags was stored in the allow-list.
		pe := &PolicyEngine{
			AllowedCommands: map[string]map[string]bool{
				"find": {},
			},
		}
		ir := emptyIR(PlatformUnix)
		ir.Commands = []string{"find"}
		ir.CommandArgs = map[string][]string{
			"find": {"-exec"},
		}

		d := pe.Decide(ir)
		if !d.NeedsConfirmation {
			t.Error("expected NeedsConfirmation: -exec was not in stored allow-list")
		}
	})

	t.Run("approved flag permits auto-approve", func(t *testing.T) {
		pe := &PolicyEngine{
			AllowedCommands: map[string]map[string]bool{
				"find": {"-name": true, "-type": true},
			},
		}
		ir := emptyIR(PlatformUnix)
		ir.Commands = []string{"find"}
		ir.CommandArgs = map[string][]string{
			"find": {"-name", "-type"},
		}

		d := pe.Decide(ir)
		if d.NeedsConfirmation {
			t.Error("expected auto-approve: all flags are in the stored allow-list")
		}
	})

	t.Run("no flags used auto-approves bare command", func(t *testing.T) {
		pe := &PolicyEngine{
			AllowedCommands: map[string]map[string]bool{
				"find": {},
			},
		}
		ir := emptyIR(PlatformUnix)
		ir.Commands = []string{"find"}
		// CommandArgs empty — bare "find ." with no flags.

		d := pe.Decide(ir)
		if d.NeedsConfirmation {
			t.Error("expected auto-approve for bare allow-listed command with no flags")
		}
	})

	t.Run("pipe: unapproved flag on one command blocks", func(t *testing.T) {
		pe := &PolicyEngine{
			AllowedCommands: map[string]map[string]bool{
				"find": {},
				"grep": {"-r": true},
			},
		}
		ir := emptyIR(PlatformUnix)
		ir.Commands = []string{"find", "grep"}
		ir.Operators = []string{"|"}
		ir.RiskFlags = []ReasonCode{ReasonOperator}
		ir.CommandArgs = map[string][]string{
			"find": {"-exec"}, // not in stored flags → must block
			"grep": {"-r"},
		}

		d := pe.Decide(ir)
		if !d.NeedsConfirmation {
			t.Error("expected NeedsConfirmation: find -exec not in stored allow-list")
		}
	})

	t.Run("unix: find -exec rm -rf parsed as unapproved flag", func(t *testing.T) {
		// End-to-end through the Unix parser.
		pe := &PolicyEngine{
			AllowedCommands: map[string]map[string]bool{
				"find": {}, // only bare find was approved
			},
		}
		p := &UnixParser{}
		ir, err := p.Parse(`find . -exec rm -rf {} \;`)
		if err != nil {
			t.Fatalf("unexpected parse error: %v", err)
		}

		d := pe.Decide(ir)
		if !d.NeedsConfirmation {
			t.Error("expected NeedsConfirmation: -exec not in stored allow-list")
		}
	})
}

// TestGitLogDoesNotApproveGitPush verifies that approving "git log" does NOT
// auto-approve "git push" — each subcommand must have independent approval.
//
// This regression test validates the fix for the subcommand granularity bug where
// the AST adapters now properly emit compound command keys ("git push", not just "git")
// to match the compound keys created by ParseCommandsForAllowList.
func TestGitLogDoesNotApproveGitPush(t *testing.T) {
	p := &UnixParser{}
	ir, err := p.Parse("git push")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	t.Logf("AST IR for 'git push': Commands=%v, CommandArgs=%+v", ir.Commands, ir.CommandArgs)

	// Simulate: user approved "git log --oneline" but NOT "git push"
	pe := &PolicyEngine{
		AllowedCommands: map[string]map[string]bool{
			"git log": {"--oneline": true},
			// Note: "git push" is NOT in the allow-list
		},
	}

	d := pe.Decide(ir)

	// With the fix: adapter now emits "git push" as the command key, policy engine
	// checks if "git push" is in AllowedCommands, it's not, so correctly requires confirmation.
	if !d.NeedsConfirmation {
		t.Errorf("REGRESSION: 'git push' should require confirmation when only 'git log' is approved, but got NeedsConfirmation=false")
		t.Errorf("  Commands in IR: %v", ir.Commands)
		t.Errorf("  Allowed keys: %v", pe.AllowedCommands)
	}
}

// TestGitLogApprovesGitLogWithFlags verifies that "git log" with matching flags
// is approved when "git log --oneline" was previously approved.
func TestGitLogApprovesGitLogWithFlags(t *testing.T) {
	p := &UnixParser{}
	ir, err := p.Parse("git log --oneline")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	// Simulate: user previously approved "git log --oneline"
	pe := &PolicyEngine{
		AllowedCommands: map[string]map[string]bool{
			"git log": {"--oneline": true},
		},
	}

	d := pe.Decide(ir)

	// Should auto-approve because command and all flags match the allow-list.
	if d.NeedsConfirmation {
		t.Errorf("'git log --oneline' should auto-approve when explicitly approved, but got NeedsConfirmation=true")
	}
}

// TestGitLogRejectsGitLogWithNewFlags verifies that "git log" with a new flag
// requires confirmation even if some flags were approved.
func TestGitLogRejectsGitLogWithNewFlags(t *testing.T) {
	p := &UnixParser{}
	ir, err := p.Parse("git log --all")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	// Simulate: user previously approved only "git log --oneline"
	pe := &PolicyEngine{
		AllowedCommands: map[string]map[string]bool{
			"git log": {"--oneline": true},
			// Note: --all is NOT in the allowed flags for "git log"
		},
	}

	d := pe.Decide(ir)

	// Should require confirmation because --all flag was not previously approved.
	if !d.NeedsConfirmation {
		t.Errorf("'git log --all' should require confirmation when only '--oneline' was approved, but got NeedsConfirmation=false")
	}
}
