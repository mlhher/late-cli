//go:build !windows

package tool

import (
	"context"
	"os/exec"
	"sync"
)

var (
	unixShellPath     string
	unixShellPathOnce sync.Once
)

func detectedUnixShellPath() string {
	unixShellPathOnce.Do(func() {
		unixShellPath = "bash"
		if _, err := exec.LookPath(unixShellPath); err != nil {
			unixShellPath = "sh"
		}
	})
	return unixShellPath
}

func newShellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, detectedUnixShellPath(), "-c", command)
}
