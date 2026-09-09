//go:build unix

package plugin

import (
	"errors"
	"os/exec"
	"syscall"
)

// prepareHookCmd applies Unix-specific process-group isolation and cancellation to an exec.Cmd.
//
// On Linux and macOS, hooks run in their own process group (Setpgid: true).
// When the execution context is cancelled or times out, cmd.Cancel terminates
// the entire process group via SIGKILL to ensure runaway descendant/child processes
// are terminated as well as the parent process.
func prepareHookCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	cmd.Cancel = func() error { return terminateHookCmd(cmd) }
}

// terminateHookCmd kills the process group even if its leader has already
// exited. That latter case occurs when WaitDelay fires because a background
// descendant still holds one of the hook's inherited pipes open.
func terminateHookCmd(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	// Negative PID sends SIGKILL to the entire process group.
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
