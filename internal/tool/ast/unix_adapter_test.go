package ast

import (
	"testing"
)

func TestUnixParser_SimpleCommands(t *testing.T) {
	p := &UnixParser{}
	tests := []struct {
		command     string
		wantCmds    []string
		wantRisk    []ReasonCode
		wantBlocked bool // proxy: ReasonCd or ReasonRedirect present
	}{
		{
			command:  "ls",
			wantCmds: []string{"ls"},
			wantRisk: nil,
		},
		{
			command:  "ls -la",
			wantCmds: []string{"ls"},
			wantRisk: nil,
		},
		{
			command:  "ls | grep foo",
			wantCmds: []string{"ls", "grep"},
			wantRisk: []ReasonCode{ReasonOperator},
		},
		{
			command:  "ls; pwd",
			wantCmds: []string{"ls", "pwd"},
			wantRisk: []ReasonCode{ReasonOperator},
		},
		{
			command:  "echo $HOME",
			wantCmds: []string{"echo"},
			wantRisk: []ReasonCode{ReasonExpansion},
		},
		{
			command:     "cd /tmp",
			wantCmds:    []string{"cd"},
			wantBlocked: true,
			wantRisk:    []ReasonCode{ReasonCd},
		},
		{
			command:     "ls > out.txt",
			wantCmds:    []string{"ls"},
			wantBlocked: true,
			wantRisk:    []ReasonCode{ReasonRedirect},
		},
		{
			command:  "ls > /dev/null",
			wantCmds: []string{"ls"},
			wantRisk: nil, // safe redirect — no ReasonRedirect
		},
		{
			command:  "echo $(ls)",
			wantCmds: []string{"echo"},
			wantRisk: []ReasonCode{ReasonSubshell},
		},
		{
			command:  "(ls)",
			wantCmds: []string{"ls"},
			wantRisk: []ReasonCode{ReasonSubshell},
		},
		{
			command:  "git log --oneline",
			wantCmds: []string{"git log"},
			wantRisk: nil,
		},
		{
			command:  "go mod tidy && go test ./...",
			wantCmds: []string{"go mod", "go test"},
			wantRisk: []ReasonCode{ReasonOperator},
		},
	}

	for _, tc := range tests {
		t.Run(tc.command, func(t *testing.T) {
			ir, _ := p.Parse(tc.command)

			if ir.Version != IRVersion {
				t.Errorf("Version: got %q want %q", ir.Version, IRVersion)
			}
			if ir.Platform != PlatformUnix {
				t.Errorf("Platform: got %q want %q", ir.Platform, PlatformUnix)
			}

			for _, wantCmd := range tc.wantCmds {
				found := false
				for _, c := range ir.Commands {
					if c == wantCmd {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected command %q in %v", wantCmd, ir.Commands)
				}
			}

			for _, wantRC := range tc.wantRisk {
				if !hasRisk(ir, wantRC) {
					t.Errorf("expected risk flag %q in %v", wantRC, ir.RiskFlags)
				}
			}

			gotBlocked := hasRisk(ir, ReasonCd) || hasRisk(ir, ReasonRedirect)
			if gotBlocked != tc.wantBlocked {
				t.Errorf("wantBlocked=%v but blocking risk flags present=%v (flags=%v)",
					tc.wantBlocked, gotBlocked, ir.RiskFlags)
			}
		})
	}
}

func TestUnixParser_SyntaxError(t *testing.T) {
	p := &UnixParser{}
	ir, err := p.Parse("echo $(")
	if err == nil {
		t.Error("expected error for invalid syntax")
	}
	if !hasRisk(ir, ReasonSyntaxError) {
		t.Errorf("expected ReasonSyntaxError in %v", ir.RiskFlags)
	}
	if len(ir.ParseErrors) == 0 {
		t.Error("expected non-empty ParseErrors")
	}
}

func TestUnixParser_SafeRedirectsNotFlagged(t *testing.T) {
	p := &UnixParser{}
	for _, cmd := range []string{
		"ls > /dev/null",
		"ls > /dev/stdout",
		"ls 2> /dev/stderr",
		"ls 2>&1",
		"ls 2>&-",
	} {
		ir, _ := p.Parse(cmd)
		if hasRisk(ir, ReasonRedirect) {
			t.Errorf("%q: got ReasonRedirect but should be safe", cmd)
		}
	}
}

func TestUnixParser_Operators(t *testing.T) {
	p := &UnixParser{}
	ir, _ := p.Parse("git status && go test ./...")
	if !hasRisk(ir, ReasonOperator) {
		t.Error("expected ReasonOperator for &&")
	}
	foundOp := false
	for _, op := range ir.Operators {
		if op == "&&" {
			foundOp = true
		}
	}
	if !foundOp {
		t.Errorf("expected && in Operators, got %v", ir.Operators)
	}
}
