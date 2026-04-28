package ast

import (
"bufio"
"bytes"
_ "embed"
"encoding/base64"
"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
	"unicode/utf16"
)

//go:embed ps_bridge.ps1
var psBridgeScript []byte

// WindowsParser implements Parser for PowerShell using a persistent pwsh
// bridge process that invokes System.Management.Automation.Language.Parser.
// The bridge NEVER executes the command; it only parses and emits JSON IR.
//
// The first Parse call starts the bridge process; subsequent calls reuse it.
// On any transport failure the bridge is killed and restarted on the next call.
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

winEncodedBridge     string
winEncodedBridgeOnce sync.Once
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

// getBridgeEncoded returns the cached base64-encoded bridge script.
func getBridgeEncoded() string {
winEncodedBridgeOnce.Do(func() {
winEncodedBridge = encodePSScript(psBridgeScript)
})
return winEncodedBridge
}

const maxCommandBytes = 65536

// sanitizeCommand rejects inputs that could corrupt the bridge transport:
// null bytes or commands exceeding the size guard.
func sanitizeCommand(command string) error {
if len(command) > maxCommandBytes {
return fmt.Errorf("command exceeds %d byte limit", maxCommandBytes)
}
for _, r := range command {
if r == '\x00' {
return fmt.Errorf("command contains null byte")
}
}
return nil
}

// ---- persistent bridge process ----

// bridgeProcess holds a running pwsh bridge and its I/O handles.
// All fields are immutable after construction except dead, which is set
// under mu before any shutdown and never cleared.
type bridgeProcess struct {
mu     sync.Mutex
cmd    *exec.Cmd
stdin  io.WriteCloser
stdout *bufio.Reader
dead   bool
}

var (
globalBridge   *bridgeProcess
globalBridgeMu sync.Mutex
)

// getOrStartBridge returns the active bridge, starting a new one if necessary.
func getOrStartBridge() (*bridgeProcess, error) {
globalBridgeMu.Lock()
defer globalBridgeMu.Unlock()
if globalBridge != nil {
return globalBridge, nil
}
bp, err := startBridgeProcess()
if err != nil {
return nil, err
}
globalBridge = bp
return bp, nil
}

// invalidateBridge clears globalBridge if it still points to bp.
func invalidateBridge(bp *bridgeProcess) {
globalBridgeMu.Lock()
if globalBridge == bp {
globalBridge = nil
}
globalBridgeMu.Unlock()
}

// startBridgeProcess spawns a fresh pwsh process running the bridge loop.
func startBridgeProcess() (*bridgeProcess, error) {
shell := getWindowsShellPath()
if shell == "" {
return nil, fmt.Errorf("ast/windows: pwsh not available")
}
cmd := exec.Command(
shell,
"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
"-EncodedCommand", getBridgeEncoded(),
)
stdinPipe, err := cmd.StdinPipe()
if err != nil {
return nil, fmt.Errorf("ast/windows: stdin pipe: %w", err)
}
stdoutPipe, err := cmd.StdoutPipe()
if err != nil {
return nil, fmt.Errorf("ast/windows: stdout pipe: %w", err)
}
cmd.Stderr = io.Discard
if err := cmd.Start(); err != nil {
return nil, fmt.Errorf("ast/windows: start bridge: %w", err)
}
return &bridgeProcess{
cmd:    cmd,
stdin:  stdinPipe,
stdout: bufio.NewReader(stdoutPipe),
}, nil
}

const bridgeCallTimeout = 15 * time.Second

// roundTrip sends one JSON request to the bridge and reads back one JSON line.
// Must be called with bp.mu held.
func (bp *bridgeProcess) roundTrip(command string) ([]byte, error) {
req, err := json.Marshal(struct {
Cmd string `json:"cmd"`
}{Cmd: command})
if err != nil {
return nil, fmt.Errorf("marshal request: %w", err)
}
req = append(req, '\n')

type result struct {
data []byte
err  error
}
ch := make(chan result, 1)
go func() {
if _, werr := bp.stdin.Write(req); werr != nil {
ch <- result{nil, fmt.Errorf("write: %w", werr)}
return
}
line, rerr := bp.stdout.ReadBytes('\n')
ch <- result{bytes.TrimSpace(line), rerr}
}()

select {
case r := <-ch:
return r.data, r.err
case <-time.After(bridgeCallTimeout):
return nil, fmt.Errorf("bridge call timed out after %s", bridgeCallTimeout)
}
}

// ---- public API ----

// Parse invokes the persistent PowerShell bridge, with one automatic restart
// attempt on transport failure.
//
// Fail-closed guarantee: any invocation error, transport error, or schema
// mismatch causes a ParsedIR with ReasonSyntaxError to be returned along with
// a non-nil error. Callers MUST treat this as requiring confirmation.
func (w *WindowsParser) Parse(command string) (ParsedIR, error) {
ir := emptyIR(PlatformWindows)

if err := sanitizeCommand(command); err != nil {
ir.ParseErrors = append(ir.ParseErrors, err.Error())
ir.RiskFlags = appendUniqueRC(ir.RiskFlags, ReasonSyntaxError)
return ir, fmt.Errorf("ast/windows: %w", err)
}

for attempt := 0; attempt < 2; attempt++ {
bp, err := getOrStartBridge()
if err != nil {
ir.ParseErrors = append(ir.ParseErrors, err.Error())
ir.RiskFlags = appendUniqueRC(ir.RiskFlags, ReasonSyntaxError)
return ir, err
}

bp.mu.Lock()
if bp.dead {
bp.mu.Unlock()
continue
}
raw, callErr := bp.roundTrip(command)
if callErr != nil {
bp.dead = true
bp.mu.Unlock()
invalidateBridge(bp)
_ = bp.cmd.Process.Kill()
if attempt == 0 {
continue
}
msg := callErr.Error()
ir.ParseErrors = append(ir.ParseErrors, msg)
ir.RiskFlags = appendUniqueRC(ir.RiskFlags, ReasonSyntaxError)
return ir, fmt.Errorf("ast/windows: %s", msg)
}
bp.mu.Unlock()

return unmarshalBridgeResponse(raw)
}

// Both attempts failed (dead bridge + failed restart).
ir.ParseErrors = append(ir.ParseErrors, "bridge unavailable after restart")
ir.RiskFlags = appendUniqueRC(ir.RiskFlags, ReasonSyntaxError)
return ir, fmt.Errorf("ast/windows: bridge unavailable")
}

// unmarshalBridgeResponse decodes the raw JSON line emitted by the bridge.
func unmarshalBridgeResponse(raw []byte) (ParsedIR, error) {
ir := emptyIR(PlatformWindows)

if len(raw) == 0 {
ir.ParseErrors = append(ir.ParseErrors, "bridge emitted empty output")
ir.RiskFlags = appendUniqueRC(ir.RiskFlags, ReasonSyntaxError)
return ir, fmt.Errorf("ast/windows: empty bridge output")
}

var payload struct {
Version     string   `json:"version"`
Platform    string   `json:"platform"`
Commands    []string `json:"commands"`
Operators   []string `json:"operators"`
Redirects   []string `json:"redirects"`
Expansions  []string `json:"expansions"`
RiskFlags   []string `json:"risk_flags"`
ParseErrors []string `json:"parse_errors"`
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
ReasonInvokeExpr, ReasonCd, ReasonSyntaxError, ReasonNewPath:
return true
}
return false
}
