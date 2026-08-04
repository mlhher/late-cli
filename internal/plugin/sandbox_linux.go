//go:build linux

package plugin

import (
	"os/exec"
)

// setCmdSysProcAttr applies Linux-specific hardening to an exec.Cmd.
//
// We previously set Cloneflags: syscall.CLONE_NEWPID here for PID
// namespace isolation, but unprivileged PID namespaces are denied by
// many kernels / container runtimes / AppArmor profiles and the
// resulting EPERM aborts hook execution outright (fork/exec: operation
// not permitted).
//
// We also considered setting NoNewPrivs (the kernel-enforced
// "no setuid/setgid/file-cap surprises" flag), but that flag lives
// only on golang.org/x/sys/unix.SysProcAttr, not on the standard
// library's syscall.SysProcAttr. Adding a new dependency is out of
// scope for this fix; until x/sys/unix is adopted, leave SysProcAttr
// unset and document the trade-off below.
//
// SECURITY POSTURE CALL-OUT:
//   Plugin hooks now run with the same privileges as the `late`
//   process and share the host PID namespace. The trust boundary
//   becomes the manifest itself — Late is not a sandbox, it's an
//   installer. Plugin authors MUST treat script content as if a user
//   could have authored it (which they could — `late plugin link`
//   points at any local directory on disk).
func setCmdSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = nil
}
