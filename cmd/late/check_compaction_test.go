package main

import (
	"bytes"
	"flag"
	"io"
	"strings"
	"testing"

	"late/internal/compaction"
)

// TestCheckCompactionFlagParses pins the -check-compaction flag's CLI shape:
// it is a boolean (no value name in help), it defaults to false, it pairs
// with -compaction-mode, and it renders in the grouped help under Context
// compaction.
func TestCheckCompactionFlagParses(t *testing.T) {
	newFlagSet := func() (*flag.FlagSet, *bool) {
		fs := flag.NewFlagSet("check-test", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		check := fs.Bool("check-compaction", false, "usage of check-compaction")
		fs.String("compaction-mode", "", "usage of compaction-mode")
		return fs, check
	}

	// Default: off.
	fs, check := newFlagSet()
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("Parse(nil): %v", err)
	}
	if *check {
		t.Error("-check-compaction defaults to true, want false")
	}

	// Set, alone.
	fs, check = newFlagSet()
	if err := fs.Parse([]string{"-check-compaction"}); err != nil {
		t.Fatalf("Parse(-check-compaction): %v", err)
	}
	if !*check {
		t.Error("-check-compaction did not parse as true")
	}

	// Set, paired with -compaction-mode (the documented pairing: the check
	// resolves the backend the same way a run with that mode would).
	fs, check = newFlagSet()
	if err := fs.Parse([]string{"-check-compaction", "-compaction-mode=enabled"}); err != nil {
		t.Fatalf("Parse(-check-compaction -compaction-mode=enabled): %v", err)
	}
	if !*check {
		t.Error("-check-compaction did not parse as true when paired with -compaction-mode")
	}
	if got := fs.Lookup("compaction-mode").Value.String(); got != "enabled" {
		t.Errorf("-compaction-mode = %q, want enabled", got)
	}

	// Help rendering: the flag shows up exactly once in the grouped output.
	var buf bytes.Buffer
	writeHelp(&buf, newHelpTestFlagSet(t))
	if n := countRenderedFlagLines(buf.String(), "check-compaction"); n != 1 {
		t.Errorf("-check-compaction rendered %d times in help, want exactly 1:\n%s", n, buf.String())
	}
}

// TestNoBackendCheckReport pins the stage-0 failure report: the guidance
// sentence, the FAIL verdict naming the backend stage, and no question/gate/
// expand rows — the real stages cannot run without a backend.
func TestNoBackendCheckReport(t *testing.T) {
	results := noBackendCheckResults(errNoBackendForTest())
	if len(results) != 1 {
		t.Fatalf("got %d results, want exactly the stage-0 backend failure", len(results))
	}
	if results[0].Stage != compaction.CheckStageBackend || results[0].OK {
		t.Fatalf("results[0] = %+v, want a failing %q stage", results[0], compaction.CheckStageBackend)
	}

	out := compaction.FormatCheckReport(results, false)
	for _, want := range []string{
		"late compaction preflight",
		"[FAIL] " + compaction.CheckStageBackend,
		"no compaction backend configured (set the provider key or run with -compaction-mode pointing at a gateway)",
		"no System One backend available", // the resolver's typed reason
		`result: FAIL (stage "backend" failed)`,
		"cost: n/a (the decisions client does not track token usage)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
	for _, banned := range []string{"[ok  ] " + compaction.CheckStageQuestions, "[FAIL] " + compaction.CheckStageGate, "[FAIL] " + compaction.CheckStageExpand} {
		if strings.Contains(out, banned) {
			t.Errorf("report must not run the %q stage without a backend:\n%s", banned, out)
		}
	}
}

// errNoBackendForTest builds the resolver's no-backend error without touching
// the process environment (a test machine may carry real provider keys, and
// runCompactionCheck must never be exercised against them).
func errNoBackendForTest() error {
	return &compaction.NoBackendError{
		Detail: "typesafe: no API key for backend \"typesafe\": set TYPESAFE_API_KEY=<key> in the environment, or write the key to /keys/compaction-typesafe.key",
	}
}
