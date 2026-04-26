package tool

import (
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// tier1AllowList defines simple commands and their permitted flags.
// Positional arguments (not starting with '-') are allowed if the command is in this list.
var tier1AllowList = map[string]map[string]bool{
	"ls":     {"-l": true, "-a": true, "-la": true, "-1": true, "-R": true, "-h": true, "--color": true, "-F": true},
	"cat":    {"-n": true, "-b": true, "-v": true},
	"head":   {"-n": true, "-c": true},
	"tail":   {"-n": true, "-c": true, "-f": true},
	"pwd":    {"-P": true, "-L": true},
	"date":   {"-u": true, "-R": true},
	"whoami": {},
	"wc":     {"-l": true, "-w": true, "-c": true, "-m": true},
	"seq":    {},
	"file":   {"-b": true, "-i": true},
	"echo":   {"-n": true, "-e": true},
	"du":     {"-h": true, "-s": true, "-a": true, "-c": true},
	"df":     {"-h": true, "-T": true},
	"stat":   {"-c": true, "-f": true},
	"lsof":   {"-i": true, "-p": true, "-u": true, "-n": true, "-P": true},
	"grep":   {"-i": true, "-v": true, "-l": true, "-n": true, "-r": true, "-R": true, "-E": true, "-F": true, "-w": true, "-x": true, "-c": true},
}

// tier2AllowList defines complex commands with subcommands and their permitted flags.
var tier2AllowList = map[string]map[string]map[string]bool{
	"git": {
		"status":    {"-s": true, "--short": true, "--long": true, "-b": true, "--branch": true, "--porcelain": true},
		"log":       {"--oneline": true, "--stat": true, "-n": true, "--author": true, "--graph": true, "--patch": true, "-p": true, "--reverse": true, "--all": true},
		"diff":      {"--stat": true, "--cached": true, "--staged": true, "-p": true, "--patch": true, "--color": true, "--name-only": true, "--name-status": true},
		"show":      {"--stat": true, "--oneline": true, "-p": true, "--patch": true, "--name-only": true},
		"tag":       {"-l": true, "--list": true},
		"rev-parse": {"--show-toplevel": true, "--abbrev-ref": true, "--short": true},
		"remote":    {"-v": true},
	},
	"go": {
		"doc": {"-all": true, "-src": true, "-u": true},
		"mod": {"tidy": true, "graph": true, "verify": true, "why": true, "download": true},
	},
}

// findAllowedFlags defines flags permitted for the 'find' command.
var findAllowedFlags = map[string]bool{
	"-name":      true,
	"-iname":     true,
	"-type":      true,
	"-maxdepth":  true,
	"-mindepth":  true,
	"-size":      true,
	"-mtime":     true,
	"-atime":     true,
	"-ctime":     true,
	"-newer":     true,
	"-user":      true,
	"-group":     true,
	"-path":      true,
	"-ipath":     true,
	"-links":     true,
	"-empty":     true,
	"-not":       true,
	"-and":       true,
	"-or":        true,
}

// allowedEnvVars contains environment variables that are safe to set.
var allowedEnvVars = map[string]bool{
	"DEBUG":       true,
	"LANG":        true,
	"LC_ALL":      true,
	"TERM":        true,
	"COLOR":       true,
	"GOOS":        true,
	"GOARCH":      true,
	"CGO_ENABLED": true,
}

type BashAnalyzer struct {
	// ProjectAllowedCommands is a list of normalized command strings (e.g., "git log", "go test")
	// that the user has explicitly allowed for this project, mapped to the flags allowed for each.
	ProjectAllowedCommands map[string]map[string]bool
}

func (b *BashAnalyzer) Analyze(command string) CommandAnalysis {
	parser := syntax.NewParser()
	f, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		return CommandAnalysis{NeedsConfirmation: true}
	}

	analysis := CommandAnalysis{}

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
			if analysis.IsBlocked {
				return false
			}

		case *syntax.Redirect:
			switch n.Op {
			case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.RdrClob, syntax.AppClob, syntax.DplOut:
				analysis.IsBlocked = true
				analysis.NeedsConfirmation = true
				analysis.BlockReason = fmt.Errorf("Output redirection (>) is blocked. Use `write_file` or `target_edit` to modify files.")
				return false
			}

		case *syntax.BinaryCmd:
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
			analysis.NeedsConfirmation = true

		case *syntax.CmdSubst, *syntax.Subshell, *syntax.ProcSubst:
			analysis.NeedsConfirmation = true

		case *syntax.IfClause, *syntax.WhileClause, *syntax.ForClause, *syntax.CaseClause:
			analysis.NeedsConfirmation = true

		case *syntax.ParamExp:
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

	cmdName, ok := b.resolveWord(n.Args[0])
	if !ok || cmdName == "" || strings.Contains(cmdName, "/") {
		return false
	}

	// SECURITY: Block 'cd' explicitly.
	if cmdName == "cd" {
		analysis.IsBlocked = true
		analysis.BlockReason = fmt.Errorf("Do not use `cd` to change directories. Use the `cwd` parameter in the shell tool instead.")
		return false
	}

	// Step 1: Environment check (always enforced, even for project-allowed commands)
	for _, assign := range n.Assigns {
		if assign.Name == nil || !allowedEnvVars[assign.Name.Value] {
			return false
		}
		if assign.Value == nil {
			return false
		}
		if _, ok := b.resolveWord(assign.Value); !ok {
			return false
		}
	}

	// Step 2: Check project-specific allow-list (with flag-level granularity)
	if allowedFlags, ok := b.isProjectAllowed(n); ok {
		// If project-allowed, we check each argument.
		for _, arg := range n.Args[1:] {
			val, ok := b.resolveWord(arg)
			if !ok {
				return false
			}
			if strings.HasPrefix(val, "-") {
				// Strip key-value pairs (e.g., --output=foo -> --output)
				flagKey := val
				if idx := strings.Index(val, "="); idx != -1 {
					flagKey = val[:idx]
				}

				// Exact match for flags
				if allowedFlags[flagKey] {
					continue
				}
				// Support numeric wildcard if saved as '-*'
				if allowedFlags["-*"] && isNumericFlag(val) {
					continue
				}
				// Flag not in the approved set for this command
				return false
			}
			
			// It's a positional argument. 
			// Special case: if it's in the allowedFlags map (e.g., 'tidy' in 'go mod tidy'), allow it.
			if allowedFlags[val] {
				continue
			}

			// Otherwise, it's a generic positional argument. Since it doesn't start with '-',
			// and resolveWord succeeded (static literal), it's safe.
		}
		return true
	}

	// Step 3: Tier Categorization and Validation (Hardcoded Schema)
	if allowedFlags, ok := tier1AllowList[cmdName]; ok {
		return b.validateTier1(cmdName, n.Args[1:], allowedFlags)
	}

	if subcommands, ok := tier2AllowList[cmdName]; ok {
		return b.validateTier2(cmdName, n.Args[1:], subcommands)
	}

	if cmdName == "find" {
		return b.validateFind(n.Args[1:])
	}

	// Default Deny
	return false
}

// isProjectAllowed returns the set of allowed flags and true if the command is whitelisted.
func (b *BashAnalyzer) isProjectAllowed(n *syntax.CallExpr) (map[string]bool, bool) {
	if len(b.ProjectAllowedCommands) == 0 {
		return nil, false
	}

	cmdName, ok := b.resolveWord(n.Args[0])
	if !ok {
		return nil, false
	}

	// Check Command + Subcommand (e.g., "git log")
	// Only do this for known Tier 2 commands to avoid false positives (e.g., "grep pattern")
	if _, isTier2 := tier2AllowList[cmdName]; isTier2 && len(n.Args) >= 2 {
		subCmd, ok := b.resolveWord(n.Args[1])
		if ok && subCmd != "" && !strings.HasPrefix(subCmd, "-") {
			fullCmd := cmdName + " " + subCmd
			if flags, ok := b.ProjectAllowedCommands[fullCmd]; ok {
				return flags, true
			}
		}
	}

	// Check base command
	if flags, ok := b.ProjectAllowedCommands[cmdName]; ok {
		return flags, true
	}

	return nil, false
}

func (b *BashAnalyzer) validateTier1(cmd string, args []*syntax.Word, allowedFlags map[string]bool) bool {
	for _, arg := range args {
		val, ok := b.resolveWord(arg)
		if !ok {
			return false
		}
		if strings.HasPrefix(val, "-") {
			// Allow numeric flags for head and tail (e.g., -20)
			if (cmd == "head" || cmd == "tail") && isNumericFlag(val) {
				continue
			}
			if !allowedFlags[val] {
				return false
			}
		} else {
			// Positional argument
			if !b.isSafePositionalArg(arg) {
				return false
			}
		}
	}
	return true
}

func isNumericFlag(s string) bool {
	if len(s) < 2 || s[0] != '-' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func (b *BashAnalyzer) validateTier2(cmd string, args []*syntax.Word, subcommands map[string]map[string]bool) bool {
	if len(args) == 0 {
		return true // Just the base command is help
	}

	subCmd, ok := b.resolveWord(args[0])
	if !ok || subCmd == "" || strings.HasPrefix(subCmd, "-") {
		return false // Subcommand expected
	}

	allowedFlags, ok := subcommands[subCmd]
	if !ok {
		return false // Subcommand not whitelisted
	}

	// Validate remaining arguments
	for _, arg := range args[1:] {
		val, ok := b.resolveWord(arg)
		if !ok {
			return false
		}
		if strings.HasPrefix(val, "-") {
			if !allowedFlags[val] {
				return false
			}
		} else {
			// Positional argument
			if !b.isSafePositionalArg(arg) {
				return false
			}
		}
	}
	return true
}

func (b *BashAnalyzer) validateFind(args []*syntax.Word) bool {
	for _, arg := range args {
		val, ok := b.resolveWord(arg)
		if !ok {
			return false
		}
		if strings.HasPrefix(val, "-") {
			// Find flags often start with - but are not exactly like standard flags.
			// Still, we check them against an allow-list.
			if !findAllowedFlags[val] {
				return false
			}
		} else {
			// Positional argument (path, etc)
			if !b.isSafePositionalArg(arg) {
				return false
			}
		}
	}
	return true
}

func (b *BashAnalyzer) isSafePositionalArg(word *syntax.Word) bool {
	if word == nil {
		return true
	}
	// Ensure it doesn't look like a flag (injection prevention)
	val, ok := b.resolveWord(word)
	if !ok || strings.HasPrefix(val, "-") {
		return false
	}

	return true
}

// resolveWord concatenates all parts of a word into a single string.
// It returns false if the word contains non-literal parts (expansions, subshells, etc).
func (b *BashAnalyzer) resolveWord(word *syntax.Word) (string, bool) {
	if word == nil {
		return "", true
	}
	var sb strings.Builder
	for _, p := range word.Parts {
		if !b.resolvePart(&sb, p) {
			return "", false
		}
	}
	return sb.String(), true
}

func (b *BashAnalyzer) resolvePart(sb *strings.Builder, p syntax.WordPart) bool {
	switch n := p.(type) {
	case *syntax.Lit:
		sb.WriteString(n.Value)
		return true
	case *syntax.SglQuoted:
		sb.WriteString(n.Value)
		return true
	case *syntax.DblQuoted:
		for _, qp := range n.Parts {
			if !b.resolvePart(sb, qp) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// ParseCommandsForAllowList extracts stable keys (e.g., "git log") and their lists of flags
// for ALL commands in a potentially compound string (pipes, chains, etc).
func ParseCommandsForAllowList(command string) map[string][]string {
	parser := syntax.NewParser()
	f, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		return nil
	}

	commands := make(map[string][]string)
	analyzer := &BashAnalyzer{}

	syntax.Walk(f, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true // Keep walking
		}

		cmdName, ok := analyzer.resolveWord(call.Args[0])
		if !ok || cmdName == "" {
			return true
		}

		var key string
		var subCmd string
		var startIdx int

		// Check for subcommand (only for Tier 2 commands)
		if _, isTier2 := tier2AllowList[cmdName]; isTier2 && len(call.Args) >= 2 {
			sc, ok := analyzer.resolveWord(call.Args[1])
			if ok && sc != "" && !strings.HasPrefix(sc, "-") {
				key = cmdName + " " + sc
				subCmd = sc
				startIdx = 2
			} else {
				key = cmdName
				startIdx = 1
			}
		} else {
			key = cmdName
			startIdx = 1
		}

		var flags []string
		for i := startIdx; i < len(call.Args); i++ {
			val, ok := analyzer.resolveWord(call.Args[i])
			if !ok {
				continue
			}

			if strings.HasPrefix(val, "-") {
				// Strip key-value pairs (e.g., --output=foo -> --output)
				flagKey := val
				if idx := strings.Index(val, "="); idx != -1 {
					flagKey = val[:idx]
				}

				// Normalize numeric flags
				if isNumericFlag(val) {
					flags = append(flags, "-*")
				} else {
					flags = append(flags, flagKey)
				}
			} else {
				// Positional argument. 
				// Check if it's a whitelisted "sub-sub-command" (like 'tidy' in 'go mod tidy')
				if subCmd != "" {
					if _, ok := tier2AllowList[cmdName][subCmd][val]; ok {
						flags = append(flags, val)
					}
				}
			}
		}

		if key != "" {
			commands[key] = append(commands[key], flags...)
		}

		return true // Keep walking to find more commands
	})

	return commands
}
