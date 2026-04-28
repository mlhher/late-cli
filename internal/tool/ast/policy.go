package ast

import "fmt"

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
//  1. Fail-closed: syntax errors or empty IR → NeedsConfirmation.
//  2. cd command → IsBlocked (users must use the cwd parameter).
//  3. Dangerous output redirect → IsBlocked.
//  4. Dynamic invocation (Invoke-Expression / iex) → NeedsConfirmation.
//  5. Subshell / command substitution → NeedsConfirmation.
//  6. Variable/parameter expansion → NeedsConfirmation.
//  7. Shell operators (&&, ||, ;, control-flow keywords) beyond a safe plain
//     pipe between allow-listed commands → NeedsConfirmation.
//  8. All commands in ir.Commands are allow-listed + no blocking signals
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

	// 4–6. Soft signals → NeedsConfirmation.
	for _, soft := range []ReasonCode{ReasonInvokeExpr, ReasonSubshell, ReasonExpansion} {
		if hasRisk(ir, soft) {
			d.NeedsConfirmation = true
			return d
		}
	}

	// 7. Operator signal (&&, ||, ;, control-flow).
	// A plain | between all-allowlisted commands is permitted; anything else
	// requires confirmation.
	if hasRisk(ir, ReasonOperator) {
		if !p.allCommandsAllowlisted(ir.Commands) {
			d.NeedsConfirmation = true
			return d
		}
		// Pure pipe between allow-listed commands — fall through to auto-approve.
	}

	// 8. Allow-list check: if every command is explicitly allow-listed, approve.
	if len(ir.Commands) > 0 && p.allCommandsAllowlisted(ir.Commands) {
		return d
	}

	// Default: unknown command combination → require confirmation.
	if len(ir.Commands) > 0 {
		d.NeedsConfirmation = true
	}
	return d
}

// allCommandsAllowlisted returns true when every command string in cmds has an
// entry in p.AllowedCommands. An empty allow-list always returns false.
func (p *PolicyEngine) allCommandsAllowlisted(cmds []string) bool {
	if len(p.AllowedCommands) == 0 || len(cmds) == 0 {
		return false
	}
	for _, cmd := range cmds {
		if _, ok := p.AllowedCommands[cmd]; !ok {
			return false
		}
	}
	return true
}
