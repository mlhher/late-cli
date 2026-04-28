package ast

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"encoding/base64"
)

//go:embed ps_bridge.ps1
var psBridgeScript []byte

// WindowsParser implements Parser for PowerShell using an out-of-process pwsh
// bridge that invokes System.Management.Automation.Language.Parser::ParseInput.
// The bridge NEVER executes the command; it only parses and emits JSON IR.
//
// On non-Windows hosts (or when pwsh is unavailable) Parse fails closed:
// it returns a ParsedIR with ReasonSyntaxError and a non-nil error.
type WindowsParser struct {
	// Cwd is the working directory context used for path-resolution heuristics
	// in the policy engine. The bridge script itself does not use it.
	Cwd string
}

var (
	winPSPath     string
	winPSPathOnce sync.Once
)

func getWindowsShellPath() string {
	winPSPathOnce.Do(func() {
		if p, err := exec.LookPath("pwsh.exe"); err == nil {
			winPSPath = p
			return
		}
		if p, err := exec.LookPath("powershell.exe"); err == nil {
			winPSPath = p
			return
		}
		winPSPath = ""
	})
	return winPSPath
}

// encodePSScript base64-encodes a PowerShell script for -EncodedCommand.
func encodePSScript(script []byte) string {
	u16 := utf16.Encode([]rune(string(script)))
	b := make([]byte, len(u16)*2)
	for i, r := range u16 {
		b[i*2] = byte(r)
		b[i*2+1] = byte(r >> 8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// Parse invokes the embedded PowerShell bridge out-of-process, feeds it the
// command string via stdin, and unmarshals the resulting JSON into a ParsedIR.
//
// Fail-closed guarantee: any invocation error, transport error, or schema
// mismatch causes a ParsedIR with ReasonSyntaxError to be returned along with
// a non-nil error. Callers MUST treat this as requiring confirmation.
func (w *WindowsParser) Parse(command string) (ParsedIR, error) {
	ir := emptyIR(PlatformWindows)

	shell := getWindowsShellPath()
	if shell == "" {
		ir.ParseErrors = append(ir.ParseErrors, "pwsh/powershell not found in PATH")
		ir.RiskFlags = appendUniqueRC(ir.RiskFlags, ReasonSyntaxError)
		return ir, fmt.Errorf("ast/windows: pwsh not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	encoded := encodePSScript(psBridgeScript)
	cmd := exec.CommandContext(
		ctx, shell,
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", encoded,
	)
	cmd.Stdin = strings.NewReader(command)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := fmt.Sprintf("bridge process error: %v", err)
		if s := strings.TrimSpace(stderr.String()); s != "" {
			msg += ": " + s
		}
		ir.ParseErrors = append(ir.ParseErrors, msg)
		ir.RiskFlags = appendUniqueRC(ir.RiskFlags, ReasonSyntaxError)
		return ir, fmt.Errorf("ast/windows: %s", msg)
	}

	raw := bytes.TrimSpace(stdout.Bytes())
	if len(raw) == 0 {
		ir.ParseErrors = append(ir.ParseErrors, "bridge emitted empty output")
		ir.RiskFlags = appendUniqueRC(ir.RiskFlags, ReasonSyntaxError)
		return ir, fmt.Errorf("ast/windows: empty bridge output")
	}

	// Unmarshal into a loose shape first to validate the version field.
	var payload struct {
		Version     string       `json:"version"`
		Platform    string       `json:"platform"`
		Commands    []string     `json:"commands"`
		Operators   []string     `json:"operators"`
		Redirects   []string     `json:"redirects"`
		Expansions  []string     `json:"expansions"`
		RiskFlags   []string     `json:"risk_flags"`
		ParseErrors []string     `json:"parse_errors"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		ir.ParseErrors = append(ir.ParseErrors, fmt.Sprintf("bridge JSON decode error: %v", err))
		ir.RiskFlags = appendUniqueRC(ir.RiskFlags, ReasonSyntaxError)
		return ir, fmt.Errorf("ast/windows: %w", err)
	}

	// Strict schema version check — reject unknown/malformed payloads.
	if payload.Version != IRVersion {
		msg := fmt.Sprintf("bridge IR version mismatch: got %q, want %q", payload.Version, IRVersion)
		ir.ParseErrors = append(ir.ParseErrors, msg)
		ir.RiskFlags = appendUniqueRC(ir.RiskFlags, ReasonSyntaxError)
		return ir, fmt.Errorf("ast/windows: %s", msg)
	}

	// Convert string risk flags to typed ReasonCode values, reject unknowns.
	riskCodes := make([]ReasonCode, 0, len(payload.RiskFlags))
	for _, rf := range payload.RiskFlags {
		rc := ReasonCode(rf)
		if !isKnownReasonCode(rc) {
			ir.ParseErrors = append(ir.ParseErrors, fmt.Sprintf("unknown risk flag %q from bridge", rf))
			ir.RiskFlags = appendUniqueRC(ir.RiskFlags, ReasonSyntaxError)
			return ir, fmt.Errorf("ast/windows: unknown risk flag %q", rf)
		}
		riskCodes = appendUniqueRC(riskCodes, rc)
	}

	// Populate final IR — all slices guaranteed non-nil by emptyIR.
	ir.Commands = nilToEmpty(payload.Commands)
	ir.Operators = nilToEmpty(payload.Operators)
	ir.Redirects = nilToEmpty(payload.Redirects)
	ir.Expansions = nilToEmpty(payload.Expansions)
	ir.RiskFlags = riskCodes
	ir.ParseErrors = nilToEmpty(payload.ParseErrors)

	return ir, nil
}

func nilToEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// isKnownReasonCode validates that rc is one of the defined ReasonCode constants.
func isKnownReasonCode(rc ReasonCode) bool {
	switch rc {
	case ReasonOperator, ReasonRedirect, ReasonExpansion, ReasonSubshell,
		ReasonInvokeExpr, ReasonCd, ReasonSyntaxError, ReasonNewPath, ReasonAllowlisted:
		return true
	}
	return false
}
