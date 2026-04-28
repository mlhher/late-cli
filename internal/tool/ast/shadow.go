package ast

import (
	"log"
	"reflect"
)

// ShadowAnalyzer wraps a legacy CommandAnalyzer and runs the AST pipeline in
// parallel (shadow mode). It always returns the legacy decision so there is
// zero behavior change in Phase 4. Decision deltas are logged for analysis.
//
// Wire it in ShellTool.getAnalyzer() when FeatureASTShadow() is true.
type ShadowAnalyzer struct {
	legacy          legacyAnalyzer
	astParser       Parser
	policy          *PolicyEngine
	allowedCommands map[string]map[string]bool
}

// legacyAnalyzer mirrors tool.CommandAnalyzer without importing the tool
// package (which would create a circular dependency).
type legacyAnalyzer interface {
	Analyze(command string) LegacyAnalysis
}

// LegacyAnalysis is the subset of tool.CommandAnalysis that ShadowAnalyzer
// needs. It is populated by the adapter shim in implementations.go.
type LegacyAnalysis struct {
	IsBlocked         bool
	BlockReason       error
	NeedsConfirmation bool
}

// NewShadowAnalyzer creates a ShadowAnalyzer. platform selects the parser
// adapter; cwd is passed to the WindowsParser for path-resolution context;
// allowedCommands is the merged allow-list from the permissions subsystem.
func NewShadowAnalyzer(
	legacy legacyAnalyzer,
	platform Platform,
	cwd string,
	allowedCommands map[string]map[string]bool,
) *ShadowAnalyzer {
	return &ShadowAnalyzer{
		legacy:          legacy,
		astParser:       NewParser(platform, cwd),
		policy:          &PolicyEngine{AllowedCommands: allowedCommands},
		allowedCommands: allowedCommands,
	}
}

// Analyze runs both the legacy analyzer and the AST pipeline, logs any
// decision delta, and returns the legacy result (shadow mode — no enforcement).
func (s *ShadowAnalyzer) Analyze(command string) LegacyAnalysis {
	legacyResult := s.legacy.Analyze(command)

	ir, err := s.astParser.Parse(command)
	if err != nil {
		log.Printf("[ast/shadow] parse error for %q: %v", truncate(command, 80), err)
		return legacyResult
	}

	astDecision := s.policy.Decide(ir)

	legacyNorm := LegacyAnalysis{
		IsBlocked:         legacyResult.IsBlocked,
		NeedsConfirmation: legacyResult.NeedsConfirmation,
	}
	astNorm := LegacyAnalysis{
		IsBlocked:         astDecision.IsBlocked,
		NeedsConfirmation: astDecision.NeedsConfirmation,
	}

	if !reflect.DeepEqual(legacyNorm, astNorm) {
		log.Printf(
			"[ast/shadow] DELTA command=%q legacy={blocked:%v confirm:%v} ast={blocked:%v confirm:%v} risk_flags=%v",
			truncate(command, 80),
			legacyResult.IsBlocked, legacyResult.NeedsConfirmation,
			astDecision.IsBlocked, astDecision.NeedsConfirmation,
			astDecision.ReasonCodes,
		)
	}

	return legacyResult
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
