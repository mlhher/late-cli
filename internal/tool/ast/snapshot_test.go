package ast

import (
	"testing"
)

// snapshotEntry records the expected Decision for a command in the corpus.
// These are derived from the existing BashAnalyzer/PowerShellAnalyzer baselines.
type snapshotEntry struct {
	command     string
	wantBlocked bool
	wantConfirm bool
}

// unixCorpus mirrors the table in bash_analyzer_test.go so that the AST
// policy engine can be validated against the established baseline.
var unixCorpus = []snapshotEntry{
	{"ls", false, false},
	{"ls -la", false, false},
	{"ls -rt", false, false},
	{"date", false, false},
	{"echo 'hello world'", false, false},
	{"echo $HOME", false, true},         // expansion
	{"cd /tmp", true, true},             // cd blocked
	{"ls > out.txt", true, true},        // redirect blocked
	{"echo foo >> bar.txt", true, true}, // redirect blocked
	{"ls | grep foo", false, false},     // safe pipe (allowlisted in corpus)
	{"(ls)", false, true},               // subshell
	{"echo $(ls)", false, true},         // subshell
	{"ls; pwd", false, false},           // safe compound
	{"mkdir foo", false, true},          // unknown command
	{"cd /tmp; ls", true, true},         // cd in compound
	{"ls > /dev/null", false, false},    // safe redirect
	{"ls 2>&1", false, false},           // safe fd dup
}

// TestUnixCorpusSnapshot verifies the AST policy engine against the
// known-good baseline corpus. Commands that reference the allow-list
// (e.g. "ls | grep") use a pre-seeded AllowedCommands map.
func TestUnixCorpusSnapshot(t *testing.T) {
	p := &UnixParser{}
	pe := &PolicyEngine{
		AllowedCommands: map[string]map[string]bool{
			"ls":   {"-la": true, "-rt": true},
			"grep": {},
			"pwd":  {},
			"date": {},
			"echo": {},
			"head": {},
			"tail": {},
			"git":  {},
			"go":   {},
		},
	}

	for _, tc := range unixCorpus {
		t.Run(tc.command, func(t *testing.T) {
			ir, _ := p.Parse(tc.command)
			d := pe.Decide(ir)

			if d.IsBlocked != tc.wantBlocked {
				t.Errorf("IsBlocked: got %v want %v (flags=%v)", d.IsBlocked, tc.wantBlocked, ir.RiskFlags)
			}
			if d.NeedsConfirmation != tc.wantConfirm {
				t.Errorf("NeedsConfirmation: got %v want %v (flags=%v)", d.NeedsConfirmation, tc.wantConfirm, ir.RiskFlags)
			}
		})
	}
}

