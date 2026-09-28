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

	// exec.CommandContext kills only the direct PowerShell child; descendants
	// that inherited the output pipes would keep Wait blocked forever after the
	// shell itself is gone. taskkill /T walks the process tree instead.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Best-effort tree kill: taskkill errors are ignored because WaitDelay
		// (below) still guarantees Wait returns.
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
		return nil
	}
	// Grace period for descendants that keep the output pipes open after the
	// kill, so Wait (and therefore CombinedOutput) always returns.
	cmd.WaitDelay = 5 * time.Second

	return cmd
}
