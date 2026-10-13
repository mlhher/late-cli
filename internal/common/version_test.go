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
			name:      "all four pieces stamped",
			version:   "2.0.0-rc.1",
			buildNum:  "1239",
			commit:    "2fe0e83",
			buildDate: "2026-09-25T13:12:11Z",
			want:      "late 2.0.0-rc.1 (build 1239, commit 2fe0e83, built 2026-09-25T13:12:11Z)",
		},
		{
			name:    "unstamped dev",
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
			name:     "partial stamping",
			version:  "2.0.0-rc.1",
			buildNum: "1239",
			commit:   "2fe0e83",
			want:     "late 2.0.0-rc.1 (build 1239, commit 2fe0e83)",
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
			name:     "all pieces stamped",
			version:  "2.0.0-rc.1",
			buildNum: "1239",
			commit:   "2fe0e83",
			want:     "2.0.0-rc.1 · b1239 · 2fe0e83",
		},
		{
			name:    "unstamped dev",
			version: "dev",
			want:    "dev",
		},
		{
			name:     "unknown sentinels degrade to version",
			version:  "2.0.0-rc.1",
			buildNum: "unknown",
			commit:   "unknown",
			want:     "2.0.0-rc.1",
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
