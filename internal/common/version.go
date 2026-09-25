package common

import "strings"

// Version is the release identifier (e.g. "2.0.0-rc.1"), set via
// -ldflags -X at build time; it stays "dev" for a plain `go build`.
var Version = "dev"

// BuildNumber is the deterministic build ordinal — the commit count of the
// current HEAD (`git rev-list --count HEAD`), set via -ldflags -X at build
// time (Makefile: BUILD_NUMBER). It is stable for the same commit, grows
// monotonically as the repo grows, and needs no network; it stays "unknown"
// when not stamped (plain `go build`) or when git is unavailable (tarball
// without .git).
var BuildNumber = "unknown"

// Commit is the short git commit the binary was built from, set via
// -ldflags -X at build time (Makefile: `git rev-parse --short HEAD`);
// it stays "unknown" when not stamped (plain `go build`, tarball
// without .git).
var Commit = "unknown"

// BuildDate is the UTC build timestamp (RFC3339), set via -ldflags -X
// at build time (Makefile: `date -u +%Y-%m-%dT%H:%M:%SZ`); it stays
// "unknown" when not stamped.
var BuildDate = "unknown"

// versionDisplay renders the one-line build identity from injected
// values, so tests can exercise every degradation without mutating the
// package vars:
//
//	late 2.0.0-rc.1 (build 1239, commit 2fe0e83, built 2026-09-25T13:12:11Z)
//
// Degradations (the output stays on one line):
//   - unknown build number ("unknown" or empty) → the build number is omitted;
//   - unknown commit ("unknown" or empty) → the commit is omitted;
//   - unknown date ("unknown" or empty) → the date is omitted;
//   - dev version with nothing stamped (plain `go build`) → bare "late dev".
func versionDisplay(version, buildNumber, commit, buildDate string) string {
	buildKnown := buildNumber != "" && buildNumber != "unknown"
	commitKnown := commit != "" && commit != "unknown"
	dateKnown := buildDate != "" && buildDate != "unknown"
	if version == "dev" && !buildKnown && !commitKnown && !dateKnown {
		return "late dev"
	}
	display := "late " + version
	var meta []string
	if buildKnown {
		meta = append(meta, "build "+buildNumber)
	}
	if commitKnown {
		meta = append(meta, "commit "+commit)
	}
	if dateKnown {
		meta = append(meta, "built "+buildDate)
	}
	if len(meta) > 0 {
		display += " (" + strings.Join(meta, ", ") + ")"
	}
	return display
}

// VersionDisplay renders the full one-line build identity for -version
// from the stamped package vars. See versionDisplay for the format and
// its graceful degradations.
func VersionDisplay() string {
	return versionDisplay(Version, BuildNumber, Commit, BuildDate)
}

// versionDisplayShort renders the compact identity used where only one
// row must fit (the TUI info bar): "<version>[ · b<build number>][ ·
// <commit>]" — the build number comes first, then the short commit. Each
// unknown piece degrades silently to just the version, and the build date
// is deliberately left out.
func versionDisplayShort(version, buildNumber, commit string) string {
	parts := make([]string, 0, 3)
	if version != "" {
		parts = append(parts, version)
	}
	if buildNumber != "" && buildNumber != "unknown" {
		parts = append(parts, "b"+buildNumber)
	}
	if commit != "" && commit != "unknown" {
		parts = append(parts, commit)
	}
	return strings.Join(parts, " · ")
}

// VersionDisplayShort renders the short build identity from the stamped
// package vars. See versionDisplayShort for the format.
func VersionDisplayShort() string {
	return versionDisplayShort(Version, BuildNumber, Commit)
}
