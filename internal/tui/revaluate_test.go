package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"late/internal/client"
	"late/internal/common"
	"late/internal/tool"
)

// ---------------------------------------------------------------------------
// Test harness
//
// The bash gate under test consults three kinds of ambient state:
//  1. the AST analyzer's allow-lists, read from ./.late/allowed_commands.json
//     (relative to the process CWD) and from the OS config dir (derived from
//     $HOME on darwin/windows, $XDG_CONFIG_HOME on linux),
//  2. the package-global pending-OTP registry in internal/tool,
//  3. the LATE_BASH_GATE environment variable (search-command gate level).
//
// isolateTestEnv points HOME and the process working directory at a fresh
// temp dir so no real allow-list can mark commands as safe (a dangerous `rm`
// therefore always prompts), pins the search gate to its default "enforce"
// level, and drops all pending OTPs so tests cannot leak codes into each
// other.
func isolateTestEnv(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("bash-specific gate tests")
	}

	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("LATE_BASH_GATE", "enforce")
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("failed to chdir into temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origWd)
		tool.ResetOTPRegistry()
	})
	tool.ResetOTPRegistry()
	return tmp
}

func newBashRegistry(t *testing.T) *common.ToolRegistry {
	t.Helper()
	reg := common.NewToolRegistry()
	reg.Register(&tool.ShellTool{})
	return reg
}

type bashArgs struct {
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
	OTPCode string `json:"otp_code"`
}

func bashCall(t *testing.T, args bashArgs) client.ToolCall {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("failed to marshal bash arguments: %v", err)
	}
	return client.ToolCall{
		Type: "function",
		Function: client.FunctionCall{
			Name:      "bash",
			Arguments: string(raw),
		},
	}
}

// forceRevaluateCtx simulates an unsupervised run started with
// -force-revaluate-dangerous-commands.
func forceRevaluateCtx() context.Context {
	ctx := context.Background()
	ctx = context.WithValue(ctx, common.SkipConfirmationKey, true)
	ctx = context.WithValue(ctx, common.ForceRevaluateKey, true)
	return ctx
}

var otpCodePattern = regexp.MustCompile(`\b\d{7}\b`)

// extractOTP asserts the message contains exactly one 7-digit code and
// returns it.
func extractOTP(t *testing.T, msg string) string {
	t.Helper()
	matches := otpCodePattern.FindAllString(msg, -1)
	if len(matches) != 1 {
		t.Fatalf("expected exactly one 7-digit code in message, got %d: %q", len(matches), msg)
	}
	if len(matches[0]) != 7 {
		t.Fatalf("expected a 7-digit code, got %q", matches[0])
	}
	return matches[0]
}

// dangerousCommand returns a command the policy always prompts for (rm),
// targeting a path inside the isolated temp cwd.
func dangerousCommand(tmp string) string {
	return "rm -rf " + filepath.Join(tmp, "build")
}

// ---------------------------------------------------------------------------
// Gate activation
// ---------------------------------------------------------------------------

func TestHandleForceRevaluate_InactiveWithoutFlag(t *testing.T) {
	tmp := isolateTestEnv(t)
	reg := newBashRegistry(t)

	// Unsupervised mode alone (no -force-revaluate-dangerous-commands) must
	// keep the pre-existing yolo behaviour: the gate never engages.
	ctx := context.WithValue(context.Background(), common.SkipConfirmationKey, true)

	_, _, blockMsg, handled := handleForceRevaluate(ctx, reg, bashCall(t, bashArgs{Command: dangerousCommand(tmp)}))
	if handled {
		t.Fatalf("expected gate to be inactive without ForceRevaluateKey, got handled=true blockMsg=%q", blockMsg)
	}
}

func TestHandleForceRevaluate_InactiveForNonBash(t *testing.T) {
	isolateTestEnv(t)

	// Registry deliberately contains only a non-shell tool; the call targets
	// "other", which is not registered at all.
	reg := common.NewToolRegistry()
	reg.Register(&tool.ReadFileTool{})

	tc := client.ToolCall{
		Type: "function",
		Function: client.FunctionCall{
			Name:      "other",
			Arguments: `{"command": "rm -rf build"}`,
		},
	}

	_, _, blockMsg, handled := handleForceRevaluate(forceRevaluateCtx(), reg, tc)
	if handled {
		t.Fatalf("expected gate to be inactive for non-bash tool calls, got handled=true blockMsg=%q", blockMsg)
	}
}

// ---------------------------------------------------------------------------
// Middleware end-to-end
// ---------------------------------------------------------------------------

func TestHandleForceRevaluate_BlocksDangerousCommandFirstAttempt(t *testing.T) {
	tmp := isolateTestEnv(t)
	messenger := &mockMessenger{}
	reg := newBashRegistry(t)

	nextCalls := 0
	next := func(ctx context.Context, tc client.ToolCall) (string, error) {
		nextCalls++
		return "ok", nil
	}
	runner := TUIConfirmMiddleware(messenger, reg)(next)

	tc := bashCall(t, bashArgs{Command: dangerousCommand(tmp)})

	result, err := runner(forceRevaluateCtx(), tc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "re-evaluate") {
		t.Errorf("expected block message to ask for re-evaluation, got %q", result)
	}
	if code := extractOTP(t, result); len(code) != 7 {
		t.Errorf("expected a 7-digit OTP code in the block message, got %q", code)
	}
	if nextCalls != 0 {
		t.Errorf("expected next NOT to be called while blocked, got %d calls", nextCalls)
	}
	if messenger.confirmCalled {
		t.Errorf("expected no interactive confirmation request, but one was recorded")
	}
}

func TestHandleForceRevaluate_PendingCodeIsStable(t *testing.T) {
	tmp := isolateTestEnv(t)
	messenger := &mockMessenger{}
	reg := newBashRegistry(t)

	next := func(ctx context.Context, tc client.ToolCall) (string, error) {
		t.Errorf("next must not be called while the command stays blocked")
		return "ok", nil
	}
	runner := TUIConfirmMiddleware(messenger, reg)(next)

	tc := bashCall(t, bashArgs{Command: dangerousCommand(tmp)})
	ctx := forceRevaluateCtx()

	first, err := runner(ctx, tc)
	if err != nil {
		t.Fatalf("unexpected error on first attempt: %v", err)
	}
	second, err := runner(ctx, tc)
	if err != nil {
		t.Fatalf("unexpected error on second attempt: %v", err)
	}

	firstCode := extractOTP(t, first)
	secondCode := extractOTP(t, second)
	if firstCode != secondCode {
		t.Errorf("expected the pending code to be re-presented, got %q then %q", firstCode, secondCode)
	}
}

func TestHandleForceRevaluate_ValidOTPRetryExecutes(t *testing.T) {
	tmp := isolateTestEnv(t)
	messenger := &mockMessenger{}
	reg := newBashRegistry(t)

	command := dangerousCommand(tmp)
	tc := bashCall(t, bashArgs{Command: command})
	ctx := forceRevaluateCtx()

	nextCalls := 0
	var nextCtx context.Context
	var nextTC client.ToolCall
	next := func(ctx context.Context, tc client.ToolCall) (string, error) {
		nextCalls++
		nextCtx, nextTC = ctx, tc
		return "ok", nil
	}
	runner := TUIConfirmMiddleware(messenger, reg)(next)

	blocked, err := runner(ctx, tc)
	if err != nil {
		t.Fatalf("unexpected error on first attempt: %v", err)
	}
	code := extractOTP(t, blocked)

	retry := bashCall(t, bashArgs{Command: command, OTPCode: code})
	result, err := runner(ctx, retry)
	if err != nil {
		t.Fatalf("unexpected error on retry: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected next's result to be forwarded, got %q", result)
	}
	if nextCalls != 1 {
		t.Fatalf("expected next to be called exactly once, got %d calls", nextCalls)
	}
	if nextTC.Function.Arguments != retry.Function.Arguments {
		t.Errorf("expected tool call arguments to pass through unchanged, got %q want %q", nextTC.Function.Arguments, retry.Function.Arguments)
	}
	var passed bashArgs
	if err := json.Unmarshal([]byte(nextTC.Function.Arguments), &passed); err != nil {
		t.Fatalf("failed to unmarshal forwarded arguments: %v", err)
	}
	if passed.Command != command {
		t.Errorf("expected forwarded command to be byte-identical to the original, got %q want %q", passed.Command, command)
	}
	approved, ok := nextCtx.Value(common.ToolApprovalKey).(bool)
	if !ok || !approved {
		t.Errorf("expected ToolApprovalKey to be true in the context passed to next")
	}
	if messenger.confirmCalled {
		t.Errorf("expected no interactive confirmation request, but one was recorded")
	}
}

// ---------------------------------------------------------------------------
// OTP lifecycle
// ---------------------------------------------------------------------------

func TestHandleForceRevaluate_OTPSingleUse(t *testing.T) {
	tmp := isolateTestEnv(t)
	reg := newBashRegistry(t)
	ctx := forceRevaluateCtx()
	command := dangerousCommand(tmp)

	_, _, blockMsg, handled := handleForceRevaluate(ctx, reg, bashCall(t, bashArgs{Command: command}))
	if !handled || blockMsg == "" {
		t.Fatalf("expected first attempt to be blocked with a message, got handled=%v blockMsg=%q", handled, blockMsg)
	}
	code := extractOTP(t, blockMsg)

	// Valid OTP approves the call: handled with an empty block message.
	_, _, approveMsg, approved := handleForceRevaluate(ctx, reg, bashCall(t, bashArgs{Command: command, OTPCode: code}))
	if !approved || approveMsg != "" {
		t.Fatalf("expected valid OTP to approve the command, got approved=%v blockMsg=%q", approved, approveMsg)
	}

	// The consumed OTP must not work again: the command is blocked once more
	// and a DIFFERENT code is issued for the next attempt.
	_, _, reblockMsg, rehandled := handleForceRevaluate(ctx, reg, bashCall(t, bashArgs{Command: command, OTPCode: code}))
	if !rehandled {
		t.Fatalf("expected gate to handle the re-used OTP attempt")
	}
	if reblockMsg == "" {
		t.Fatalf("expected re-used OTP to be rejected (command blocked again)")
	}
	fresh := extractOTP(t, reblockMsg)
	if fresh == code {
		t.Errorf("expected a fresh code after consumption, got the same code %q", code)
	}
}

func TestHandleForceRevaluate_WrongOTPRepresentsPendingCode(t *testing.T) {
	tmp := isolateTestEnv(t)
	reg := newBashRegistry(t)
	ctx := forceRevaluateCtx()
	command := dangerousCommand(tmp)

	_, _, blockMsg, handled := handleForceRevaluate(ctx, reg, bashCall(t, bashArgs{Command: command}))
	if !handled || blockMsg == "" {
		t.Fatalf("expected first attempt to be blocked with a message, got handled=%v blockMsg=%q", handled, blockMsg)
	}
	pending := extractOTP(t, blockMsg)

	const wrongCode = "0000001"
	_, _, wrongMsg, wrongHandled := handleForceRevaluate(ctx, reg, bashCall(t, bashArgs{Command: command, OTPCode: wrongCode}))
	if !wrongHandled || wrongMsg == "" {
		t.Fatalf("expected wrong OTP attempt to stay blocked, got handled=%v blockMsg=%q", wrongHandled, wrongMsg)
	}
	if !strings.Contains(wrongMsg, pending) {
		t.Errorf("expected block message to re-present the pending code %q, got %q", pending, wrongMsg)
	}
	if pending != wrongCode && strings.Contains(wrongMsg, wrongCode) {
		t.Errorf("expected the wrong code %q not to be echoed back, got %q", wrongCode, wrongMsg)
	}

	// A wrong attempt must not burn the pending code: the correct one still
	// consumes afterwards.
	_, _, approveMsg, approved := handleForceRevaluate(ctx, reg, bashCall(t, bashArgs{Command: command, OTPCode: pending}))
	if !approved || approveMsg != "" {
		t.Fatalf("expected correct pending code to consume after a wrong attempt, got approved=%v blockMsg=%q", approved, approveMsg)
	}
}

func TestHandleForceRevaluate_WhitespaceChangeIsDifferentCommand(t *testing.T) {
	tmp := isolateTestEnv(t)
	reg := newBashRegistry(t)
	ctx := forceRevaluateCtx()
	command := dangerousCommand(tmp)

	_, _, blockMsg, handled := handleForceRevaluate(ctx, reg, bashCall(t, bashArgs{Command: command}))
	if !handled || blockMsg == "" {
		t.Fatalf("expected first attempt to be blocked with a message, got handled=%v blockMsg=%q", handled, blockMsg)
	}
	code := extractOTP(t, blockMsg)

	// A double space makes it a DIFFERENT command string: the code issued for
	// the original command must not approve it.
	altered := "rm  -rf " + filepath.Join(tmp, "build")
	_, _, alteredMsg, alteredHandled := handleForceRevaluate(ctx, reg, bashCall(t, bashArgs{Command: altered, OTPCode: code}))
	if !alteredHandled {
		t.Fatalf("expected gate to handle the whitespace-changed command")
	}
	if alteredMsg == "" {
		t.Fatalf("expected whitespace-changed command to stay blocked even with the issued code")
	}
	fresh := extractOTP(t, alteredMsg)
	if fresh == code {
		t.Errorf("expected a fresh code for the altered command, got the same code %q", code)
	}
}

// ---------------------------------------------------------------------------
// Safe commands and preserved hard refusals
// ---------------------------------------------------------------------------

func TestHandleForceRevaluate_SafeCommandPassesThrough(t *testing.T) {
	isolateTestEnv(t)
	reg := newBashRegistry(t)
	ctx := forceRevaluateCtx()

	// Empirically verified: with clean allow-lists `echo hello` is a built-in
	// safe command (RequiresConfirmation == false).
	const safeCommand = "echo hello"

	_, _, blockMsg, handled := handleForceRevaluate(ctx, reg, bashCall(t, bashArgs{Command: safeCommand}))
	if handled {
		t.Fatalf("expected safe command to pass through untouched, got handled=true blockMsg=%q", blockMsg)
	}

	// A stray otp_code on the safe path must be ignored entirely: no approval
	// flow, and no pending OTP is consumed by the pass-through.
	code := tool.IssueOTP(safeCommand)
	_, _, strayMsg, strayHandled := handleForceRevaluate(ctx, reg, bashCall(t, bashArgs{Command: safeCommand, OTPCode: code}))
	if strayHandled {
		t.Fatalf("expected safe command with stray otp_code to still pass through, got blockMsg=%q", strayMsg)
	}
	if !tool.ConsumeOTP(safeCommand, code) {
		t.Fatalf("expected the stray otp_code to NOT be consumed by the safe pass-through (it should still be pending)")
	}
	if tool.ConsumeOTP(safeCommand, code) {
		t.Fatalf("expected the code to be single-use once explicitly consumed")
	}
}

func TestHandleForceRevaluate_HardRefusalsPreserved(t *testing.T) {
	tmp := isolateTestEnv(t)
	reg := newBashRegistry(t)
	ctx := forceRevaluateCtx()

	// A cwd sibling of the temp cwd: IsSafePath rejects it (verified
	// empirically) because it escapes the process working directory.
	unsafeCwd := filepath.Join(tmp, "..", "late-outside-cwd")

	cases := []struct {
		name string
		args bashArgs
	}{
		{"bash search gate (grep)", bashArgs{Command: "grep -r foo ."}},
		{"AST hard block (cd)", bashArgs{Command: "cd " + tmp}},
		{"AST hard block (redirect)", bashArgs{Command: "echo hi > " + filepath.Join(tmp, "f")}},
		{"malformed command with redirect (parse error)", bashArgs{Command: "echo hi > " + filepath.Join(tmp, "f") + " &&"}},
		{"dangerous command with unsafe cwd", bashArgs{Command: dangerousCommand(tmp), Cwd: unsafeCwd}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, blockMsg, handled := handleForceRevaluate(ctx, reg, bashCall(t, tc.args))
			if handled {
				t.Fatalf("expected hard refusal to keep flowing to the normal path untouched, got handled=true blockMsg=%q", blockMsg)
			}
		})
	}
}

// TestHandleForceRevaluate_UnparseableRedirectNeverGetsOTP pins the regression
// fixed by hard-blocking unparseable commands that carry a hard-block
// signature. A trailing "&&" makes the command a shell syntax error, which
// used to downgrade it to plain "needs confirmation" and let the
// force-revaluate gate issue an OTP for it — so after OTP approval the command
// would fail open into the shell. The agent must NEVER see the OTP message for
// a hard-blocked command, in any form, even when it sends an otp_code: the
// parse-error hard block in ValidateBashCommand makes the gate's preservation
// check reject the command before any OTP logic, so the call keeps flowing to
// the normal path, where Execute fails closed with the plain block message.
func TestHandleForceRevaluate_UnparseableRedirectNeverGetsOTP(t *testing.T) {
	tmp := isolateTestEnv(t)
	reg := newBashRegistry(t)
	ctx := forceRevaluateCtx()

	// Genuinely unparseable: the trailing "&&" is a shell syntax error, yet
	// the raw text still carries an output redirect to a plain path.
	command := "echo hi > " + filepath.Join(tmp, "f") + " &&"

	// First attempt: the hard refusal must be preserved — the gate does not
	// handle the call, so no OTP is issued and no message is produced.
	_, _, blockMsg, handled := handleForceRevaluate(ctx, reg, bashCall(t, bashArgs{Command: command}))
	if handled {
		t.Fatalf("expected unparseable redirect command to keep its hard refusal, got handled=true blockMsg=%q", blockMsg)
	}
	if blockMsg != "" {
		t.Fatalf("expected empty block message when the gate does not handle the call, got %q", blockMsg)
	}

	// Second attempt WITH an otp_code: still no OTP flow — the code must be
	// ignored entirely, never consumed, and the call must stay unhandled.
	_, _, blockMsg2, handled2 := handleForceRevaluate(ctx, reg, bashCall(t, bashArgs{Command: command, OTPCode: "1234567"}))
	if handled2 {
		t.Fatalf("expected unparseable redirect command to stay unhandled even with an otp_code, got handled=true blockMsg2=%q", blockMsg2)
	}
	if blockMsg2 != "" {
		t.Fatalf("expected empty block message when an otp_code is offered to a hard-blocked command, got %q", blockMsg2)
	}

	// Executor-boundary guarantee: the same command fails closed at the
	// executor with the plain redirect block message (mirroring Execute's
	// first check), so the re-run on the normal path can never execute it.
	bashTool, ok := reg.Get("bash").(*tool.ShellTool)
	if !ok {
		t.Fatalf("expected the registry to hold a *tool.ShellTool under \"bash\"")
	}
	if err := bashTool.ValidateBashCommand(command, ""); err == nil {
		t.Fatalf("ValidateBashCommand(%q) error = nil, want output redirection hard block", command)
	} else if !strings.Contains(err.Error(), "Output redirection (>) is blocked") {
		t.Errorf("ValidateBashCommand(%q) error = %q, want it to contain %q", command, err.Error(), "Output redirection (>) is blocked")
	}
	// When blocked, the returned error is the block reason (the same one
	// Execute surfaces via WrapError), and it must be the plain redirect
	// message — never the OTP re-evaluate text.
	blocked, blockReason := bashTool.IsCommandBlocked(command, "")
	if !blocked {
		t.Fatalf("IsCommandBlocked(%q) = false, want true", command)
	}
	if blockReason == nil || !strings.Contains(blockReason.Error(), "Output redirection (>) is blocked") {
		t.Errorf("IsCommandBlocked(%q) block reason = %v, want the output redirection block message", command, blockReason)
	}
}

// ---------------------------------------------------------------------------
// Conversation reset
// ---------------------------------------------------------------------------

func TestShellToolResetConversationStateClearsOTPs(t *testing.T) {
	isolateTestEnv(t)

	command := "rm -rf reset-state-probe"
	code := tool.IssueOTP(command)
	if len(code) != 7 {
		t.Fatalf("expected a 7-digit pending code, got %q", code)
	}

	(&tool.ShellTool{}).ResetConversationState()

	if tool.ConsumeOTP(command, code) {
		t.Fatalf("expected pending OTP to be cleared by ResetConversationState")
	}
}
