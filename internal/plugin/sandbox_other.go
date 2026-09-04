//go:build !linux

package plugin

import "os/exec"

// setCmdSysProcAttr is a no-op on non-Linux platforms where SysProcAttr
// fields like NoNewPrivs and clone flags are either unavailable or
// inapplicable.
func setCmdSysProcAttr(cmd *exec.Cmd) {
	// no-op
}
