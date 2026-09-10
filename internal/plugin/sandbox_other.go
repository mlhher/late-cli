//go:build !unix

package plugin

import (
	"errors"
	"os"
	"os/exec"
)

// prepareHookCmd is a no-op fallback on non-Unix platforms.
// cmd.WaitDelay in hooks.go ensures that child processes holding pipes
// will still timeout and unblock execution.
func prepareHookCmd(cmd *exec.Cmd) {
	// no-op
}

// terminateHookCmd can only terminate the direct process on non-Unix
// platforms. WaitDelay still guarantees that inherited pipes are closed and
// runHook returns; Windows needs a Job Object for full descendant cleanup.
func terminateHookCmd(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := cmd.Process.Kill()
	if err == nil || errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
