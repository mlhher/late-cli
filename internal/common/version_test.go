package common

import (
	"strings"
	"testing"
)

func TestVersionDisplay(t *testing.T) {
	tests := []struct {
		name      string
		version   string
		buildNum  string
		commit    string
		buildDate string
		want      string
	}{
		{
			name:      "all four pieces stamped, order is build, commit, built",
			version:   "2.0.0-rc.1",
			buildNum:  "1239",
			commit:    "2fe0e83",
			buildDate: "2026-09-25T13:12:11Z",
			want:      "late 2.0.0-rc.1 (build 1239, commit 2fe0e83, built 2026-09-25T13:12:11Z)",
		},
		{
			name:     "build number only",
			version:  "2.0.0-rc.1",
			buildNum: "1239",
			want:     "late 2.0.0-rc.1 (build 1239)",
		},
		{
			name:      "build number and build date omit the commit",
			version:   "2.0.0-rc.1",
			buildNum:  "1239",
			buildDate: "2026-09-25T13:12:11Z",
			want:      "late 2.0.0-rc.1 (build 1239, built 2026-09-25T13:12:11Z)",
		},
		{
			name:      "commit and build date stamped without build number",
			version:   "2.0.0-rc.1",
			commit:    "2fe0e83",
			buildDate: "2026-09-25T10:57:00+02:00",
			want:      "late 2.0.0-rc.1 (commit 2fe0e83, built 2026-09-25T10:57:00+02:00)",
		},
		{
			name:    "commit only",
			version: "2.0.0-rc.1",
			commit:  "2fe0e83",
			want:    "late 2.0.0-rc.1 (commit 2fe0e83)",
		},
		{
			name:      "build date only",
			version:   "2.0.0-rc.1",
			buildDate: "2026-09-25T08:57:00Z",
			want:      "late 2.0.0-rc.1 (built 2026-09-25T08:57:00Z)",
		},
		{
			name:    "nothing stamped",
			version: "2.0.0-rc.1",
			want:    "late 2.0.0-rc.1",
		},
		{
			name:    "dev with nothing stamped is the bare dev banner",
			version: "dev",
			want:    "late dev",
		},
		{
			name:      "unknown sentinels are treated as unstamped",
			version:   "2.0.0-rc.1",
			buildNum:  "unknown",
			commit:    "unknown",
			buildDate: "unknown",
			want:      "late 2.0.0-rc.1",
		},
		{
			name:     "empty build number is treated as unstamped",
			version:  "2.0.0-rc.1",
			buildNum: "",
			commit:   "2fe0e83",
			want:     "late 2.0.0-rc.1 (commit 2fe0e83)",
		},
		{
			name:     "dev with only the build number stamped keeps it",
			version:  "dev",
			buildNum: "1239",
			want:     "late dev (build 1239)",
		},
		{
			name:      "dev with only the build date stamped keeps the date",
			version:   "dev",
			buildDate: "2026-09-25T08:57:00Z",
			want:      "late dev (built 2026-09-25T08:57:00Z)",
		},
		{
			name:    "dev with only the commit stamped keeps the commit",
			version: "dev",
			commit:  "2fe0e83",
			want:    "late dev (commit 2fe0e83)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := versionDisplay(tt.version, tt.buildNum, tt.commit, tt.buildDate)
			if got != tt.want {
				t.Errorf("versionDisplay(%q, %q, %q, %q) = %q, want %q", tt.version, tt.buildNum, tt.commit, tt.buildDate, got, tt.want)
			}
			// The output must stay on one line: it feeds -version stdout
			// and single-row parsers.
			if strings.ContainsAny(got, "\n\r") {
				t.Errorf("versionDisplay(...) = %q, must not contain line breaks", got)
			}
		})
	}
}

func TestVersionDisplayShort(t *testing.T) {
	tests := []struct {
		name     string
		version  string
		buildNum string
		commit   string
		want     string
	}{
		{
			name:     "build number precedes the commit",
			version:  "2.0.0-rc.1",
			buildNum: "1239",
			commit:   "2fe0e83",
			want:     "2.0.0-rc.1 · b1239 · 2fe0e83",
		},
		{
			name:     "build number only",
			version:  "2.0.0-rc.1",
			buildNum: "1239",
			want:     "2.0.0-rc.1 · b1239",
		},
		{
			name:    "commit only",
			version: "2.0.0-rc.1",
			commit:  "2fe0e83",
			want:    "2.0.0-rc.1 · 2fe0e83",
		},
		{
			name:     "unknown build number degrades to version and commit",
			version:  "2.0.0-rc.1",
			buildNum: "unknown",
			commit:   "2fe0e83",
			want:     "2.0.0-rc.1 · 2fe0e83",
		},
		{
			name:     "empty build number degrades to version and commit",
			version:  "2.0.0-rc.1",
			buildNum: "",
			commit:   "2fe0e83",
			want:     "2.0.0-rc.1 · 2fe0e83",
		},
		{
			name:    "unknown commit degrades to the bare version",
			version: "2.0.0-rc.1",
			commit:  "unknown",
			want:    "2.0.0-rc.1",
		},
		{
			name:    "empty commit degrades to the bare version",
			version: "2.0.0-rc.1",
			want:    "2.0.0-rc.1",
		},
		{
			name:    "dev with unknown commit",
			version: "dev",
			want:    "dev",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := versionDisplayShort(tt.version, tt.buildNum, tt.commit)
			if got != tt.want {
				t.Errorf("versionDisplayShort(%q, %q, %q) = %q, want %q", tt.version, tt.buildNum, tt.commit, got, tt.want)
			}
			if strings.ContainsAny(got, "\n\r") {
				t.Errorf("versionDisplayShort(...) = %q, must not contain line breaks", got)
			}
		})
	}
}

// TestVersionDisplayWrappersUseVars pins that the thin var-backed wrappers
// plumb the package vars through (the build-tag-free injection point used
// by the TUI tests).
func TestVersionDisplayWrappersUseVars(t *testing.T) {
	origVersion, origBuildNum, origCommit, origBuildDate := Version, BuildNumber, Commit, BuildDate
	defer func() { Version, BuildNumber, Commit, BuildDate = origVersion, origBuildNum, origCommit, origBuildDate }()

	Version, BuildNumber, Commit, BuildDate = "2.0.0-rc.1", "1239", "2fe0e83", "2026-09-25T10:57:00+02:00"
	if got, want := VersionDisplay(), "late 2.0.0-rc.1 (build 1239, commit 2fe0e83, built 2026-09-25T10:57:00+02:00)"; got != want {
		t.Errorf("VersionDisplay() = %q, want %q", got, want)
	}
	if got, want := VersionDisplayShort(), "2.0.0-rc.1 · b1239 · 2fe0e83"; got != want {
		t.Errorf("VersionDisplayShort() = %q, want %q", got, want)
	}

	// A plain `go build` (all vars at their source defaults) is the bare
	// dev banner.
	Version, BuildNumber, Commit, BuildDate = "dev", "unknown", "unknown", "unknown"
	if got := VersionDisplay(); got != "late dev" {
		t.Errorf("VersionDisplay() = %q, want %q", got, "late dev")
	}
	if got := VersionDisplayShort(); got != "dev" {
		t.Errorf("VersionDisplayShort() = %q, want %q", got, "dev")
	}
}
