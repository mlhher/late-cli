package tool

import (
	cryptorand "crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"
)

var (
	otpMu sync.Mutex
	// otpCodes maps the exact (cwd, command) pair — otpKey, which
	// canonicalizes the cwd — to the pending OTP. A command re-run from a
	// different working directory is a different command and never matches a
	// pending code issued for another cwd.
	otpCodes = make(map[string]string)
	// sessionOTPApprovals counts successful OTP approvals per exact (cwd,
	// command) key for the current session. It is reported in the gate block
	// message (issue #1) and reset together with the pending-OTP registry on
	// conversation reset. Guarded by otpMu.
	sessionOTPApprovals = make(map[string]int)
)

// otpKey builds the pending-OTP registry key for an exact (cwd, command)
// pair: the canonical cwd plus the RAW command parameter value, joined by
// the same NUL separator the allowlist uses. The command stays byte-for-byte
// (no boundary-whitespace trimming — that is the allowlist's rule): the
// pending-OTP registry matches the argv exactly, so " rm -rf x" and
// "rm -rf x" are different pending commands. The cwd can never contain NUL,
// so the split is unambiguous.
func otpKey(cwd, command string) string {
	return canonicalExecCwd(cwd) + exactCommandKeySeparator + command
}

// GenerateOTPCode returns a cryptographically random 7-digit code
// (zero-padded, e.g. "0042319"), drawn uniformly from 0..9999999 using
// crypto/rand (the system entropy source).
func GenerateOTPCode() string {
	n, err := cryptorand.Int(cryptorand.Reader, big.NewInt(10_000_000))
	if err != nil {
		// System entropy unavailable: fall back to a time-derived value
		// rather than failing open and auto-approving a dangerous command.
		return fmt.Sprintf("%07d", time.Now().UnixNano()%10_000_000)
	}
	return fmt.Sprintf("%07d", n.Int64())
}

// IssueOTP returns the OTP pending for the exact (cwd, command) pair,
// generating and storing a fresh one if none is pending. Re-attempts of the
// same command from the same cwd while the code is pending re-present the
// same code; the same command from a different cwd gets its own code.
func IssueOTP(cwd, command string) string {
	key := otpKey(cwd, command)
	otpMu.Lock()
	defer otpMu.Unlock()
	if code, ok := otpCodes[key]; ok {
		return code
	}
	code := GenerateOTPCode()
	otpCodes[key] = code
	return code
}

// ConsumeOTP validates code against the pending OTP for the exact (cwd,
// command) pair (byte-for-byte; any difference, even whitespace, is a
// different command, and a different cwd is always a different command). On
// success the OTP is deleted (single use) and true is returned.
func ConsumeOTP(cwd, command, code string) bool {
	key := otpKey(cwd, command)
	otpMu.Lock()
	defer otpMu.Unlock()
	pending, ok := otpCodes[key]
	if !ok || pending != code {
		return false
	}
	delete(otpCodes, key)
	return true
}

// ResetOTPRegistry drops all pending OTPs. Called on conversation reset and
// implicitly at process exit (end of agent session).
func ResetOTPRegistry() {
	otpMu.Lock()
	defer otpMu.Unlock()
	otpCodes = make(map[string]string)
	sessionOTPApprovals = make(map[string]int)
}

// RecordOTPApproval bumps the session-scoped approval counter for the exact
// (cwd, command) key and returns the new count. Called when the
// force-revaluate gate accepts a valid OTP, so the block message can report
// how often the command was already approved in this session.
func RecordOTPApproval(cwd, command string) int {
	key := otpKey(cwd, command)
	otpMu.Lock()
	defer otpMu.Unlock()
	sessionOTPApprovals[key]++
	return sessionOTPApprovals[key]
}

// SessionOTPApprovals returns the number of successful OTP approvals for the
// exact (cwd, command) key in the current session.
func SessionOTPApprovals(cwd, command string) int {
	otpMu.Lock()
	defer otpMu.Unlock()
	return sessionOTPApprovals[otpKey(cwd, command)]
}

// OTPRevaluateMessage renders the block message returned to the LLM agent
// when a dangerous command is first attempted under
// -force-revaluate-dangerous-commands. It has no cwd context, so the message
// carries no cwd scoping sentence.
func OTPRevaluateMessage(code string) string {
	return OTPRevaluateMessageDetailed("", code, 0, 0)
}

// OTPRevaluateMessageDetailed augments the block message with the execution
// cwd the gate is scoping to, the approval history of the exact (cwd,
// command) pair (issue #1, gate-message suggestion) — how many times the
// command was already approved and allowlisted before (0 when never approved
// — an exact-allowlisted command bypasses the gate entirely, so any block
// with a non-zero count means the previous approval entries expired or were
// removed), the session-scope approval count when non-zero, and the note
// that THIS approval will auto-allow the exact command in that same cwd
// globally from then on.
func OTPRevaluateMessageDetailed(cwd, code string, priorGlobalApprovals, sessionApprovals int) string {
	msg := fmt.Sprintf("Late detected that you want to execute a command potentially dangerous and destructive. Please re-evaluate your command to ensure it is safe for the current system and environment, verify the assumptions and the paths directly to avoid mistakes like symlinks or forgotten stashes, consider all the consequences, direct and indirect, and all the potential issues that the command can cause. If after this evaluation you'll decide to execute the command, run the command again with the following OTP code passed as the `otp_code` tool parameter: %s", code)
	// The gate is (cwd, command)-scoped: relative paths in the command
	// resolve against the cwd, so the OTP and the resulting approval are
	// bound to this directory. Showing it lets the agent/human see the
	// scoping and re-run with the identical cwd. An empty cwd means the
	// caller has no execution context (the legacy renderer): the sentence
	// is skipped rather than guessing a directory.
	if strings.TrimSpace(cwd) != "" {
		msg += fmt.Sprintf(" This gate applies to the command executed in working directory %s: the same command run from a different working directory is a different command and needs its own approval.", canonicalExecCwd(cwd))
	}
	if priorGlobalApprovals > 0 {
		msg += fmt.Sprintf(" This exact command has been approved and allowlisted globally %d time(s) before (that entry has since expired or was removed).", priorGlobalApprovals)
	} else {
		msg += " This exact command has never been approved and allowlisted before."
	}
	if sessionApprovals > 0 {
		msg += fmt.Sprintf(" It was approved %d time(s) earlier in this session.", sessionApprovals)
	}
	msg += " After this approval, this exact command will be auto-allowed after this approval (global persistent allow-list, same exact string in the same working directory only)."
	return msg
}

// ResetConversationState implements common.ConversationResetter: pending
// OTP codes must not carry into a new conversation.
func (t *ShellTool) ResetConversationState() { ResetOTPRegistry() }
