package tool

import (
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// bashWhitelistedCommands contains commands that do not require user confirmation.
// Only genuinely read-only commands belong here.
var bashWhitelistedCommands = map[string]bool{
	"grep":   true,
	"ls":     true,
	"cat":    true,
	"head":   true,
	"tail":   true,
	"pwd":    true,
	"date":   true,
	"whoami": true,
	"wc":     true,
	"seq":    true,
	"file":   true,
	"echo":   true,
	"du":     true,
	"df":     true,
	"stat":   true,
	"lsof":   true,
	"find":   true,
	"git":    true,
	"go":     true,
}

var safeGitSubcommands = map[string]bool{
	"status":    true,
	"diff":      true,
	"log":       true,
	"show":      true,
	"tag":       true,
	"rev-parse": true,
}

var safeGoSubcommands = map[string]bool{
	"doc": true,
	"mod": true,
}

// allowedEnvVars contains environment variables that are safe to set.
// Hijacking variables like PAGER, GIT_ASKPASS, or LD_PRELOAD is blocked.
var allowedEnvVars = map[string]bool{
	"DEBUG":    true,
	"LANG":     true,
	"LC_ALL":   true,
	"TERM":     true,
	"COLOR":    true,
	"GOOS":     true,
	"GOARCH":   true,
	"CGO_ENABLED": true,
}

type BashAnalyzer struct{}

func (b *BashAnalyzer) Analyze(command string) CommandAnalysis {
	parser := syntax.NewParser()
	f, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		// If we can't parse it, it might be a weird one-liner that's valid shell but not POSIX/Bash.
		// Conservative approach: require confirmation, but don't block unless we're sure.
		return CommandAnalysis{NeedsConfirmation: true}
	}

	analysis := CommandAnalysis{}

	// We use a manual walk to handle BinaryCmd intelligently.
	var checkNode func(node syntax.Node) bool
	checkNode = func(node syntax.Node) bool {
		if node == nil {
			return true
		}
		switch n := node.(type) {
		case *syntax.CallExpr:
			if !b.isSafeCall(n, &analysis) {
				analysis.NeedsConfirmation = true
			}
			// If blocked during isSafeCall, stop walking.
			if analysis.IsBlocked {
				return false
			}

		case *syntax.Redirect:
			// Op is RedirOperator. Check if it's an output redirect.
			switch n.Op {
			case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.RdrClob, syntax.AppClob, syntax.DplOut:
				analysis.IsBlocked = true
				analysis.NeedsConfirmation = true
				analysis.BlockReason = fmt.Errorf("Output redirection (>) is blocked. Use `write_file` or `target_edit` to modify files.")
				return false
			}

		case *syntax.BinaryCmd:
			// Pipes (|), logical operators (&&, ||)
			// We check both sides. If both sides are safe, the binary command is safe.
			if !checkNode(n.X) || !checkNode(n.Y) {
				return false
			}

		case *syntax.Stmt:
			for _, redir := range n.Redirs {
				if !checkNode(redir) {
					return false
				}
			}
			if !checkNode(n.Cmd) {
				return false
			}

		case *syntax.File:
			for _, stmt := range n.Stmts {
				if !checkNode(stmt) {
					return false
				}
			}

		case *syntax.Block:
			for _, stmt := range n.Stmts {
				if !checkNode(stmt) {
					return false
				}
			}
			analysis.NeedsConfirmation = true // Blocks themselves are slightly complex

		case *syntax.CmdSubst, *syntax.Subshell, *syntax.ProcSubst:
			// $(cmd), `cmd`, (cmd), <(cmd)
			analysis.NeedsConfirmation = true

		case *syntax.IfClause, *syntax.WhileClause, *syntax.ForClause, *syntax.CaseClause:
			// Control structures
			analysis.NeedsConfirmation = true

		case *syntax.ParamExp:
			// ${var}
			analysis.NeedsConfirmation = true
		}
		return true
	}

	checkNode(f)

	return analysis
}

func (b *BashAnalyzer) isSafeCall(n *syntax.CallExpr, analysis *CommandAnalysis) bool {
	if len(n.Args) == 0 {
		return true
	}

	cmdName := b.extractCommandName(n.Args[0])
	if cmdName == "" {
		return false
	}

	// SECURITY: Forbid commands with paths. They must be simple binary names to be whitelisted.
	// This prevents the "/tmp/ls" bypass trick.
	if strings.Contains(cmdName, "/") {
		return false
	}

	// Handle blocked commands
	if cmdName == "cd" {
		analysis.IsBlocked = true
		analysis.BlockReason = fmt.Errorf("Do not use `cd` to change directories. Use the `cwd` parameter in the shell tool instead.")
		return false
	}

	// Check if whitelisted
	if !bashWhitelistedCommands[cmdName] {
		return false
	}

	// Special handling for find
	if cmdName == "find" {
		if !b.isSafeFind(n) {
			return false
		}
	}

	// Special handling for git
	if cmdName == "git" {
		if !b.isSafeGit(n) {
			return false
		}
	}

	// Special handling for go
	if cmdName == "go" {
		if !b.isSafeGo(n) {
			return false
		}
	}

	// Check environment assignments
	for _, assign := range n.Assigns {
		if assign.Name == nil || !allowedEnvVars[assign.Name.Value] {
			return false
		}
		if assign.Value == nil {
			return false
		}
		for _, p := range assign.Value.Parts {
			if !isSafeWordPart(p) {
				return false
			}
		}
	}

	// Check if any argument is not a simple literal/safe quoted part
	for _, arg := range n.Args {
		if arg != nil {
			for _, p := range arg.Parts {
				if !isSafeWordPart(p) {
					return false
				}
			}
		}
	}

	return true
}

func (b *BashAnalyzer) extractCommandName(word *syntax.Word) string {
	if word == nil || len(word.Parts) == 0 {
		return ""
	}

	var rawName string
	if len(word.Parts) == 1 {
		switch p := word.Parts[0].(type) {
		case *syntax.Lit:
			rawName = p.Value
		case *syntax.SglQuoted:
			rawName = p.Value
		case *syntax.DblQuoted:
			if len(p.Parts) == 1 {
				if lit, ok := p.Parts[0].(*syntax.Lit); ok {
					rawName = lit.Value
				}
			}
		}
	}

	return rawName
}

func (b *BashAnalyzer) isSafeFind(n *syntax.CallExpr) bool {
	// Check for destructive or execution flags
	for _, arg := range n.Args {
		if len(arg.Parts) == 1 {
			if lit, ok := arg.Parts[0].(*syntax.Lit); ok {
				val := lit.Value
				if val == "-exec" || val == "-execdir" || val == "-ok" || val == "-okdir" || val == "-delete" {
					return false
				}
			}
		}
	}
	return true
}

func (b *BashAnalyzer) isSafeGit(n *syntax.CallExpr) bool {
	if len(n.Args) < 2 {
		return true // Just "git" is safe (shows help)
	}
	subCmd := b.extractCommandName(n.Args[1])
	if subCmd == "" || strings.Contains(subCmd, "/") {
		return false
	}
	return safeGitSubcommands[subCmd]
}

func (b *BashAnalyzer) isSafeGo(n *syntax.CallExpr) bool {
	if len(n.Args) < 2 {
		return true // Just "go" is safe (shows help)
	}
	subCmd := b.extractCommandName(n.Args[1])
	if subCmd == "" || strings.Contains(subCmd, "/") {
		return false
	}
	return safeGoSubcommands[subCmd]
}

// isSafeWordPart returns true if the WordPart is a literal or a quoted string
// that contains only literals (no expansions, no subshells).
func isSafeWordPart(p syntax.WordPart) bool {
	switch n := p.(type) {
	case *syntax.Lit:
		return true
	case *syntax.SglQuoted:
		return true
	case *syntax.DblQuoted:
		for _, qp := range n.Parts {
			if !isSafeWordPart(qp) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
