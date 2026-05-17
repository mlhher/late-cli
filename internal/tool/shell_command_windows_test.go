//go:build windows

package tool

import (
	"context"
	"testing"
)

func TestNewShellCommand(t *testing.T) {
	expectedShell := getWindowsShellPath()

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

			// Validate arguments based on how it's built in shell_command_windows.go
			// Expected args: shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand", <encoded_command>
			if len(cmd.Args) != 7 {
				t.Fatalf("expected 7 args for windows shell command, got %v (%d args)", cmd.Args, len(cmd.Args))
			}
			if cmd.Args[1] != "-NoProfile" || cmd.Args[2] != "-NonInteractive" || cmd.Args[3] != "-ExecutionPolicy" || cmd.Args[4] != "Bypass" || cmd.Args[5] != "-EncodedCommand" {
				t.Fatalf("unexpected arguments: %v", cmd.Args[1:6])
			}

			expectedEncoded := encodePSCommand(tt.expectedCmd)
			if cmd.Args[6] != expectedEncoded {
				t.Fatalf("expected encoded command %q, got %q", expectedEncoded, cmd.Args[6])
			}
		})
	}
}
