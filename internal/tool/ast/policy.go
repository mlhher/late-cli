package ast

import "fmt"

// tier2Commands is the set of commands that have mandatory subcommands.
// The AST adapters should emit compound command keys (e.g. "git log", "go mod")
// for these commands to maintain fine-grained allow-list granularity.
var tier2Commands = map[string]bool{
	"git": true,
	"go":  true,
}

// PolicyEngine evaluates a ParsedIR against an optional allow-list and
// produces a Decision. The engine consumes ONLY the compact IR — no raw
// AST nodes — making decisions deterministic and platform-neutral.
//
// Decision semantics (mirrors CommandAnalysis in the tool package):
//   - IsBlocked:         hard block; execution MUST be prevented.
//   - NeedsConfirmation: soft gate; user confirmation required before execution.
//   - BlockReason:       non-nil error message when IsBlocked is true.
//   - ReasonCodes:       the risk flags that drove the decision.
type PolicyEngine struct {
	// AllowedCommands is the merged project/global/session allow-list loaded
	// from the permissions subsystem. Keys are normalized command strings
	// (e.g. "git log"). A nil or empty map disables allow-list overrides.
	AllowedCommands map[string]map[string]bool
}

// Decide converts a ParsedIR into a Decision.
//
// Blocking rules (checked in order):
//  1. Schema version mismatch → NeedsConfirmation (fail-closed).
//  2. Syntax/parse errors → NeedsConfirmation (fail-closed).
//     Note: an empty IR with no risk flags and no parse errors is valid and
//     auto-approves (rule 9 below). Only explicit parse errors trigger this.
//  3. cd command → IsBlocked (users must use the cwd parameter).
//  4. Dangerous output redirect → IsBlocked.
//  5. Dynamic invocation (Invoke-Expression / iex) → NeedsConfirmation.
//  6. Subshell / command substitution → NeedsConfirmation.
//  7. Variable/parameter expansion → NeedsConfirmation.
//  8. Destructive filesystem operation (Remove-Item, Copy-Item, etc.) → NeedsConfirmation.
//  9. Shell operators (&&, ||, ;, |) with any non-allow-listed command → NeedsConfirmation.
//  10. All commands in ir.Commands are allow-listed + no blocking signals
//     → auto-approve (NeedsConfirmation = false).
func (p *PolicyEngine) Decide(ir ParsedIR) Decision {
	d := Decision{ReasonCodes: ir.RiskFlags}

	// 0. Schema sanity — treat mismatched versions as fail-closed.
	if ir.Version != IRVersion {
		d.NeedsConfirmation = true
		return d
	}

	// 1. Syntax/parse errors → fail closed.
	if hasRisk(ir, ReasonSyntaxError) || len(ir.ParseErrors) > 0 {
		d.NeedsConfirmation = true
		return d
	}

	// 2. cd → hard block.
	if hasRisk(ir, ReasonCd) {
		d.IsBlocked = true
		d.NeedsConfirmation = true
		d.BlockReason = fmt.Errorf(
			"Do not use `cd` to change directories. Use the `cwd` parameter in the shell tool instead.")
		return d
	}

	// 3. Unsafe output redirect → hard block.
	if hasRisk(ir, ReasonRedirect) {
		d.IsBlocked = true
		d.NeedsConfirmation = true
		d.BlockReason = fmt.Errorf(
			"Output redirection (>) is blocked. Use `write_file` or `target_edit` to modify files.")
		return d
	}

	// 4–7. Soft signals → NeedsConfirmation.
	for _, soft := range []ReasonCode{ReasonInvokeExpr, ReasonSubshell, ReasonExpansion, ReasonDestructive} {
		if hasRisk(ir, soft) {
			d.NeedsConfirmation = true
			return d
		}
	}

	// 8. Operator signal (&&, ||, ;, |).
	// Any compound/pipe where all commands are allow-listed is permitted;
	// otherwise require confirmation.
	if hasRisk(ir, ReasonOperator) {
		if !p.allCommandsAllowlisted(ir) {
			d.NeedsConfirmation = true
			return d
		}
		// All commands allowlisted — fall through to auto-approve.
	}

	// 9. Allow-list check: if every command is explicitly allow-listed, approve.
	if len(ir.Commands) > 0 && p.allCommandsAllowlisted(ir) {
		return d
	}

	// Default: unknown command combination → require confirmation.
	if len(ir.Commands) > 0 {
		d.NeedsConfirmation = true
	}
	return d
}

// allCommandsAllowlisted returns true when every command in ir.Commands has an
// entry in p.AllowedCommands AND every flag used in the invocation is present
// in the stored allowed-flag set for that command.
//
// Flag validation is strict: if a flag appears in the
// command but was not stored when the command was originally approved, the
// allow-list check fails and the policy engine falls through to
// NeedsConfirmation. This prevents a previously-approved "find ." from
// silently permitting "find . -exec rm -rf {} \;".
func (p *PolicyEngine) allCommandsAllowlisted(ir ParsedIR) bool {
	if len(p.AllowedCommands) == 0 || len(ir.Commands) == 0 {
		return false
	}
	for _, cmd := range ir.Commands {
		allowedFlags, ok := p.AllowedCommands[cmd]
		if !ok {
			return false
		}
		// nil flag set = built-in whitelist entry: all flags are permitted.
		// Only enforce strict flag checking for user-approved commands
		// (non-nil flag sets stored by the permissions subsystem).
		if allowedFlags == nil {
			continue
		}
		// Every flag actually used must appear in the stored allow-list.
		for _, flag := range ir.CommandArgs[cmd] {
			if !allowedFlags[flag] {
				return false
			}
		}
	}
	return true
}
