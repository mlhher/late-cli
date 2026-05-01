package tool

import (
	"strings"
	"late/internal/tool/ast"
)

// extractTargetPath extracts the target path from a command for unsupervised
// mode validation. Returns empty string if unable to extract.
func extractTargetPath(command string, platform ast.Platform) string {
	if platform == ast.PlatformWindows {
		return extractPowerShellTargetPath(command)
	}
	// Unix: extract mkdir target from simple commands like "mkdir foo"
	tokens := strings.Fields(strings.TrimSpace(command))
	if len(tokens) < 2 {
		return ""
	}
	cmd := strings.ToLower(tokens[0])
	if cmd == "mkdir" {
		return tokens[1]
	}
	return ""
}

// astAnalyzer wraps the ast pipeline and implements CommandAnalyzer so it can
// be dropped into ShellTool.getAnalyzer as a drop-in replacement (Phase 5).
type astAnalyzer struct {
	parser ast.Parser
	policy *ast.PolicyEngine
	cwd    string
}

func newASTAnalyzer(platform ast.Platform, cwd string, allowed map[string]map[string]bool) *astAnalyzer {
	// On Windows, seed the policy engine with the built-in safe cmdlets so
	// that Get-ChildItem, ls, pwd etc. auto-approve without user allowlisting.
	// Source of truth is whitelistedWindowsCommands in powershell_analyzer.go.
	// Check the platform parameter (not runtime.GOOS) so behaviour is consistent
	// when platform is overridden, e.g. in cross-platform tests.
	if platform == ast.PlatformWindows {
		for cmd := range whitelistedWindowsCommands {
			if _, ok := allowed[cmd]; !ok {
				allowed[cmd] = map[string]bool{}
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

	// Unsupervised mode: PolicyEngine conservatively requires confirmation for
	// mkdir/New-Item (it has no cwd context). Here we auto-approve new-path
	// operations on all platforms, but only if the target doesn't already exist.
	if d.NeedsConfirmation && !d.IsBlocked {
		if ast.HasRiskOnly(ir, ast.ReasonNewPath) {
			target := extractTargetPath(command, ir.Platform)
			if target != "" && isNewPath(target, a.cwd) {
				return CommandAnalysis{NeedsConfirmation: false}
			}
		}
	}

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
