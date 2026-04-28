package ast

import "os"

// Feature-flag environment variables for the AST rollout.
// These are read at call-time so they can be toggled without restarting the
// process (useful for integration tests and gradual rollout).
const (
	// EnvASTShadow enables shadow mode: the AST pipeline runs alongside the
	// legacy analyzer and logs decision deltas but does NOT change behavior.
	EnvASTShadow = "LATE_AST_SHADOW"

	// EnvASTEnforcement promotes the AST pipeline to the authoritative path.
	// When set, the legacy analyzer is bypassed entirely. This implies shadow
	// mode as well (no need to set both).
	EnvASTEnforcement = "LATE_AST_ENFORCEMENT"
)

// FeatureASTShadow reports whether AST shadow mode is enabled.
func FeatureASTShadow() bool {
	v := os.Getenv(EnvASTShadow)
	return v == "1" || v == "true" || v == "on"
}

// FeatureASTEnforcement reports whether AST enforcement (Phase 5) is active.
// When true, the AST policy path is authoritative and the legacy analyzer is
// not consulted.
func FeatureASTEnforcement() bool {
	v := os.Getenv(EnvASTEnforcement)
	return v == "1" || v == "true" || v == "on"
}
