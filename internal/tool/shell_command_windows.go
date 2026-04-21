//go:build windows

package tool

import (
	"context"
	"encoding/base64"
	"os/exec"
	"sync"
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
	return exec.CommandContext(
		ctx, shell,
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", encoded,
	)
}
