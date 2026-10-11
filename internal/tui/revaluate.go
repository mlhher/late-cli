package tui

import (
	"context"
	"encoding/json"
	"late/internal/client"
	"late/internal/common"
	"late/internal/tool"
)

// handleForceRevaluate implements the -force-revaluate-dangerous-commands
// OTP gate for the bash tool inside the skip-confirmation branch of
// TUIConfirmMiddleware (i.e. only where unsupervised mode would otherwise
// auto-approve the call; the Windows bash carve-out never reaches it).
//
// Return values:
//   - handled == false: the gate does not apply (flag off, not bash, hard
//     refusal, or safe command); caller proceeds with the ORIGINAL ctx/tc
//     exactly as before this feature existed, preserving every hard refusal.
//   - handled == true && blockMsg != "": the call is blocked; caller must
//     return (blockMsg, nil) WITHOUT calling next, so the message becomes
//     the tool result the LLM sees. No ToolApprovalKey is stamped.
//   - handled == true && blockMsg == "": the call is approved; caller must
//     call next with the returned ctx (ToolApprovalKey already stamped) and
//     the returned tc, which is UNCHANGED - the command parameter never
//     carries the OTP, so no argument rewriting is ever needed.
func handleForceRevaluate(ctx context.Context, reg *common.ToolRegistry, tc client.ToolCall) (newCtx context.Context, newTC client.ToolCall, blockMsg string, handled bool) {
	newCtx, newTC = ctx, tc
	if enabled, ok := ctx.Value(common.ForceRevaluateKey).(bool); !ok || !enabled {
		return
	}
	if reg == nil || tc.Function.Name != "bash" {
		return
	}
	t := reg.Get(tc.Function.Name)
	bashTool, isShell := t.(*tool.ShellTool)
	if !isShell {
		return
	}

	var params struct {
		Command string `json:"command"`
		Cwd     string `json:"cwd"`
		OTPCode string `json:"otp_code"`
	}
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &params); err != nil {
		return // let the normal flow surface malformed arguments
	}

	// HARD REFUSALS ARE PRESERVED: anything the executor itself would refuse
	// must keep flowing to the normal path and fail exactly as in yolo mode.
	// The OTP gate applies only to commands yolo would have executed.
	if params.Cwd != "" && !tool.IsSafePath(params.Cwd) {
		return // refused in ShellTool.Execute: cwd outside allowed directory
	}
	if err := bashTool.ValidateBashCommand(params.Command, params.Cwd); err != nil {
		return // bash search gate (grep/rg/find...) and AST hard blocks (cd, redirects)
	}

	// Resolve the EXECUTION cwd exactly the way ShellTool.Execute computes
	// cmd.Dir (explicit cwd parameter > worktree dir > process CWD): the
	// exact-string allowlist, the pending-OTP registry and the session
	// counters are all keyed by (cwd, command), because relative paths in
	// the command resolve against the cwd — the same argv from a different
	// directory is a different command.
	execCwd, err := tool.ResolveShellExecCwd(ctx, params.Cwd)
	if err != nil {
		// No working directory to execute in: let the normal flow surface
		// the same failure ShellTool.Execute would.
		return
	}

	if !bashTool.RequiresConfirmationForCwd(params.Command, execCwd) {
		return // safe command: auto-approve as before (any otp_code is ignored)
	}

	// Dangerous command: a valid, unconsumed OTP bound to this EXACT
	// (cwd, command) pair is required. A re-run from a different cwd is a
	// different command and needs a fresh OTP.
	//
	// Gate check order (issue #1): the persistent allow-list check already
	// ran above via RequiresConfirmationForCwd — an exact-string approval
	// written by SaveExactAllowedCommand for this (cwd, command) pair clears
	// NeedsConfirmation inside the analyzer, so an allowlisted exact command
	// returns at the "safe command" branch above and NEVER reaches this OTP
	// branch. The OTP path is only for commands not yet covered by any
	// persistent approval.
	if params.OTPCode != "" && tool.ConsumeOTP(execCwd, params.Command, params.OTPCode) {
		// Issue #1: the exact (cwd, command) pair was approved by OTP and is
		// about to execute — persist it into the PERSISTENT GLOBAL
		// allow-list so every future late instance (any session, any
		// process) auto-allows this exact string in this cwd without
		// prompting. Best-effort: a failed write must not veto an approval
		// the user already granted; the command would simply gate again on
		// its next run.
		_ = tool.SaveExactAllowedCommand(execCwd, params.Command, true)
		tool.RecordOTPApproval(execCwd, params.Command)
		approved := context.WithValue(ctx, common.ToolApprovalKey, true)
		return approved, tc, "", true
	}

	// Blocked: issue (or re-present) the pending OTP for this (cwd, command)
	// pair, reporting how often this exact pair was approved before (issue
	// #1 gate-message suggestion) plus the session-scope approval count.
	otpCode := tool.IssueOTP(execCwd, params.Command)
	return ctx, tc, tool.OTPRevaluateMessageDetailed(execCwd, otpCode,
		tool.CountExactCommandApprovals(execCwd, params.Command),
		tool.SessionOTPApprovals(execCwd, params.Command)), true
}
