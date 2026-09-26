package ast

import (
	"errors"
	"strings"
)

// BlockReasonCD is the hard-block message returned for `cd` usage.
const BlockReasonCD = "Do not use `cd` to change directories. Use the `cwd` parameter in the shell tool instead."

// BlockReasonRedirect is the hard-block message returned for unsafe output redirection.
const BlockReasonRedirect = "Output redirection (>) is blocked. Use `write_file` or `target_edit` to modify files."

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
		d.BlockReason = errors.New(BlockReasonCD)
		return d
	}

	// 3. Unsafe output redirect → hard block.
	if hasRisk(ir, ReasonRedirect) {
		d.IsBlocked = true
		d.NeedsConfirmation = true
		d.BlockReason = errors.New(BlockReasonRedirect)
		return d
	}

	// 4. Hard-prompt for deletion commands.
	// Even if explicitly allow-listed, we always prompt for rm to prevent
	// catastrophic scenarios caused by the positional argument loophole.
	for _, cmd := range ir.Commands {
		cmdLower := strings.ToLower(cmd)
		if cmdLower == "rm" || cmdLower == "rmdir" || cmdLower == "unlink" ||
			cmdLower == "remove-item" || cmdLower == "del" || cmdLower == "erase" ||
			cmdLower == "rd" || cmdLower == "ri" {
			d.NeedsConfirmation = true
			return d
		}
	}

	// 5. High-Risk Soft Signals (Always prompt)
	if hasRisk(ir, ReasonSubshell) || hasRisk(ir, ReasonInvokeExpr) {
		d.NeedsConfirmation = true
		return d
	}

	// 6. Expansion handling (Balanced approach)
	if hasRisk(ir, ReasonExpansion) {
		// Only allow expansions for harmless output commands
		allOutput := len(ir.Commands) > 0
		for _, cmd := range ir.Commands {
			cmdLower := strings.ToLower(cmd)
			if cmdLower != "echo" && cmdLower != "write-output" && cmdLower != "write-host" && cmdLower != "printf" {
				allOutput = false
				break
			}
		}
		if !allOutput {
			d.NeedsConfirmation = true
			return d
		}
	}

	// 7. Allow-list check: if every command is explicitly allow-listed, approve.
	// This overrides ReasonDestructive and ReasonOperator.
	isAllowlisted := len(ir.Commands) > 0 && p.allCommandsAllowlisted(ir)
	if isAllowlisted {
		return d
	}

	// 8. Remaining Soft Signals & Operators → NeedsConfirmation.
	// If the command is not explicitly allowlisted, these signals force a prompt.
	for _, soft := range []ReasonCode{ReasonDestructive, ReasonOperator} {
		if hasRisk(ir, soft) {
			d.NeedsConfirmation = true
			return d
		}
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
		// Every flag actually used must appear in the stored allow-list.
		for _, flag := range ir.CommandArgs[cmd] {
			if !allowedFlags[flag] {
				return false
			}
		}
	}
	return true
}
