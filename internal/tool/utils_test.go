package tool

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestCompressWithSqz(t *testing.T) {
	// Skip if sqz is not available on the system
	if _, err := exec.LookPath("sqz"); err != nil {
		t.Skip("sqz binary not found in PATH")
	}

	ctx := context.Background()
	input := []byte("hello world")
	command := "echo hello"

	output, err := CompressWithSqz(ctx, input, command)
	if err != nil {
		t.Fatalf("CompressWithSqz failed: %v", err)
	}

	if len(output) == 0 {
		t.Fatal("expected non-empty output from sqz")
	}
}

func TestCompressWithSqz_Mocked(t *testing.T) {
	// Mock sqz NOT available
	originalIsSqzAvailable := isSqzAvailable
	isSqzAvailable = func() bool { return false }
	defer func() { isSqzAvailable = originalIsSqzAvailable }()

	ctx := context.Background()
	input := []byte("hello world")
	output, err := CompressWithSqz(ctx, input, "cmd")
	if err != nil {
		t.Fatalf("expected no error when sqz not available, got %v", err)
	}
	if string(output) != string(input) {
		t.Fatalf("expected original input, got %q", string(output))
	}
}

func TestSetSqzEnabled(t *testing.T) {
	defer SetSqzEnabled(false) // Restore default

	// Test disabling
	SetSqzEnabled(false)
	if IsSqzAvailable() {
		t.Error("expected IsSqzAvailable to be false after SetSqzEnabled(false)")
	}
}

func TestLimitToolOutput(t *testing.T) {
	// Small output passes through unchanged.
	small := "hello world\n"
	if out := limit_tool_output([]byte(small)); out != small {
		t.Errorf("expected unchanged output %q, got %q", small, out)
	}

	// Input longer than maxBashOutputChars is capped with the truncation suffix.
	longLine := strings.Repeat("a", maxBashOutputChars+1000)
	out := limit_tool_output([]byte(longLine))
	if len(out) > maxBashOutputChars+len("\n... (output truncated)") {
		t.Errorf("output length %d exceeds limit %d", len(out), maxBashOutputChars)
	}
	if !strings.Contains(out, "... (output truncated)") {
		t.Error("expected output to contain truncation message")
	}

	// More than maxBashOutputLines lines is capped.
	var sb strings.Builder
	for i := 0; i < maxBashOutputLines+100; i++ {
		sb.WriteString("line\n")
	}
	out = limit_tool_output([]byte(sb.String()))
	lines := strings.Split(out, "\n")
	if len(lines) > maxBashOutputLines+1 { // maxBashOutputLines lines + truncation message
		t.Errorf("output has %d lines, expected at most %d", len(lines), maxBashOutputLines+1)
	}
	if !strings.Contains(out, "... (output truncated)") {
		t.Error("expected output to contain truncation message")
	}

	// A payload with a null byte yields the binary-detection string.
	out = limit_tool_output([]byte{0x7f, 0x45, 0x4c, 0x00, 0x01})
	if out != "(binary output detected)" {
		t.Errorf("expected %q, got %q", "(binary output detected)", out)
	}
}
