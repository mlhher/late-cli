package ast

// addRiskFlag adds rc to ir.RiskFlags if not already present (idempotent).
func addRiskFlag(ir *ParsedIR, seen map[ReasonCode]bool, rc ReasonCode) {
	if !seen[rc] {
		seen[rc] = true
		ir.RiskFlags = append(ir.RiskFlags, rc)
	}
}

// addStringUnique adds s to *slice if not already tracked in seen.
func addStringUnique(slice *[]string, seen map[string]bool, s string) {
	if !seen[s] {
		seen[s] = true
		*slice = append(*slice, s)
	}
}

// appendUniqueRC returns a new slice with rc appended only if absent.
func appendUniqueRC(sl []ReasonCode, rc ReasonCode) []ReasonCode {
	for _, v := range sl {
		if v == rc {
			return sl
		}
	}
	return append(sl, rc)
}

// hasRisk reports whether rc appears in ir.RiskFlags.
func hasRisk(ir ParsedIR, rc ReasonCode) bool {
	for _, r := range ir.RiskFlags {
		if r == rc {
			return true
		}
	}
	return false
}

// emptyIR returns a correctly initialised ParsedIR with all slices non-nil.
func emptyIR(platform Platform) ParsedIR {
	return ParsedIR{
		Version:     IRVersion,
		Platform:    platform,
		Commands:    []string{},
		Operators:   []string{},
		Redirects:   []string{},
		Expansions:  []string{},
		RiskFlags:   []ReasonCode{},
		ParseErrors: []string{},
	}
}
