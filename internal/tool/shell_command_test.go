//go:build !windows

package tool

import (
	"context"
	"testing"
)

func TestNewShellCommand(t *testing.T) {
	expectedShell := getUnixShellPath()

	tests := []struct {
		name         string
		mockSqz      bool
		expectedCmd  string
	}{
		{
			name:        "sqz not available",
			mockSqz:     false,
			expectedCmd: "echo test",
		},
		{
			name:        "sqz available (should not add pipe here anymore)",
			mockSqz:     true,
			expectedCmd: "echo test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mock the IsSqzAvailable function
			originalIsSqzAvailable := isSqzAvailable
			defer func() { isSqzAvailable = originalIsSqzAvailable }()
			isSqzAvailable = func() bool { return tt.mockSqz }

			cmd := newShellCommand(context.Background(), "echo test")

			if cmd.Path != expectedShell {
				t.Fatalf("expected cmd.Path %q, got %q", expectedShell, cmd.Path)
			}
			if len(cmd.Args) < 3 {
				t.Fatalf("expected at least 3 args for unix shell command, got %v", cmd.Args)
			}
			if cmd.Args[1] != "-c" {
				t.Fatalf("expected cmd.Args[1] to be -c, got %q", cmd.Args[1])
			}
			if cmd.Args[2] != tt.expectedCmd {
				t.Fatalf("expected cmd.Args[2] to be %q, got %q", tt.expectedCmd, cmd.Args[2])
			}
		})
	}
}
