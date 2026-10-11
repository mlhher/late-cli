package ast

import (
	"testing"
)

// runReadOnlyTable parses each command with the UnixParser and decides via a
// bare PolicyEngine (no allow-list) so the read-only classification is proven
// to stand on its own.
func runReadOnlyTable(t *testing.T, name string, commands []string, wantConfirm, wantBlocked bool) {
	t.Helper()
	p := &UnixParser{}
	pe := &PolicyEngine{}
	for _, command := range commands {
		t.Run(name+"/"+command, func(t *testing.T) {
			ir, err := p.Parse(command)
			if err != nil {
				t.Fatalf("Parse(%q): unexpected error %v (parse_errors=%v)", command, err, ir.ParseErrors)
			}
			d := pe.Decide(ir)
			if d.NeedsConfirmation != wantConfirm {
				t.Errorf("Decide(%q): NeedsConfirmation = %v, want %v (reasons=%v)", command, d.NeedsConfirmation, wantConfirm, d.ReasonCodes)
			}
			if d.IsBlocked != wantBlocked {
				t.Errorf("Decide(%q): IsBlocked = %v, want %v", command, d.IsBlocked, wantBlocked)
			}
		})
	}
}

// TestReadOnlyGHCommands_AutoApproved verifies that read-only gh operations —
// alone, piped to another read-only command, or chained with read-only
// operators — need no confirmation (and therefore no OTP re-evaluation).
func TestReadOnlyGHCommands_AutoApproved(t *testing.T) {
	runReadOnlyTable(t, "gh", []string{
		// gh api: GET-only forms.
		"gh api",
		"gh api /user",
		"gh api repos/cli/cli/pulls",
		"gh api --method GET /user",
		"gh api --method=HEAD /user",
		"gh api -X GET /user",
		"gh api -XGET /user",
		"gh api -i /user",
		// gh issue read-only actions.
		"gh issue view 123",
		"gh issue list",
		"gh issue status",
		"gh issue view 1 --json title -q .title",
		// gh pr read-only actions.
		"gh pr view 42",
		"gh pr list --state open",
		"gh pr checks 42",
		"gh pr diff 42",
		"gh pr status",
		// gh repo / search / run / release / label / gist / config / auth.
		"gh repo view cli/cli",
		"gh search code 'read only'",
		"gh search prs is:open",
		"gh run view 12345",
		"gh run list",
		"gh run watch 12345",
		"gh release view v1.0.0",
		"gh release list",
		"gh label list",
		"gh gist view abc123",
		"gh gist list",
		"gh config get editor",
		"gh auth status",
		// Pipelines and chains of read-only operations.
		"gh pr list | grep fix",
		"gh issue view 1 | head -5",
		"gh pr list > /dev/null", // /dev/null is a safe redirect target
		"gh api /user | jq .login",
		"gh pr diff 42 | wc -l",
		"gh run list | sort",
		"gh pr view && gh issue list",
		"gh pr list; gh run list",
		"gh issue list || gh pr list",
	}, false, false)
}

// TestReadOnlyGitCommands_AutoApproved verifies the git read-only split,
// including the flag-guarded branch/remote forms.
func TestReadOnlyGitCommands_AutoApproved(t *testing.T) {
	runReadOnlyTable(t, "git", []string{
		"git log",
		"git log --oneline -5",
		"git show HEAD",
		"git diff",
		"git diff --cached",
		"git status",
		"git status --porcelain",
		"git blame main.go",
		"git rev-parse HEAD",
		// git branch: flag-only listing forms.
		"git branch",
		"git branch -a",
		"git branch -vv",
		"git branch --list",
		"git branch --show-current",
		"git branch --sort=-committerdate",
		// git remote: bare, verbose, show/get-url.
		"git remote",
		"git remote -v",
		"git remote --verbose",
		"git remote show origin",
		"git remote get-url origin",
		// Pipelines and chains of read-only operations.
		"git log | head",
		"git diff | wc -l",
		"git status && git log -1",
		"git log --oneline | grep refactor",
	}, false, false)
}

// TestMutatingForms_StillGated verifies that mutating gh/git subcommands and
// gh api write verbs keep the current confirmation gate.
func TestMutatingForms_StillGated(t *testing.T) {
	runReadOnlyTable(t, "mutating", []string{
		// gh api write verbs and body flags.
		"gh api -X POST repos/cli/cli/issues",
		"gh api --method POST /user",
		"gh api --method=PUT /user/following/cli",
		"gh api -X PATCH /user",
		"gh api -X DELETE /user/following/cli",
		"gh api -X post /user",
		"gh api -f body=hello /repos/cli/cli/issues",
		"gh api --field title=x /repos/cli/cli/issues",
		"gh api -F count=1 /repos/cli/cli/issues",
		"gh api --raw-field body=x /repos/cli/cli/issues",
		"gh api --input payload.json /repos/cli/cli/issues",
		// gh mutating subcommands.
		"gh issue create --title x",
		"gh issue edit 1 --title x",
		"gh issue close 1",
		"gh pr create --title x",
		"gh pr merge 42",
		"gh pr ready 42",
		"gh repo create foo",
		"gh repo delete cli/cli",
		"gh repo fork cli/cli",
		"gh gist create notes.txt",
		"gh release create v1.0.0",
		"gh label create bug",
		"gh config set editor vim",
		"gh auth login",
		"gh run rerun 12345",
		"gh run cancel 12345",
		"gh codespace list",
		"gh ssh-key list",
		// git mutating subcommands.
		"git push origin main",
		"git commit -m x",
		"git add .",
		"git checkout -b feat",
		"git merge main",
		"git rebase main",
		"git reset --hard HEAD~1",
		"git clean -fd",
		"git stash",
		"git fetch",
		"git pull",
		"git tag v1.0.0",
		"git apply patch.diff",
		"git branch -D feat",
		"git branch -m old new",
		"git branch feat",
		"git remote add origin git@example.com:x.git",
		"git remote set-url origin git@example.com:x.git",
		"git remote prune origin",
		// git read-only subcommand with a write flag.
		"git diff --output=patch.diff",
		"git log -opatch.diff",
	}, true, false)
}

// TestUnknownSubcommands_KeepCurrentBehavior verifies that unknown or bare gh
// invocations keep the existing gate (NeedsConfirmation) instead of being
// classified read-only.
func TestUnknownSubcommands_KeepCurrentBehavior(t *testing.T) {
	runReadOnlyTable(t, "unknown", []string{
		"gh",
		"gh --version",
		"gh issue",
		"gh pr",
		"gh foobar list",
		"gh issue --web",
		"git",
		"git foobar",
	}, true, false)
}

// TestReadOnlyDangerousCombinations verifies that read-only commands combined
// with dangerous elements stay gated exactly as before.
func TestReadOnlyDangerousCombinations(t *testing.T) {
	p := &UnixParser{}
	pe := &PolicyEngine{}
	tests := []struct {
		command     string
		wantBlocked bool // hard block expected (redirect/cd), else soft gate
	}{
		// Chained into a mutating command.
		{command: "gh pr view 1 && rm -rf /tmp/x"},
		{command: "gh api /user && git push origin main"},
		{command: "gh issue list; gh pr merge 1"},
		{command: "git log && git push"},
		{command: "gh pr view 1 || gh issue create"},
		// Pipes into write-capable programs stay gated.
		{command: "gh pr list | xargs rm"},
		{command: "gh pr list | tee /tmp/leak.txt"},
		{command: "gh pr list | sh"},
		{command: "gh api /user | bash"},
		{command: "git log | git apply -"},
		// Command substitution / expansion on a read-only command.
		{command: "gh pr view $(cat pr.txt)"},
		{command: "git show $REF"},
		{command: "gh pr view $(gh pr list | head -1)"},
		// Redirections keep the hard block.
		{command: "gh pr view 1 > /tmp/out.md", wantBlocked: true},
		{command: "git log > /tmp/log.txt", wantBlocked: true},
		// sudo keeps the gate.
		{command: "sudo gh pr view 1"},
		{command: "sudo git log"},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			ir, err := p.Parse(tt.command)
			if err != nil {
				t.Fatalf("Parse(%q): unexpected error %v (parse_errors=%v)", tt.command, err, ir.ParseErrors)
			}
			d := pe.Decide(ir)
			if tt.wantBlocked {
				if !d.IsBlocked {
					t.Errorf("Decide(%q): expected IsBlocked, got confirm=%v blocked=%v", tt.command, d.NeedsConfirmation, d.IsBlocked)
				}
				return
			}
			if !d.NeedsConfirmation {
				t.Errorf("Decide(%q): expected NeedsConfirmation to stay gated (reasons=%v)", tt.command, d.ReasonCodes)
			}
			if d.IsBlocked {
				t.Errorf("Decide(%q): unexpected hard block (reasons=%v)", tt.command, d.ReasonCodes)
			}
		})
	}
}

// TestReadOnlyYieldsToContradictingAllowList verifies that the read-only
// bypass never widens an explicit allow-list approval: once "git log" was
// approved with --oneline only, "git log --all" keeps the gate even though
// git log is a read-only subcommand (strict flag matching is preserved).
func TestReadOnlyYieldsToContradictingAllowList(t *testing.T) {
	p := &UnixParser{}
	pe := &PolicyEngine{AllowedCommands: map[string]map[string]bool{
		"git log": {"--oneline": true},
	}}
	tests := []struct {
		command     string
		wantConfirm bool
	}{
		{command: "git log --oneline", wantConfirm: false}, // stored approval covers it
		{command: "git log --all", wantConfirm: true},      // flag not in the stored set
		{command: "git log -5", wantConfirm: true},         // numeric flag not stored
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			ir, err := p.Parse(tt.command)
			if err != nil {
				t.Fatalf("Parse(%q): unexpected error %v", tt.command, err)
			}
			d := pe.Decide(ir)
			if d.NeedsConfirmation != tt.wantConfirm {
				t.Errorf("Decide(%q): NeedsConfirmation = %v, want %v (reasons=%v)", tt.command, d.NeedsConfirmation, tt.wantConfirm, d.ReasonCodes)
			}
		})
	}
}

// TestReadOnlyFlagEmission pins down the adapter-level contract: the
// read_only flag is emitted only when every command in the statement is
// read-only, and it coexists with the operator flag for pipelines.
func TestReadOnlyFlagEmission(t *testing.T) {
	p := &UnixParser{}
	tests := []struct {
		command   string
		wantFlag  bool
		wantOpToo bool
	}{
		{command: "gh pr view 1", wantFlag: true},
		{command: "git status", wantFlag: true},
		{command: "gh pr list | grep fix", wantFlag: true, wantOpToo: true},
		{command: "gh pr merge 1", wantFlag: false},
		{command: "git push", wantFlag: false},
		{command: "gh pr view 1 && rm -rf /tmp", wantFlag: false},
		{command: "mkdir foo", wantFlag: false},
		{command: "gh pr view $PR", wantFlag: false}, // dynamic argument
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			ir, err := p.Parse(tt.command)
			if err != nil {
				t.Fatalf("Parse(%q): unexpected error %v", tt.command, err)
			}
			got := false
			for _, rc := range ir.RiskFlags {
				if rc == ReasonReadOnly {
					got = true
				}
			}
			if got != tt.wantFlag {
				t.Errorf("Parse(%q): read_only flag = %v, want %v (flags=%v)", tt.command, got, tt.wantFlag, ir.RiskFlags)
			}
			if tt.wantOpToo {
				hasOp := false
				for _, rc := range ir.RiskFlags {
					if rc == ReasonOperator {
						hasOp = true
					}
				}
				if !hasOp {
					t.Errorf("Parse(%q): expected operator flag alongside read_only (flags=%v)", tt.command, ir.RiskFlags)
				}
			}
		})
	}
}
