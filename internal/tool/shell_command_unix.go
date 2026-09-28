//go:build !windows

package tool

import (
	"context"
	"os/exec"
	"sync"
	"syscall"
	"time"
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
	cmd := exec.CommandContext(ctx, getUnixShellPath(), "-c", command)

	// Run the shell in its own process group and kill the whole group on
	// cancellation. exec.CommandContext only signals the direct child, so a
	// grandchild that inherited stdout/stderr (the pipes Wait reads) would
	// survive the shell and block Wait forever; a group-wide SIGKILL reaps the
	// entire tree and WaitDelay (below) caps any pipe still held afterwards.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative PID targets the process group; ESRCH means the group is
		// already gone, which is success for a kill.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			return err
		}
		return nil
	}
	// Give up waiting on pipe-holding descendants 5s after the group kill so
	// Wait (and therefore CombinedOutput) always returns.
	cmd.WaitDelay = 5 * time.Second

	return cmd
}
