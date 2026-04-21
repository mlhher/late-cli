//go:build !windows

package tool

import (
	"context"
	"os/exec"
)

func newShellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "bash", "-c", command)
}
