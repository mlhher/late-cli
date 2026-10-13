package common

import "strings"

// Version is the release identifier, set via -ldflags -X (default "dev").
var Version = "dev"

// BuildNumber is the commit count from HEAD, set via -ldflags -X.
var BuildNumber = "unknown"

// Commit is the short git commit hash, set via -ldflags -X.
var Commit = "unknown"

// BuildDate is the UTC build timestamp (RFC3339), set via -ldflags -X.
var BuildDate = "unknown"

// versionDisplay renders the one-line build identity:
// "late <version> (build <N>, commit <hash>, built <date>)".
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

// VersionDisplay renders the full build identity for -version.
func VersionDisplay() string {
	return versionDisplay(Version, BuildNumber, Commit, BuildDate)
}

// versionDisplayShort renders a compact build identity: "<version> · b<build> · <commit>".
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

// VersionDisplayShort renders the short build identity from package vars.
func VersionDisplayShort() string {
	return versionDisplayShort(Version, BuildNumber, Commit)
}
