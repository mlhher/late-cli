package tool

import (
	"late/internal/tool/ast"
)

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

// whitelistedUnixCommands contains Unix/shell commands that are considered
// read-only/safe and auto-approve without user allowlisting.
var whitelistedUnixCommands = map[string]map[string]bool{
	"cat": {
		"-n": true, "-b": true, "-v": true,
	},
	"date": {
		"-u": true, "-R": true,
	},
	"echo": {
		"-n": true, "-e": true,
	},
	"file": {
		"-b": true, "-i": true,
	},
	"find": {
		"-name": true, "-iname": true, "-type": true, "-maxdepth": true, "-mindepth": true,
		"-size": true, "-mtime": true, "-atime": true, "-ctime": true, "-newer": true,
		"-user": true, "-group": true, "-path": true, "-ipath": true, "-links": true,
		"-empty": true, "-not": true, "-and": true, "-or": true,
	},
	"grep": {
		"-i": true, "-v": true, "-l": true, "-n": true, "-r": true, "-R": true,
		"-E": true, "-F": true, "-w": true, "-x": true, "-c": true,
	},
	"head": {
		"-n": true, "-c": true, "-*": true, // -* allows numeric flags like -20
	},
	"ls": {
		"-l": true, "-a": true, "-la": true, "-1": true, "-R": true, "-h": true,
		"--color": true, "-F": true,
	},
	"pwd": {
		"-P": true, "-L": true,
	},
	"tail": {
		"-n": true, "-c": true, "-f": true, "-*": true, // -* allows numeric flags like -20
	},
	"wc": {
		"-l": true, "-w": true, "-c": true, "-m": true,
	},
	"whoami": {},
}

// astAnalyzer wraps the AST pipeline and implements CommandAnalyzer.
type astAnalyzer struct {
	parser ast.Parser
	policy *ast.PolicyEngine
	cwd    string
}

func newASTAnalyzer(platform ast.Platform, cwd string, allowed map[string]map[string]bool) *astAnalyzer {
	// Seed the policy engine with the built-in safe commands so that
	// basic commands (ls, pwd, cat, etc.) auto-approve without user allowlisting.
	// Check the platform parameter (not runtime.GOOS) so behaviour is consistent
	// when platform is overridden, e.g. in cross-platform tests.
	if platform == ast.PlatformWindows {
		for cmd := range whitelistedWindowsCommands {
			if _, ok := allowed[cmd]; !ok {
				allowed[cmd] = map[string]bool{}
			}
		}
	} else {
		// Unix: seed with commands and their common flags
		for cmd, flags := range whitelistedUnixCommands {
			if _, ok := allowed[cmd]; !ok {
				allowed[cmd] = make(map[string]bool)
			}
			// Add all the whitelisted flags for this command
			for flag := range flags {
				allowed[cmd][flag] = true
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
