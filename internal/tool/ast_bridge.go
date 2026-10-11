package tool

import (
	"strings"

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
	// exactAllowed holds the full allow-list store keys (exactCommandKeyPrefix
	// + cwd + NUL + command string) that carry an exact-string approval in the
	// persistent allow-list store (issue #1). Such approvals are written by
	// SaveExactAllowedCommand after a gated command executed with a valid OTP,
	// and they bypass the confirmation gate for that exact command IN THE SAME
	// EXECUTION CWD in every late instance — relative paths in the command
	// resolve against the cwd, so the same argv from a different directory is
	// a different command — while never bypassing hard blocks (cd, unsafe
	// redirects, parse-error hard refusals). Nil/empty disables the bypass.
	// Lookups go through ExactCommandKey so the cwd is canonicalized the same
	// way at save and lookup time.
	exactAllowed map[string]bool
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

	// Exact-string approvals (issue #1) ride in the same merged allow-list
	// under the reserved exactCommandKeyPrefix key namespace (cwd + command
	// pairs); extract them into a dedicated lookup so Analyze can grant the
	// bypass for an exact (cwd, command string) pair without touching
	// per-command flag semantics.
	exactAllowed := make(map[string]bool)
	for key := range allowed {
		if strings.HasPrefix(key, exactCommandKeyPrefix) {
			exactAllowed[key] = true
		}
	}

	return &astAnalyzer{
		parser:       ast.NewParser(platform, cwd),
		policy:       &ast.PolicyEngine{AllowedCommands: allowed},
		cwd:          cwd,
		exactAllowed: exactAllowed,
	}
}

func (a *astAnalyzer) Analyze(command string) CommandAnalysis {
	ir, err := a.parser.Parse(command)
	if err != nil {
		// Fail closed on any parse error — and keep hard blocks hard: scan
		// the raw command for the policy's hard-block signatures (cd,
		// unsafe output redirects) so unparseable input cannot slip past
		// them (e.g. into the force-revaluate OTP flow).
		if blockErr := parseErrorHardBlock(command); blockErr != nil {
			return CommandAnalysis{IsBlocked: true, NeedsConfirmation: true, BlockReason: blockErr}
		}
		// Issue #1: an exact-string approval also covers commands that do
		// not parse — the same (cwd, command) pair already executed once
		// with a valid OTP, and hard-block signatures were checked above.
		// Everything else about the string is known, so the gate must not
		// re-prompt.
		if a.exactAllowed[ExactCommandKey(a.cwd, command)] {
			return CommandAnalysis{NeedsConfirmation: false}
		}
		// Fail closed on any parse error.
		return CommandAnalysis{NeedsConfirmation: true}
	}
	d := a.policy.Decide(ir)

	// Issue #1: an exact-string approval clears the soft confirmation gate
	// for this exact command string executed in the analyzer's cwd (never a
	// hard block). This is what makes "approved once with an OTP → never
	// gated again" hold in every late instance: the approval lives in the
	// persistent allow-list store that every process reloads here, and both
	// the approval and this lookup resolve the cwd through ExactCommandKey,
	// so a re-run from a different directory stays gated.
	if d.NeedsConfirmation && !d.IsBlocked && a.exactAllowed[ExactCommandKey(a.cwd, command)] {
		d.NeedsConfirmation = false
	}

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
