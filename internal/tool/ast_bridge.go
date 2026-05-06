package tool

import (
	"late/internal/tool/ast"
)

// whitelistedUnixCommands lists Unix/bash commands that are considered
// read-only/safe and auto-approve without user allowlisting.
// A nil flag entry in AllowedCommands means all flags are permitted.
var whitelistedUnixCommands = []string{
	"cat", "date", "echo", "env", "file", "find", "grep", "head",
	"ls", "printf", "pwd", "sort", "stat", "tail", "test", "true",
	"uniq", "wc", "which", "whoami",
}

// whitelistedWindowsCommands contains PowerShell cmdlets and aliases that are
// considered read-only/safe and auto-approve without user allowlisting.
var whitelistedWindowsCommands = map[string]bool{
	"cat":            true,
	"date":           true,
	"dir":            true,
	"echo":           true,
	"gc":             true,
	"gci":            true,
	"get-childitem":  true,
	"get-content":    true,
	"get-date":       true,
	"get-location":   true,
	"ls":             true,
	"measure-object": true,
	"pwd":            true,
	"select-string":  true,
	"sls":            true,
	"type":           true,
	"whoami":         true,
	"write-host":     true,
	"write-output":   true,
}

// astAnalyzer wraps the AST pipeline and implements CommandAnalyzer.
type astAnalyzer struct {
	parser ast.Parser
	policy *ast.PolicyEngine
	cwd    string
}

func newASTAnalyzer(platform ast.Platform, cwd string, allowed map[string]map[string]bool) *astAnalyzer {
	// Seed the policy engine with the built-in safe commands for the target
	// platform so they auto-approve without user allowlisting.
	// Check the platform parameter (not runtime.GOOS) so behaviour is consistent
	// when platform is overridden, e.g. in cross-platform tests.
	switch platform {
	case ast.PlatformWindows:
		for cmd := range whitelistedWindowsCommands {
			if _, ok := allowed[cmd]; !ok {
				allowed[cmd] = map[string]bool{}
			}
		}
	default: // Unix
		for _, cmd := range whitelistedUnixCommands {
			if _, ok := allowed[cmd]; !ok {
				// nil means "all flags permitted" for built-in safe commands.
				allowed[cmd] = nil
			}
		}
	}
	return &astAnalyzer{
		parser: ast.NewParser(platform, cwd),
		policy: &ast.PolicyEngine{AllowedCommands: allowed},
		cwd:    cwd,
	}
}

func (a *astAnalyzer) Analyze(command string) CommandAnalysis {
	ir, err := a.parser.Parse(command)
	if err != nil {
		// Fail closed on any parse error.
		return CommandAnalysis{NeedsConfirmation: true}
	}
	d := a.policy.Decide(ir)

	// Unsupervised mode: auto-approve mkdir/New-Item (new-path operations)
	// without any restrictions. The operation is allowed regardless of
	// target location or whether the path already exists.
	if d.NeedsConfirmation && !d.IsBlocked {
		if ast.HasRiskOnly(ir, ast.ReasonNewPath) {
			return CommandAnalysis{NeedsConfirmation: false}
		}
	}

	return CommandAnalysis{
		IsBlocked:         d.IsBlocked,
		BlockReason:       d.BlockReason,
		NeedsConfirmation: d.NeedsConfirmation,
	}
}
