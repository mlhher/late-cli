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

func getUnixShellPath() string {
	unixShellPathOnce.Do(func() {
		if shellPath, err := exec.LookPath("bash"); err == nil {
			unixShellPath = shellPath
			return
		}
		if shellPath, err := exec.LookPath("sh"); err == nil {
			unixShellPath = shellPath
			return
		}
		unixShellPath = "sh"
	})
	return unixShellPath
}

func newShellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, getUnixShellPath(), "-c", command)
}
