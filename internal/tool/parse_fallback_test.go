package tool

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"late/internal/tool/ast"
)

// TestParseErrorHardBlock unit-tests the pure lexical fallback that runs on
// commands the AST parser could not read. It must keep hard-blocking the
// policy's hard-block signatures (cd usage, unsafe output redirection) while
// leaving everything else to the soft fail-closed confirmation path.
func TestParseErrorHardBlock(t *testing.T) {
	tests := []struct {
		name    string
		command string
		wantErr bool
		wantMsg string // substring expected in the error; "" when wantErr is false
	}{
		{
			name:    "cd with trailing &&",
			command: "cd /tmp &&",
			wantErr: true,
			wantMsg: "change directories",
		},
		{
			name:    "truncate redirect with trailing &&",
			command: "echo hi > /tmp/f &&",
			wantErr: true,
			wantMsg: "Output redirection (>) is blocked",
		},
		{
			name:    "append redirect with trailing &&",
			command: "echo hi >> /tmp/f &&",
			wantErr: true,
			wantMsg: "Output redirection (>) is blocked",
		},
		{
			name:    "clobber-all redirect with trailing &&",
			command: "echo hi &>/tmp/f &&",
			wantErr: true,
			wantMsg: "Output redirection (>) is blocked",
		},
		{
			name:    "stderr redirect with trailing &&",
			command: "echo hi 2> /tmp/f &&",
			wantErr: true,
			wantMsg: "Output redirection (>) is blocked",
		},
		{
			name:    "noclobber redirect with trailing &&",
			command: "echo hi >| /tmp/f &&",
			wantErr: true,
			wantMsg: "Output redirection (>) is blocked",
		},
		{
			name:    "fd duplication is safe",
			command: "ls 2>&1 &&",
			wantErr: false,
		},
		{
			name:    "fd duplication to stderr is safe",
			command: "ls >&2 &&",
			wantErr: false,
		},
		{
			name:    "dev null target is safe",
			command: "ls 2>/dev/null &&",
			wantErr: false,
		},
		{
			name:    "dev stderr target is safe",
			command: "ls > /dev/stderr &&",
			wantErr: false,
		},
		{
			name:    "unterminated quote has no hard-block signatures",
			command: `echo "unterminated`,
			wantErr: false,
		},
		{
			name:    "empty command",
			command: "",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parseErrorHardBlock(tt.command)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseErrorHardBlock(%q) error = %v, wantErr %v", tt.command, err, tt.wantErr)
			}
			if err == nil {
				return
			}
			if tt.wantMsg != "" && !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("parseErrorHardBlock(%q) error = %q, want to contain %q", tt.command, err.Error(), tt.wantMsg)
			}
		})
	}

	// Lock single-sourcing: the fallback must return the exact policy
	// constants, not a diverging copy of the messages.
	cdErr := parseErrorHardBlock("cd /tmp &&")
	if cdErr == nil || cdErr.Error() != ast.BlockReasonCD {
		t.Errorf("parseErrorHardBlock(\"cd /tmp &&\") = %v, want error exactly equal to ast.BlockReasonCD (%q)", cdErr, ast.BlockReasonCD)
	}
	redirectErr := parseErrorHardBlock("echo hi > /tmp/f &&")
	if redirectErr == nil || redirectErr.Error() != ast.BlockReasonRedirect {
		t.Errorf("parseErrorHardBlock(\"echo hi > /tmp/f &&\") = %v, want error exactly equal to ast.BlockReasonRedirect (%q)", redirectErr, ast.BlockReasonRedirect)
	}
}

// TestValidateBashCommand_ParseErrorKeepsHardBlocks exercises the integration
// path: unparseable commands flow through ShellTool.ValidateBashCommand /
// IsCommandBlocked / RequiresConfirmation, where hard-block signatures must
// still hard-block while unsigned input keeps the soft fail-closed contract
// (needs confirmation, not blocked).
func TestValidateBashCommand_ParseErrorKeepsHardBlocks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("parse-error fallback targets the Unix (mvdan/sh) analyzer; Windows uses the PowerShell analyzer")
	}

	tmpDir := t.TempDir()
	tool := ShellTool{}

	// Hard blocks survive the parse-error path.
	redirectCmd := fmt.Sprintf("echo hi > %s/f &&", filepath.ToSlash(tmpDir))
	err := tool.ValidateBashCommand(redirectCmd, tmpDir)
	if err == nil {
		t.Fatalf("ValidateBashCommand(%q) error = nil, want output redirection hard block", redirectCmd)
	}
	if !strings.Contains(err.Error(), "Output redirection (>) is blocked") {
		t.Errorf("ValidateBashCommand(%q) error = %q, want to contain %q", redirectCmd, err.Error(), "Output redirection (>) is blocked")
	}

	err = tool.ValidateBashCommand("cd /tmp &&", tmpDir)
	if err == nil {
		t.Fatal("ValidateBashCommand(\"cd /tmp &&\") error = nil, want cd hard block")
	}
	if !strings.Contains(err.Error(), "change directories") {
		t.Errorf("ValidateBashCommand(\"cd /tmp &&\") error = %q, want to contain %q", err.Error(), "change directories")
	}

	// Soft fail-closed contract preserved for unparseable input without
	// hard-block signatures: not blocked, but confirmation required.
	blocked, err := tool.IsCommandBlocked("ls 2>&1 &&", tmpDir)
	if err != nil {
		t.Fatalf("IsCommandBlocked(\"ls 2>&1 &&\") error = %v", err)
	}
	if blocked {
		t.Error("IsCommandBlocked(\"ls 2>&1 &&\") = true, want false (fd duplication is a safe redirect)")
	}
	if !tool.RequiresConfirmation(json.RawMessage(`{"command": "ls 2>&1 &&"}`)) {
		t.Error("RequiresConfirmation(\"ls 2>&1 &&\") = false, want true (unparseable input must fail closed to confirmation)")
	}

	// Unterminated quote: unparseable but carries no hard-block signatures.
	blocked, err = tool.IsCommandBlocked(`echo "unterminated`, tmpDir)
	if err != nil {
		t.Fatalf("IsCommandBlocked(unterminated quote) error = %v", err)
	}
	if blocked {
		t.Error(`IsCommandBlocked("echo \"unterminated") = true, want false (no hard-block signatures)`)
	}
}
