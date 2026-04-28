package tool

import (
	"late/internal/tool/ast"
)

// astAnalyzer wraps the ast pipeline and implements CommandAnalyzer so it can
// be dropped into ShellTool.getAnalyzer as a drop-in replacement (Phase 5).
type astAnalyzer struct {
	parser ast.Parser
	policy *ast.PolicyEngine
}

func newASTAnalyzer(platform ast.Platform, cwd string, allowed map[string]map[string]bool) *astAnalyzer {
	return &astAnalyzer{
		parser: ast.NewParser(platform, cwd),
		policy: &ast.PolicyEngine{AllowedCommands: allowed},
	}
}

func (a *astAnalyzer) Analyze(command string) CommandAnalysis {
	ir, err := a.parser.Parse(command)
	if err != nil {
		// Fail closed on any parse error.
		return CommandAnalysis{NeedsConfirmation: true}
	}
	d := a.policy.Decide(ir)
	return CommandAnalysis{
		IsBlocked:         d.IsBlocked,
		BlockReason:       d.BlockReason,
		NeedsConfirmation: d.NeedsConfirmation,
	}
}

// shadowAnalyzerShim bridges the ast.LegacyAnalysis interface with the
// concrete CommandAnalyzer types in this package so ShadowAnalyzer can wrap
// them without importing tool (which would be circular).
type shadowAnalyzerShim struct {
	inner CommandAnalyzer
}

func (s *shadowAnalyzerShim) Analyze(command string) ast.LegacyAnalysis {
	ca := s.inner.Analyze(command)
	return ast.LegacyAnalysis{
		IsBlocked:         ca.IsBlocked,
		BlockReason:       ca.BlockReason,
		NeedsConfirmation: ca.NeedsConfirmation,
	}
}

// shadowWrapper wraps an ast.ShadowAnalyzer and implements CommandAnalyzer.
type shadowWrapper struct {
	shadow *ast.ShadowAnalyzer
}

func (sw *shadowWrapper) Analyze(command string) CommandAnalysis {
	la := sw.shadow.Analyze(command)
	return CommandAnalysis{
		IsBlocked:         la.IsBlocked,
		BlockReason:       la.BlockReason,
		NeedsConfirmation: la.NeedsConfirmation,
	}
}
