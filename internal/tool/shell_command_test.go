//go:build !windows

package tool

import (
	"context"
	"testing"
)

func TestNewShellCommand(t *testing.T) {
	cmd := newShellCommand(context.Background(), "echo test")

	expectedShell := getUnixShellPath()
	if cmd.Path != expectedShell {
		t.Fatalf("expected cmd.Path %q, got %q", expectedShell, cmd.Path)
	}
	if len(cmd.Args) < 3 {
		t.Fatalf("expected at least 3 args for unix shell command, got %v", cmd.Args)
	}
	if cmd.Args[1] != "-c" {
		t.Fatalf("expected cmd.Args[1] to be -c, got %q", cmd.Args[1])
	}
	if cmd.Args[2] != "echo test" {
		t.Fatalf("expected cmd.Args[2] to be original command, got %q", cmd.Args[2])
	}
}
