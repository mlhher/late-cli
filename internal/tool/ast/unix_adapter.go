package ast

import (
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// UnixParser implements Parser for Unix/bash/sh shells via mvdan.cc/sh/v3.
// It walks the native AST and maps nodes to ParsedIR risk signals.
type UnixParser struct{}

// Parse parses command as a Unix/bash shell statement and returns a ParsedIR.
// It never executes the command; parsing is purely syntactic.
func (u *UnixParser) Parse(command string) (ParsedIR, error) {
	ir := emptyIR(PlatformUnix)

	parser := syntax.NewParser()
	f, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		ir.ParseErrors = append(ir.ParseErrors, err.Error())
		ir.RiskFlags = appendUniqueRC(ir.RiskFlags, ReasonSyntaxError)
		return ir, fmt.Errorf("ast/unix: %w", err)
	}

	seenCmds := map[string]bool{}
	seenOps := map[string]bool{}
	seenRedirects := map[string]bool{}
	seenExpansions := map[string]bool{}
	seenRisk := map[ReasonCode]bool{}

	// Multiple top-level statements imply ';' as a separator.
	if len(f.Stmts) > 1 {
		addStringUnique(&ir.Operators, seenOps, ";")
		addRiskFlag(&ir, seenRisk, ReasonOperator)
	}

	syntax.Walk(f, func(node syntax.Node) bool {
		if node == nil {
			return false
		}
		switch n := node.(type) {
		case *syntax.CallExpr:
			if len(n.Args) > 0 {
				name := unixResolveWord(n.Args[0])
				if name != "" && !strings.Contains(name, "/") {
					addStringUnique(&ir.Commands, seenCmds, name)
					if name == "cd" {
						addRiskFlag(&ir, seenRisk, ReasonCd)
					}
				}
			}

		case *syntax.BinaryCmd:
			op := n.Op.String()
			addStringUnique(&ir.Operators, seenOps, op)
			addRiskFlag(&ir, seenRisk, ReasonOperator)

		case *syntax.Redirect:
			switch n.Op {
			case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll,
				syntax.RdrClob, syntax.AppClob:
				word := unixResolveWord(n.Word)
				if !unixIsSafeRedirectTarget(word) {
					addStringUnique(&ir.Redirects, seenRedirects, ">")
					addRiskFlag(&ir, seenRisk, ReasonRedirect)
				}
			case syntax.DplOut:
				word := unixResolveWord(n.Word)
				if !unixIsNumericFd(word) && !unixIsSafeRedirectTarget(word) {
					addStringUnique(&ir.Redirects, seenRedirects, ">&")
					addRiskFlag(&ir, seenRisk, ReasonRedirect)
				}
			}

		case *syntax.ParamExp:
			addStringUnique(&ir.Expansions, seenExpansions, "var")
			addRiskFlag(&ir, seenRisk, ReasonExpansion)

		case *syntax.ArithmExp, *syntax.ArithmCmd:
			addStringUnique(&ir.Expansions, seenExpansions, "arith")
			addRiskFlag(&ir, seenRisk, ReasonExpansion)

		case *syntax.CmdSubst:
			addStringUnique(&ir.Expansions, seenExpansions, "subshell")
			addRiskFlag(&ir, seenRisk, ReasonSubshell)

		case *syntax.ProcSubst:
			addStringUnique(&ir.Expansions, seenExpansions, "proc_subst")
			addRiskFlag(&ir, seenRisk, ReasonSubshell)

		case *syntax.Subshell:
			addStringUnique(&ir.Expansions, seenExpansions, "subshell")
			addRiskFlag(&ir, seenRisk, ReasonSubshell)

		case *syntax.Block, *syntax.IfClause, *syntax.WhileClause,
			*syntax.ForClause, *syntax.CaseClause:
			addRiskFlag(&ir, seenRisk, ReasonOperator)
		}
		return true
	})

	return ir, nil
}

// unixResolveWord attempts to statically resolve a syntax.Word to a plain
// string. Returns "" if the word contains dynamic parts (variables, subshells).
func unixResolveWord(w *syntax.Word) string {
	if w == nil || len(w.Parts) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			sb.WriteString(p.Value)
		case *syntax.SglQuoted:
			sb.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, qp := range p.Parts {
				if lit, ok := qp.(*syntax.Lit); ok {
					sb.WriteString(lit.Value)
				} else {
					return "" // dynamic content inside double-quotes
				}
			}
		default:
			return "" // dynamic word part — cannot resolve statically
		}
	}
	return sb.String()
}

func unixIsSafeRedirectTarget(word string) bool {
	return word == "/dev/null" || word == "/dev/stdout" || word == "/dev/stderr"
}

func unixIsNumericFd(s string) bool {
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
