package tool

import (
	"context"
	"os/exec"
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
	// Reset state after test
	originalIsSqzAvailable := isSqzAvailable
	isSqzAvailable = func() bool { return true } // Force available for this test
	defer func() {
		isSqzAvailable = originalIsSqzAvailable
		SetSqzEnabled(true) // Default back to true
	}()

	// Test disabling
	SetSqzEnabled(false)
	if IsSqzAvailable() {
		t.Error("expected IsSqzAvailable to be false after SetSqzEnabled(false)")
	}

	// Test enabling
	SetSqzEnabled(true)
	if !IsSqzAvailable() {
		t.Error("expected IsSqzAvailable to be true after SetSqzEnabled(true)")
	}
}
