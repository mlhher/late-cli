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

	if !bashTool.RequiresConfirmation(json.RawMessage(tc.Function.Arguments)) {
		return // safe command: auto-approve as before (any otp_code is ignored)
	}

	// Dangerous command: a valid, unconsumed OTP bound to this EXACT
	// command string is required.
	if params.OTPCode != "" && tool.ConsumeOTP(params.Command, params.OTPCode) {
		approved := context.WithValue(ctx, common.ToolApprovalKey, true)
		return approved, tc, "", true
	}

	// Blocked: issue (or re-present) the pending OTP for this command.
	otpCode := tool.IssueOTP(params.Command)
	return ctx, tc, tool.OTPRevaluateMessage(otpCode), true
}
