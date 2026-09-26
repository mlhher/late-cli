package tool

import (
	"fmt"
	"os"
	"sync"
)

// diagnostics is the package-level sink for mid-session diagnostic lines.
// The only current reporter is the bash gate's "warn" level (see
// ValidateBashCommand), which runs inside tool-execution paths during a live
// agent turn: writing that warning straight to os.Stderr paints raw text over
// the TUI's alt-screen. main installs a sink (SetDiagnostics) that forwards
// the line to the TUI as a DiagnosticMsg warning toast; without a sink — CLI
// flows, tests — lines fall back to os.Stderr unchanged.
//
// Package-level because tool implementations are constructed without a
// back-reference to the TUI wiring; the sink is process-global by nature.
// Multiple SetDiagnostics calls: the LAST sink wins (it replaces the
// previous one).
var (
	diagnosticsMu    sync.RWMutex
	diagnosticsFuncs func(msg string)
)

// SetDiagnostics installs fn as the sink for mid-session diagnostic lines.
// Passing nil removes the sink and restores the os.Stderr fallback.
func SetDiagnostics(fn func(msg string)) {
	diagnosticsMu.Lock()
	defer diagnosticsMu.Unlock()
	diagnosticsFuncs = fn
}

// reportf formats one diagnostic line and routes it to the installed
// diagnostics sink, or — when no sink is installed — to os.Stderr with the
// exact same format string, pinning the pre-sink behavior.
func reportf(format string, args ...any) {
	diagnosticsMu.RLock()
	fn := diagnosticsFuncs
	diagnosticsMu.RUnlock()
	if fn != nil {
		fn(fmt.Sprintf(format, args...))
		return
	}
	fmt.Fprintf(os.Stderr, format, args...)
}
