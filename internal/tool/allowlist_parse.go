package tool

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// NOTE: Compound command keys (e.g., "git log", "go mod") are NOT created here
// because AST adapters (both Windows and Unix) only emit base command names
// ("git", "go") without subcommand qualification. The policy engine matches
// against these base names, so allow-list keys must align with base names only.

// wordResolver resolves shell AST word nodes to their string values.
// It only handles static literals — any dynamic expansion (variable, subshell,
// etc.) causes resolution to fail so callers can treat the result as opaque.
type wordResolver struct{}

func (r *wordResolver) resolveWord(word *syntax.Word) (string, bool) {
	if word == nil {
		return "", true
	}
	var sb strings.Builder
	for _, p := range word.Parts {
		if !r.resolvePart(&sb, p) {
			return "", false
		}
	}
	return sb.String(), true
}

func (r *wordResolver) resolvePart(sb *strings.Builder, p syntax.WordPart) bool {
	switch n := p.(type) {
	case *syntax.Lit:
		sb.WriteString(n.Value)
		return true
	case *syntax.SglQuoted:
		sb.WriteString(n.Value)
		return true
	case *syntax.DblQuoted:
		for _, qp := range n.Parts {
			if !r.resolvePart(sb, qp) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// ParseCommandsForAllowList extracts command base names (lowercased) and their
// lists of flags for ALL commands in a potentially compound string (pipes,
// chains, etc). Command names are normalized to lowercase to align with how
// AST adapters emit them (Windows PowerShell lowers all names; Unix is
// normalized to lowercase here for consistency).
func ParseCommandsForAllowList(command string) map[string][]string {
	parser := syntax.NewParser()
	f, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		return nil
	}

	commands := make(map[string][]string)
	wr := &wordResolver{}

	syntax.Walk(f, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}

		cmdName, ok := wr.resolveWord(call.Args[0])
		if !ok || cmdName == "" {
			return true
		}

		// Normalize command name to lowercase to match AST adapter behavior:
		// Windows PowerShell adapter lowercases all cmdlets; Unix should
		// also normalize to lowercase for consistency.
		key := strings.ToLower(cmdName)

		var flags []string
		for i := 1; i < len(call.Args); i++ {
			val, ok := wr.resolveWord(call.Args[i])
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
			}
		}

		if key != "" {
			commands[key] = append(commands[key], flags...)
		}

		return true
	})

	return commands
}

// isNumericFlag reports whether s is a flag consisting only of digits (e.g. -20).
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

// isNumericFd reports whether s is a valid numeric file descriptor (or "-").
func isNumericFd(s string) bool {
	if s == "-" {
		return true
	}
	if len(s) == 0 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
