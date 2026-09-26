//go:build windows

package tool

import (
	"context"
	"encoding/base64"
	"os/exec"
	"strconv"
	"sync"
	"time"
	"unicode/utf16"
)

var (
	winShellPath     string
	winShellPathOnce sync.Once
)

func getWindowsShellPath() string {
	winShellPathOnce.Do(func() {
		if p, err := exec.LookPath("pwsh.exe"); err == nil {
			winShellPath = p
			return
		}
		if p, err := exec.LookPath("powershell.exe"); err == nil {
			winShellPath = p
			return
		}
		winShellPath = "powershell.exe"
	})
	return winShellPath
}

func encodePSCommand(command string) string {
	u16 := utf16.Encode([]rune(command))
	b := make([]byte, len(u16)*2)
	for i, r := range u16 {
		b[i*2] = byte(r)
		b[i*2+1] = byte(r >> 8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func newShellCommand(ctx context.Context, command string) *exec.Cmd {
	shell := getWindowsShellPath()
	encoded := encodePSCommand(command)
	cmd := exec.CommandContext(
		ctx, shell,
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", encoded,
	)
	// CommandContext kills only the direct child; taskkill /T /F tears down
	// the WHOLE process tree on cancellation, otherwise a grandchild that
	// inherited our stdout/stderr pipes would keep CombinedOutput blocked
	// forever even after cancel.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Best-effort: a taskkill failure (e.g. the process already exited)
		// must not mask the real outcome; WaitDelay still closes the pipes
		// below so a pipe-holding descendant can never block Wait().
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
		return nil
	}
	// If a graceful window is needed, Wait closes the pipes anyway after
	// this delay so a pipe-holding descendant can never block Wait().
	cmd.WaitDelay = 5 * time.Second
	return cmd
}
