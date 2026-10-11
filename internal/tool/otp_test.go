package tool

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
)

// otpTestCwd is the cwd argument for registry-level OTP tests: "" is
// canonicalized by ExactCommandKey to the process working directory, so the
// whole test binary shares one stable cwd key. Cross-cwd binding is covered
// by TestOTPBinding_IsCwdScoped.
const otpTestCwd = ""

func TestGenerateOTPCodeFormatAndEntropy(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 200; i++ {
		code := GenerateOTPCode()
		if len(code) != 7 {
			t.Fatalf("expected 7-character code, got %q (length %d)", code, len(code))
		}
		for _, r := range code {
			if r < '0' || r > '9' {
				t.Fatalf("expected only digits, got %q", code)
			}
		}
		seen[code] = struct{}{}
	}
	if len(seen) <= 1 {
		t.Fatalf("expected more than one distinct code in 200 draws, got %d", len(seen))
	}
}

func TestIssueOTPIsStableWhilePending(t *testing.T) {
	ResetOTPRegistry()
	t.Cleanup(func() { ResetOTPRegistry() })

	command := "rm -rf build"
	first := IssueOTP(otpTestCwd, command)
	if len(first) != 7 {
		t.Fatalf("expected 7-character pending code, got %q", first)
	}
	second := IssueOTP(otpTestCwd, command)
	if second != first {
		t.Fatalf("expected pending code to be stable, got %q then %q", first, second)
	}

	ResetOTPRegistry()
	afterReset := IssueOTP(otpTestCwd, command)
	if len(afterReset) != 7 {
		t.Fatalf("expected fresh 7-character code after reset, got %q", afterReset)
	}
}

func TestConsumeOTPSingleUse(t *testing.T) {
	ResetOTPRegistry()
	t.Cleanup(func() { ResetOTPRegistry() })

	command := "rm -rf x"
	code := IssueOTP(otpTestCwd, command)

	if !ConsumeOTP(otpTestCwd, command, code) {
		t.Fatalf("expected first consume with correct code to succeed")
	}
	if ConsumeOTP(otpTestCwd, command, code) {
		t.Fatalf("expected second consume with same code to fail (single use)")
	}
}

func TestConsumeOTPWrongCodeKeepsPending(t *testing.T) {
	ResetOTPRegistry()
	t.Cleanup(func() { ResetOTPRegistry() })

	command := "rm -rf x"
	pending := IssueOTP(otpTestCwd, command)

	if ConsumeOTP(otpTestCwd, command, "0000001") {
		t.Fatalf("expected consume with wrong code to fail")
	}
	if stillPending := IssueOTP(otpTestCwd, command); stillPending != pending {
		t.Fatalf("expected pending code %q to survive a wrong attempt, got %q", pending, stillPending)
	}
	if !ConsumeOTP(otpTestCwd, command, pending) {
		t.Fatalf("expected correct code to consume after a wrong attempt")
	}
}

func TestConsumeOTPCommandStringMustMatchExactly(t *testing.T) {
	ResetOTPRegistry()
	t.Cleanup(func() { ResetOTPRegistry() })

	keyCommand := "rm -rf x"
	validCode := IssueOTP(otpTestCwd, keyCommand)

	variants := []struct {
		name    string
		command string
	}{
		{"double space after first token", "rm  -rf x"},
		{"double space before last token", "rm -rf  x"},
		{"leading space", " rm -rf x"},
		{"trailing space", "rm -rf x "},
		{"appended otp flag", "rm -rf x -otp-code 1234567"},
	}
	for _, tc := range variants {
		if ConsumeOTP(otpTestCwd, tc.command, validCode) {
			t.Fatalf("%s: expected consume to fail for command %q", tc.name, tc.command)
		}
	}

	if !ConsumeOTP(otpTestCwd, keyCommand, validCode) {
		t.Fatalf("expected original command %q to still consume after failed variants", keyCommand)
	}
	if ConsumeOTP(otpTestCwd, "totally different", validCode) {
		t.Fatalf("expected consume to fail for a command that never had an OTP")
	}
}

func TestConsumeOTPUnknownCommandFails(t *testing.T) {
	ResetOTPRegistry()
	t.Cleanup(func() { ResetOTPRegistry() })

	if ConsumeOTP(otpTestCwd, "git push --force-with-lease", "1234567") {
		t.Fatalf("expected consume for command without a pending OTP to fail")
	}
}

func TestResetOTPRegistryClearsPending(t *testing.T) {
	ResetOTPRegistry()
	t.Cleanup(func() { ResetOTPRegistry() })

	firstCommand := "git push --force"
	secondCommand := "kubectl delete namespace prod"
	firstOld := IssueOTP(otpTestCwd, firstCommand)
	secondOld := IssueOTP(otpTestCwd, secondCommand)

	ResetOTPRegistry()

	firstNew := IssueOTP(otpTestCwd, firstCommand)
	secondNew := IssueOTP(otpTestCwd, secondCommand)
	if len(firstNew) != 7 || len(secondNew) != 7 {
		t.Fatalf("expected fresh 7-character codes after reset, got %q and %q", firstNew, secondNew)
	}
	if ConsumeOTP(otpTestCwd, firstCommand, firstOld) {
		t.Fatalf("expected old code for %q to be rejected after reset", firstCommand)
	}
	if ConsumeOTP(otpTestCwd, secondCommand, secondOld) {
		t.Fatalf("expected old code for %q to be rejected after reset", secondCommand)
	}
}

func TestConsumeOTPConcurrentSingleUse(t *testing.T) {
	ResetOTPRegistry()
	t.Cleanup(func() { ResetOTPRegistry() })

	command := "rm -rf race"
	code := IssueOTP(otpTestCwd, command)

	const goroutines = 50
	var successes atomic.Int64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if ConsumeOTP(otpTestCwd, command, code) {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := successes.Load(); got != 1 {
		t.Fatalf("expected exactly 1 successful consume across %d goroutines, got %d", goroutines, got)
	}
}

func TestShellToolParametersIncludeOTPCode(t *testing.T) {
	params := (&ShellTool{}).Parameters()
	if !json.Valid(params) {
		t.Fatalf("Parameters() is not valid JSON: %s", string(params))
	}

	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(params, &schema); err != nil {
		t.Fatalf("failed to unmarshal Parameters(): %v", err)
	}
	if _, ok := schema.Properties["otp_code"]; !ok {
		t.Fatalf("expected otp_code property in Parameters(), got: %s", string(params))
	}
	if len(schema.Required) != 1 || schema.Required[0] != "command" {
		t.Fatalf("expected required to be exactly [\"command\"], got %v", schema.Required)
	}
}
