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
	// Run bash in its own process group and kill the WHOLE group on
	// cancellation: CommandContext kills only the direct child, and a
	// grandchild that inherited our stdout/stderr pipes would keep
	// CombinedOutput blocked forever even after cancel.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// ESRCH (process already exited) is not a cancel failure: a command
		// finishing exactly at cancellation must not surface "process
		// already finished" from Wait as the tool error.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			return err
		}
		return nil
	}
	// If a graceful window is needed, Wait closes the pipes anyway after
	// this delay so a pipe-holding grandchild can never block Wait().
	cmd.WaitDelay = 5 * time.Second
	return cmd
}
