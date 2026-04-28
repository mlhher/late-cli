// Package ast provides a platform-neutral parser interface, a shared parsed
// intermediate representation (ParsedIR), and a policy engine that operates
// exclusively on the compact IR. It is the foundation for AST-backed command
// analysis across Unix and Windows shells.
package ast

import (
	"encoding/json"
	"fmt"
)

// IRVersion is the schema version for ParsedIR. All adapters MUST set this
// field to IRVersion when emitting a ParsedIR. The policy engine and consumers
// MUST reject any payload whose version does not match.
const IRVersion = "1"

// Platform identifies the shell environment a ParsedIR was produced for.
type Platform string

const (
	PlatformUnix    Platform = "unix"
	PlatformWindows Platform = "windows"
)

// ReasonCode is a normalized, deterministic policy signal emitted by adapters
// and consumed by the policy engine. Reason codes MUST be stable across releases;
// add new codes rather than renaming existing ones.
type ReasonCode string

const (
	// ReasonOperator indicates a shell operator (|, &&, ||, ;, control-flow
	// keyword) was detected. Pipelines between safe commands may override this.
	ReasonOperator ReasonCode = "operator"

	// ReasonRedirect indicates an output redirection to a non-safe target was
	// detected (e.g. '>' to a real file rather than /dev/null).
	ReasonRedirect ReasonCode = "redirect"

	// ReasonExpansion indicates a variable/parameter expansion was detected
	// (e.g. $VAR, ${VAR}, $(...), $(( )) on the Unix side; $var on Windows).
	ReasonExpansion ReasonCode = "expansion"

	// ReasonSubshell indicates a subshell, command substitution, or process
	// substitution was detected.
	ReasonSubshell ReasonCode = "subshell"

	// ReasonInvokeExpr indicates a dynamic evaluation construct that can hide
	// execution intent was detected (e.g. Invoke-Expression / iex on Windows).
	ReasonInvokeExpr ReasonCode = "invoke_expression"

	// ReasonCd indicates a 'cd' command was detected. The policy engine blocks
	// this and directs callers to use the cwd parameter instead.
	ReasonCd ReasonCode = "cd"

	// ReasonSyntaxError indicates the command could not be fully parsed. The
	// policy engine fails closed when this flag is present.
	ReasonSyntaxError ReasonCode = "syntax_error"

	// ReasonNewPath indicates the command creates a new path inside an allowed
	// root. The policy engine treats this as NeedsConfirmation; callers with
	// cwd context may downgrade to auto-approve when the target is within scope.
	ReasonNewPath ReasonCode = "new_path"
)

// ParsedIR is the compact, JSON-safe intermediate representation emitted by
// all platform adapters. It MUST NOT contain raw AST nodes—only normalized
// signals suitable for deterministic, platform-neutral policy decisions.
//
// All slice fields are de-duplicated and may be empty but never nil when
// produced by an adapter.
type ParsedIR struct {
	// Version MUST equal IRVersion; consumers MUST reject non-matching versions.
	Version string `json:"version"`

	// Platform identifies which adapter produced this IR.
	Platform Platform `json:"platform"`

	// Commands holds the base command/cmdlet names found in the input
	// (e.g. ["ls", "grep"], ["Get-ChildItem"]). Values are lower-cased
	// by the Windows adapter; Unix preserves original casing.
	Commands []string `json:"commands"`

	// Operators holds the shell operators detected (e.g. ["|", "&&", ";"]).
	Operators []string `json:"operators"`

	// Redirects holds a short descriptor for each redirection found
	// (e.g. [">", ">>"] on Unix, ["FileRedirection"] on Windows).
	Redirects []string `json:"redirects"`

	// Expansions holds the expansion types detected
	// (e.g. ["var", "subshell", "arith", "proc_subst"]).
	Expansions []string `json:"expansions"`

	// RiskFlags is the ordered set of normalized policy signals. The policy
	// engine makes decisions based primarily on this field.
	RiskFlags []ReasonCode `json:"risk_flags"`

	// ParseErrors holds human-readable diagnostics from the parser. A
	// non-empty slice indicates the input was syntactically invalid or
	// ambiguous; callers should treat this as fail-closed.
	ParseErrors []string `json:"parse_errors"`
}

// Parser is the interface implemented by every platform adapter. Each adapter
// maps its native AST to a ParsedIR.
//
// Contract:
//   - Parse MUST always return a valid ParsedIR (Version set, Platform set).
//   - If parsing fails, the returned ParsedIR MUST include a ReasonSyntaxError
//     risk flag and a non-empty ParseErrors slice, AND a non-nil error.
//   - Callers MUST treat any non-nil error as fail-closed (require confirmation).
type Parser interface {
	Parse(command string) (ParsedIR, error)
}

// Decision is the output of the PolicyEngine. It mirrors CommandAnalysis but
// lives in this package to keep ast free from circular dependencies with tool.
type Decision struct {
	IsBlocked         bool
	NeedsConfirmation bool
	BlockReason       error
	ReasonCodes       []ReasonCode
}

// MarshalIR serializes a ParsedIR to a compact JSON byte slice.
func MarshalIR(ir ParsedIR) ([]byte, error) {
	return json.Marshal(ir)
}

// UnmarshalIR deserializes a JSON byte slice into a ParsedIR and validates
// that the schema version matches IRVersion. Returns an error if the JSON is
// malformed or the version field does not match.
func UnmarshalIR(data []byte) (ParsedIR, error) {
	var ir ParsedIR
	if err := json.Unmarshal(data, &ir); err != nil {
		return ParsedIR{}, err
	}
	if ir.Version != IRVersion {
		return ParsedIR{}, fmt.Errorf("ast: unsupported IR version %q (expected %q)", ir.Version, IRVersion)
	}
	return ir, nil
}
