//go:build windows

package tool

import (
	"context"
	"encoding/json"
	"os"
	"late/internal/common"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
	"encoding/base64"
)

// getUnixShellPath is a shim so shell_command_test.go (which references this symbol)
// compiles on Windows without modification.
func getUnixShellPath() string {
	return "powershell.exe"
}

func approvedContextForCmdTests() context.Context {
	return context.WithValue(context.Background(), common.ToolApprovalKey, true)
}

// decodePSCommand reverses encodePSCommand for test assertions.
func decodePSCommand(encoded string) string {
	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return ""
	}
	u16 := make([]uint16, len(b)/2)
	for i := range u16 {
		u16[i] = uint16(b[i*2]) | uint16(b[i*2+1])<<8
	}
	runes := utf16.Decode(u16)
	return string(runes)
}

func TestPSShellCommand_UsesPowerShell(t *testing.T) {
	cmd := newShellCommand(context.Background(), "echo test")
	base := strings.ToLower(filepath.Base(cmd.Path))
	if base != "pwsh.exe" && base != "powershell.exe" {
		t.Fatalf("expected pwsh.exe or powershell.exe, got %q", cmd.Path)
	}
}

func TestPSShellCommand_HasRequiredFlags(t *testing.T) {
	cmd := newShellCommand(context.Background(), "echo test")
	args := strings.Join(cmd.Args, " ")
	for _, flag := range []string{"-NoProfile", "-NonInteractive", "-EncodedCommand"} {
		if !strings.Contains(args, flag) {
			t.Errorf("expected flag %q in args %q", flag, args)
		}
	}
}

func TestPSShellCommand_EncodedCommandDecodesCorrectly(t *testing.T) {
	cases := []string{
		"dir",
		`Get-ChildItem 'C:\Users\jmorales\my project'`,
		`Write-Output 'hello world'`,
		"Get-Content file.txt | Select-String pattern",
	}
	for _, command := range cases {
		cmd := newShellCommand(context.Background(), command)
		// -EncodedCommand is the last-1 arg
		args := cmd.Args
		var encodedArg string
		for i, a := range args {
			if strings.EqualFold(a, "-EncodedCommand") && i+1 < len(args) {
				encodedArg = args[i+1]
				break
			}
		}
		if encodedArg == "" {
			t.Fatalf("no -EncodedCommand arg found for %q", command)
		}
		decoded := decodePSCommand(encodedArg)
		if decoded != command {
			t.Errorf("round-trip mismatch: got %q, want %q", decoded, command)
		}
	}
}

func TestPSShellTool_WindowsSelectiveRequiresConfirmation(t *testing.T) {
	tool := ShellTool{}
	tests := []struct {
		name string
		args json.RawMessage
		want bool
	}{
		{name: "dir is safe", args: json.RawMessage(`{"command":"dir"}`), want: false},
		{name: "Get-ChildItem is safe", args: json.RawMessage(`{"command":"Get-ChildItem"}`), want: false},
		{name: "echo is safe", args: json.RawMessage(`{"command":"echo hello"}`), want: false},
		{name: "risky remove-item", args: json.RawMessage(`{"command":"Remove-Item foo.txt"}`), want: true},
		{name: "unknown command", args: json.RawMessage(`{"command":"git status"}`), want: true},
		{name: "pipeline to unknown command", args: json.RawMessage(`{"command":"Get-ChildItem | ForEach-Object { $_ }"}`), want: true},
		{name: "iex risky syntax", args: json.RawMessage(`{"command":"IEX 'Get-ChildItem'"}`), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tool.RequiresConfirmation(tt.args); got != tt.want {
				t.Fatalf("RequiresConfirmation(%s)=%v, want %v", string(tt.args), got, tt.want)
			}
		})
	}
}

func TestPSShellTool_WindowsNewPathCarveout(t *testing.T) {
	tool := ShellTool{}
	tempDir, err := os.MkdirTemp(".", "ps-newpath-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir in workspace: %v", err)
	}
	defer os.RemoveAll(tempDir)
	absTempDir, err := filepath.Abs(tempDir)
	if err != nil {
		t.Fatalf("failed to resolve temp dir path: %v", err)
	}
	newPath := filepath.Join(absTempDir, "new-folder")
	existingPath := filepath.Join(absTempDir, "existing-folder")
	if err := os.Mkdir(existingPath, 0755); err != nil {
		t.Fatalf("failed to create existing folder fixture: %v", err)
	}
	makeArgs := func(command, cwd string) json.RawMessage {
		payload, err := json.Marshal(map[string]string{
			"command": command,
			"cwd":     cwd,
		})
		if err != nil {
			t.Fatalf("failed to marshal args: %v", err)
		}
		return json.RawMessage(payload)
	}

	tests := []struct {
		name string
		args json.RawMessage
		want bool
	}{
		{
			name: "new-item new path can auto-approve",
				args: makeArgs(`New-Item -Path "`+newPath+`"`, absTempDir),
			want: false,
		},
		{
			name: "new-item existing path auto-approves (unsupervised mode)",
				args: makeArgs(`New-Item -Path "`+existingPath+`"`, absTempDir),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tool.RequiresConfirmation(tt.args); got != tt.want {
				t.Fatalf("RequiresConfirmation(%s)=%v, want %v", string(tt.args), got, tt.want)
			}
		})
	}
}

func TestPowerShellParserBackedCommandExtraction(t *testing.T) {
	got := getPowerShellBaseCommands(`Get-ChildItem 'C:\Program Files' | Select-String "go"; echo done`)
	want := []string{"get-childitem", "select-string", "echo"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("getPowerShellBaseCommands mismatch: got %v want %v", got, want)
	}

	if !containsPowerShellRiskySyntax("IEX 'Get-ChildItem'") {
		t.Fatal("expected IEX command to be treated as risky")
	}

	if containsPowerShellRiskySyntax("Get-ChildItem") {
		t.Fatal("expected simple Get-ChildItem to be non-risky")
	}

	if got := extractPowerShellTargetPath(`New-Item -Path C:\tmp\newfile.txt`); got == "" {
		t.Fatal("expected New-Item -Path target extraction to succeed")
	}
}

func TestPSShellTool_ExecuteFailsWithoutApproval(t *testing.T) {
	tool := ShellTool{}
	args := json.RawMessage(`{"command":"git status"}`)
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected missing approval error, got nil")
	}
	if !strings.Contains(err.Error(), "requires explicit approval") {
		t.Fatalf("expected approval error, got %q", err.Error())
	}
}

func TestPSShellTool_ExecuteSucceedsWithApproval(t *testing.T) {
	tool := ShellTool{}
	args := json.RawMessage(`{"command":"Write-Output hello"}`)
	out, err := tool.Execute(approvedContextForCmdTests(), args)
	if err != nil {
		t.Fatalf("approved execution failed: %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "hello") {
		t.Fatalf("expected output to contain hello, got %q", out)
	}
}

func TestPSShellTool_CallString(t *testing.T) {
	tool := ShellTool{}

	cases := []struct {
		args     json.RawMessage
		wantPfx  string
	}{
		{json.RawMessage(`{"command":"Get-ChildItem"}`), "Executing in PowerShell: Get-ChildItem"},
		{json.RawMessage(`{"command":"Write-Output hello","cwd":"C:/tmp"}`), "Executing in PowerShell: Write-Output hello in dir: C:/tmp"},
	}
	for _, c := range cases {
		got := tool.CallString(c.args)
		if got != c.wantPfx {
			t.Errorf("CallString() = %q, want %q", got, c.wantPfx)
		}
	}
}
