package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"late/internal/client"
	"late/internal/common"
	"late/internal/pathutil"
	"late/internal/tool"
)

// isolateAllowlistGateEnv extends isolateTestEnv with an explicit
// XDG_CONFIG_HOME so the GLOBAL allow-list store the gate persists to always
// lands inside the test's temp dir on every platform (linux derives the OS
// config dir from $XDG_CONFIG_HOME, not $HOME).
func isolateAllowlistGateEnv(t *testing.T) string {
	t.Helper()
	tmp := isolateTestEnv(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))
	return tmp
}

// nextRecorder returns a next func counting and recording its invocations.
func nextRecorder() (func(ctx context.Context, tc client.ToolCall) (string, error), *int, *context.Context) {
	calls := 0
	var lastCtx context.Context
	return func(ctx context.Context, tc client.ToolCall) (string, error) {
		calls++
		lastCtx = ctx
		return "ok", nil
	}, &calls, &lastCtx
}

// TestHandleForceRevaluate_OTPApprovalPersistsGlobalExactAllowlist is the
// end-to-end issue #1 scenario: block → approve with OTP → the exact command
// string is persisted into the global store and a FRESH tool/registry (new
// late process) executes it with NO otp_code at all.
func TestHandleForceRevaluate_OTPApprovalPersistsGlobalExactAllowlist(t *testing.T) {
	tmp := isolateAllowlistGateEnv(t)
	command := dangerousCommand(tmp)
	ctx := forceRevaluateCtx()

	next, calls, _ := nextRecorder()
	runner := TUIConfirmMiddleware(&mockMessenger{}, newBashRegistry(t))(next)

	// First attempt: blocked with an OTP.
	blocked, err := runner(ctx, bashCall(t, bashArgs{Command: command}))
	if err != nil {
		t.Fatalf("unexpected error on first attempt: %v", err)
	}
	if tool.CountExactCommandApprovals(gateExecCwd(t), command) != 0 {
		t.Fatalf("expected no exact approval before any OTP success, got %d", tool.CountExactCommandApprovals(gateExecCwd(t), command))
	}
	code := extractOTP(t, blocked)

	// Retry with the OTP: approved, and the exact string is persisted
	// globally.
	result, err := runner(ctx, bashCall(t, bashArgs{Command: command, OTPCode: code}))
	if err != nil {
		t.Fatalf("unexpected error on OTP retry: %v", err)
	}
	if result != "ok" {
		t.Fatalf("expected the OTP retry to execute, got %q", result)
	}
	if got := tool.CountExactCommandApprovals(gateExecCwd(t), command); got != 1 {
		t.Fatalf("expected the exact command to be persisted once after the OTP approval, got count %d", got)
	}

	// A brand-new registry + middleware (a fresh late instance reloading the
	// policy from disk) must execute the exact command WITHOUT any OTP.
	freshNext, freshCalls, _ := nextRecorder()
	freshRunner := TUIConfirmMiddleware(&mockMessenger{}, newBashRegistry(t))(freshNext)
	freshResult, err := freshRunner(ctx, bashCall(t, bashArgs{Command: command}))
	if err != nil {
		t.Fatalf("unexpected error on fresh-instance replay: %v", err)
	}
	if freshResult != "ok" {
		t.Fatalf("expected the fresh instance to auto-allow the exact command, got %q", freshResult)
	}
	if *freshCalls != 1 {
		t.Fatalf("expected the fresh instance to call next exactly once, got %d", *freshCalls)
	}
	if *calls != 1 {
		t.Errorf("expected the first runner to have called next exactly once, got %d", *calls)
	}
}

// TestHandleForceRevaluate_ExactAllowlistCheckedBeforeOTPPrompt pins the gate
// check order (issue #1): an exact-allowlisted command is approved on the
// FIRST attempt, before any OTP is ever issued.
func TestHandleForceRevaluate_ExactAllowlistCheckedBeforeOTPPrompt(t *testing.T) {
	tmp := isolateAllowlistGateEnv(t)
	command := dangerousCommand(tmp)
	ctx := forceRevaluateCtx()

	// Simulate an approval that happened in ANOTHER late instance/session,
	// scoped to the cwd the gate resolves for cwd-less calls.
	if err := tool.SaveExactAllowedCommand(gateExecCwd(t), command, true); err != nil {
		t.Fatalf("SaveExactAllowedCommand failed: %v", err)
	}

	next, calls, _ := nextRecorder()
	runner := TUIConfirmMiddleware(&mockMessenger{}, newBashRegistry(t))(next)

	result, err := runner(ctx, bashCall(t, bashArgs{Command: command}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ok" {
		t.Fatalf("expected the exact-allowlisted command to execute without any OTP prompt, got %q", result)
	}
	if *calls != 1 {
		t.Fatalf("expected next to be called, got %d calls", *calls)
	}
	if tool.SessionOTPApprovals(gateExecCwd(t), command) != 0 {
		t.Errorf("expected no OTP interaction for an allowlisted command, got session approvals %d", tool.SessionOTPApprovals(gateExecCwd(t), command))
	}
}

// TestHandleForceRevaluate_InvalidOTPDoesNotAllowlist proves the allowlist
// write only happens on a VALID OTP: a wrong code keeps the command blocked
// and nothing is persisted.
func TestHandleForceRevaluate_InvalidOTPDoesNotAllowlist(t *testing.T) {
	tmp := isolateAllowlistGateEnv(t)
	command := dangerousCommand(tmp)
	ctx := forceRevaluateCtx()

	next, calls, _ := nextRecorder()
	runner := TUIConfirmMiddleware(&mockMessenger{}, newBashRegistry(t))(next)

	blocked, err := runner(ctx, bashCall(t, bashArgs{Command: command}))
	if err != nil {
		t.Fatalf("unexpected error on first attempt: %v", err)
	}
	code := extractOTP(t, blocked)
	wrongCode := strings.Repeat("9", 7)
	if wrongCode == code {
		t.Fatalf("test setup failure: wrong code matches the issued code")
	}

	reblocked, err := runner(ctx, bashCall(t, bashArgs{Command: command, OTPCode: wrongCode}))
	if err != nil {
		t.Fatalf("unexpected error on wrong-OTP retry: %v", err)
	}
	if !strings.Contains(reblocked, "Late detected") {
		t.Fatalf("expected the wrong-OTP retry to stay blocked, got %q", reblocked)
	}
	if *calls != 0 {
		t.Fatalf("expected next to never be called on invalid OTP, got %d calls", *calls)
	}
	if got := tool.CountExactCommandApprovals(gateExecCwd(t), command); got != 0 {
		t.Errorf("expected no exact approval from an invalid OTP, got count %d", got)
	}

	// The global store must not contain the exact entry at all.
	cfgDir, err := pathutil.LateConfigDir()
	if err != nil {
		t.Fatalf("failed to resolve config dir: %v", err)
	}
	data, readErr := os.ReadFile(filepath.Join(cfgDir, "allowed_commands.json"))
	if readErr == nil && strings.Contains(string(data), tool.ExactCommandKey(gateExecCwd(t), command)) {
		t.Errorf("expected the global store to not contain the exact entry")
	}
}

// TestHandleForceRevaluate_BlockMessageReportsApprovalCounts covers the gate
// message suggestion (issue #1 #4): the block message reports how often the
// exact command was approved before (from expired/removed entries), the
// session-scope approval count, and the global auto-allow note.
func TestHandleForceRevaluate_BlockMessageReportsApprovalCounts(t *testing.T) {
	tmp := isolateAllowlistGateEnv(t)
	command := dangerousCommand(tmp)
	ctx := forceRevaluateCtx()

	// Fabricate a store state that yields "blocked but approved before":
	// an exact entry with times_approved=3 that expired (past entries count
	// for the message, but no longer grant an allowance).
	cfgDir, err := pathutil.LateConfigDir()
	if err != nil {
		t.Fatalf("failed to resolve config dir: %v", err)
	}
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	store := map[string]any{
		"version": common.Version,
		"entries": map[string]any{
			tool.ExactCommandKey(gateExecCwd(t), command): map[string]any{
				"flags":          []string{"__exact__"},
				"expires_at":     "2020-01-01T00:00:00Z",
				"version":        common.Version,
				"times_approved": 3,
			},
		},
	}
	raw, err := json.Marshal(store)
	if err != nil {
		t.Fatalf("failed to marshal store fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "allowed_commands.json"), raw, 0o644); err != nil {
		t.Fatalf("failed to write store fixture: %v", err)
	}
	if got := tool.CountExactCommandApprovals(gateExecCwd(t), command); got != 3 {
		t.Fatalf("expected the fixture to count 3 prior approvals, got %d", got)
	}

	// Two earlier approvals in this session.
	tool.RecordOTPApproval(gateExecCwd(t), command)
	tool.RecordOTPApproval(gateExecCwd(t), command)

	blocked, err := runnerBlock(t, ctx, command)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	extractOTP(t, blocked) // exactly one 7-digit code despite the counters
	for _, want := range []string{
		"approved and allowlisted globally 3 time(s) before",
		"approved 2 time(s) earlier in this session",
		"auto-allowed after this approval (global",
	} {
		if !strings.Contains(blocked, want) {
			t.Errorf("expected block message to contain %q, got %q", want, blocked)
		}
	}
}

// runnerBlock runs a single bash call through a fresh middleware and returns
// the tool result (the block message).
func runnerBlock(t *testing.T, ctx context.Context, command string) (string, error) {
	t.Helper()
	next, _, _ := nextRecorder()
	runner := TUIConfirmMiddleware(&mockMessenger{}, newBashRegistry(t))(next)
	return runner(ctx, bashCall(t, bashArgs{Command: command}))
}

// TestHandleForceRevaluate_DifferentCommandStillGatedAfterApproval proves the
// global exact approval covers ONLY the exact approved string.
func TestHandleForceRevaluate_DifferentCommandStillGatedAfterApproval(t *testing.T) {
	tmp := isolateAllowlistGateEnv(t)
	command := dangerousCommand(tmp)
	other := "rm -rf " + filepath.Join(tmp, "release")
	ctx := forceRevaluateCtx()

	next, _, _ := nextRecorder()
	runner := TUIConfirmMiddleware(&mockMessenger{}, newBashRegistry(t))(next)

	blocked, err := runner(ctx, bashCall(t, bashArgs{Command: command}))
	if err != nil {
		t.Fatalf("unexpected error on first attempt: %v", err)
	}
	code := extractOTP(t, blocked)
	if _, err := runner(ctx, bashCall(t, bashArgs{Command: command, OTPCode: code})); err != nil {
		t.Fatalf("unexpected error on OTP retry: %v", err)
	}

	otherBlocked, err := runner(ctx, bashCall(t, bashArgs{Command: other}))
	if err != nil {
		t.Fatalf("unexpected error on other command: %v", err)
	}
	if !strings.Contains(otherBlocked, "Late detected") {
		t.Fatalf("expected a different command to stay gated after the exact approval, got %q", otherBlocked)
	}
	if got := tool.CountExactCommandApprovals(gateExecCwd(t), other); got != 0 {
		t.Errorf("expected no approval for the other command, got %d", got)
	}
}

// TestHandleForceRevaluate_SameCommandDifferentCwdNeedsFreshOTP pins the
// cwd scoping end to end: "rm -rf ./bin" in /tmp is a DIFFERENT command than
// the same argv in the project dir, because relative paths resolve against
// the cwd. The OTP issued in one cwd must not approve the command in another,
// a fresh code must be issued there, and the persisted allowlist entries must
// be separate per (cwd, command).
func TestHandleForceRevaluate_SameCommandDifferentCwdNeedsFreshOTP(t *testing.T) {
	tmp := isolateAllowlistGateEnv(t)
	command := dangerousCommand(tmp)
	cwdA := filepath.Join(tmp, "project-a")
	cwdB := filepath.Join(tmp, "project-b")
	for _, dir := range []string{cwdA, cwdB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("failed to create scoping cwd %q: %v", dir, err)
		}
	}
	ctx := forceRevaluateCtx()

	next, calls, _ := nextRecorder()
	runner := TUIConfirmMiddleware(&mockMessenger{}, newBashRegistry(t))(next)

	// First attempt in cwdA: blocked with an OTP bound to (cwdA, command).
	blockedA, err := runner(ctx, bashCall(t, bashArgs{Command: command, Cwd: cwdA}))
	if err != nil {
		t.Fatalf("unexpected error on first attempt: %v", err)
	}
	codeA := extractOTP(t, blockedA)

	// Re-running the SAME argv from cwdB with cwdA's code stays blocked: a
	// different cwd is a different command, and the pairing is (cwd, argv).
	blockedB, err := runner(ctx, bashCall(t, bashArgs{Command: command, Cwd: cwdB, OTPCode: codeA}))
	if err != nil {
		t.Fatalf("unexpected error on cross-cwd retry: %v", err)
	}
	if !strings.Contains(blockedB, "Late detected") {
		t.Fatalf("expected the same command in a different cwd to stay gated, got %q", blockedB)
	}
	if *calls != 0 {
		t.Fatalf("expected next to never be called for the cross-cwd retry, got %d calls", *calls)
	}
	codeB := extractOTP(t, blockedB)
	if codeB == codeA {
		t.Fatalf("expected a FRESH code for the different cwd, got the same code %q", codeA)
	}

	// The original pairing (cwdA, command, codeA) is still valid.
	if _, err := runner(ctx, bashCall(t, bashArgs{Command: command, Cwd: cwdA, OTPCode: codeA})); err != nil {
		t.Fatalf("unexpected error on retry in cwdA: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("expected next to be called exactly once for the approved retry, got %d", *calls)
	}

	// The approval is scoped to cwdA: cwdB has neither a persisted approval
	// nor a session counter, and still needs its own fresh OTP.
	if got := tool.CountExactCommandApprovals(cwdA, command); got != 1 {
		t.Errorf("expected one persisted approval for (cwdA, command), got %d", got)
	}
	if got := tool.CountExactCommandApprovals(cwdB, command); got != 0 {
		t.Errorf("expected NO persisted approval for (cwdB, command), got %d", got)
	}
	if got := tool.SessionOTPApprovals(cwdA, command); got != 1 {
		t.Errorf("session approvals for (cwdA, command) = %d, want 1", got)
	}
	if got := tool.SessionOTPApprovals(cwdB, command); got != 0 {
		t.Errorf("session approvals for (cwdB, command) = %d, want 0", got)
	}
	reblocked, err := runner(ctx, bashCall(t, bashArgs{Command: command, Cwd: cwdB}))
	if err != nil {
		t.Fatalf("unexpected error on cwdB re-attempt: %v", err)
	}
	if !strings.Contains(reblocked, "Late detected") {
		t.Fatalf("expected cwdB to need its own approval, got %q", reblocked)
	}
}

// nextRecorderFunc returns a minimal next func for runners whose call count
// is not under assertion.
func nextRecorderFunc() func(ctx context.Context, tc client.ToolCall) (string, error) {
	return func(ctx context.Context, tc client.ToolCall) (string, error) {
		return "ok", nil
	}
}

// TestHandleForceRevaluate_BlockMessageShowsExecutionCwd pins the message
// requirement: the block message names the resolved execution cwd (explicit
// cwd parameter or the process default) so the agent/human sees what the OTP
// is scoped to.
func TestHandleForceRevaluate_BlockMessageShowsExecutionCwd(t *testing.T) {
	tmp := isolateAllowlistGateEnv(t)
	command := dangerousCommand(tmp)
	ctx := forceRevaluateCtx()

	// Explicit cwd parameter: the message shows that directory (canonicalized).
	cwd := filepath.Join(tmp, "project")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("failed to create scoping cwd: %v", err)
	}
	next, _, _ := nextRecorder()
	runner := TUIConfirmMiddleware(&mockMessenger{}, newBashRegistry(t))(next)
	blocked, err := runner(ctx, bashCall(t, bashArgs{Command: command, Cwd: cwd}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(blocked, "working directory "+canonicalizeDir(t, cwd)) {
		t.Errorf("expected the block message to show the explicit cwd %q, got %q", canonicalizeDir(t, cwd), blocked)
	}

	// No cwd parameter: the message shows the process working directory the
	// call would default to.
	defaultBlocked, err := runner(ctx, bashCall(t, bashArgs{Command: command}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(defaultBlocked, "working directory "+gateExecCwd(t)) {
		t.Errorf("expected the block message to show the process cwd %q, got %q", gateExecCwd(t), defaultBlocked)
	}
}

// canonicalizeDir mirrors the key/message canonicalization of the cwd:
// absolute, symlink-resolved, cleaned.
func canonicalizeDir(t *testing.T, dir string) string {
	t.Helper()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("failed to absolutize %q: %v", dir, err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}
