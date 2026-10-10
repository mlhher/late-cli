package tool

import (
	cryptorand "crypto/rand"
	"fmt"
	"math/big"
	"sync"
	"time"
)

var (
	otpMu    sync.Mutex
	otpCodes = make(map[string]string) // exact command parameter value -> pending OTP
)

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

// IssueOTP returns the OTP pending for the exact command string, generating
// and storing a fresh one if none is pending. Re-attempts of the same
// command while the code is pending re-present the same code.
func IssueOTP(command string) string {
	otpMu.Lock()
	defer otpMu.Unlock()
	if code, ok := otpCodes[command]; ok {
		return code
	}
	code := GenerateOTPCode()
	otpCodes[command] = code
	return code
}

// ConsumeOTP validates code against the pending OTP for the exact command
// string (byte-for-byte; any difference, even whitespace, is a different
// command). On success the OTP is deleted (single use) and true is returned.
func ConsumeOTP(command, code string) bool {
	otpMu.Lock()
	defer otpMu.Unlock()
	pending, ok := otpCodes[command]
	if !ok || pending != code {
		return false
	}
	delete(otpCodes, command)
	return true
}

// ResetOTPRegistry drops all pending OTPs. Called on conversation reset and
// implicitly at process exit (end of agent session).
func ResetOTPRegistry() {
	otpMu.Lock()
	defer otpMu.Unlock()
	otpCodes = make(map[string]string)
}

// OTPRevaluateMessage renders the block message returned to the LLM agent
// when a dangerous command is first attempted under
// -force-revaluate-dangerous-commands.
func OTPRevaluateMessage(code string) string {
	return fmt.Sprintf("Late detected that you want to execute a command potentially dangerous and destructive. Please re-evaluate your command to ensure it is safe for the current system and environment, verify the assumptions and the paths directly to avoid mistakes like symlinks or forgotten stashes, consider all the consequences, direct and indirect, and all the potential issues that the command can cause. If after this evaluation you'll decide to execute the command, run the command again with the following OTP code passed as the `otp_code` tool parameter: %s", code)
}

// ResetConversationState implements common.ConversationResetter: pending
// OTP codes must not carry into a new conversation.
func (t *ShellTool) ResetConversationState() { ResetOTPRegistry() }
