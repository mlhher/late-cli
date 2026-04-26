package tool

import (
	"encoding/json"
	"testing"
)

func TestAnalyzeBashCommand(t *testing.T) {
	st := &ShellTool{}

	tests := []struct {
		desc          string
		command       string
		expectBlocked bool
		expectConfirm bool
	}{
		{"Simple ls", "ls", false, false},
		{"Simple grep", "grep foo bar", false, false},
		{"Echo quoted (auto-approve)", "echo \"hello world\"", false, false},
		{"Date (auto-approve)", "date", false, false},
		{"Echo with expansion (confirm)", "echo \"hello $USER\"", false, true},
		{"Blocked cd", "cd /tmp", true, true},
		{"Blocked redirect", "ls > out.txt", true, true},
		{"Blocked append", "echo foo >> bar.txt", true, true},
		{"Blocked redirect with &", "ls &> out.txt", true, true},
		{"Safe pipe (auto-approve)", "ls | grep foo", false, false},
		{"Complex pipe (needs confirm)", "ls | grep foo | xargs rm", false, true},
		{"Nested subshell (needs confirm)", "(ls)", false, true},
		{"Command subst (needs confirm)", "echo $(ls)", false, true},
		{"Whitelisted list", "ls; pwd", false, false},
		{"Non-whitelisted", "mkdir foo", false, true},
		{"Combined cd & ls (blocked)", "cd /tmp; ls", true, true},
		{"Quoted cd (blocked)", "'cd' /tmp", true, true},
		{"Variable expansion (needs confirm)", "echo $HOME", false, true},
		{"Path-based command (blocked bypass)", "/bin/ls", false, true},
		{"Local path command (blocked bypass)", "./ls", false, true},
		{"Git status (auto-approve)", "git status", false, false},
		{"Git branch (needs confirm)", "git branch", false, true},
		{"Git commit (needs confirm)", "git commit -m 'foo'", false, true},
		{"Go doc (auto-approve)", "go doc fmt", false, false},
		{"Go run (needs confirm)", "go run main.go", false, true},
		{"Find safe (auto-approve)", "find . -name '*.go'", false, false},
		{"Find exec (needs confirm)", "find . -exec rm {} \\;", false, true},
		{"Find delete (needs confirm)", "find . -delete", false, true},
		{"Safe env var (auto-approve)", "DEBUG=1 ls", false, false},
		{"Unsafe env var (needs confirm)", "PAGER=rm ls", false, true},
		{"Hijack attempt (needs confirm)", "GIT_ASKPASS=/tmp/evil git remote", false, true},
		{"Lsof (auto-approve)", "lsof -i :8080", false, false},
		{"Stat (auto-approve)", "stat main.go", false, false},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			analyzer := &BashAnalyzer{}
			analysis := analyzer.Analyze(tc.command)
			if analysis.IsBlocked != tc.expectBlocked {
				t.Errorf("blocked mismatch (analyzer): got %v, want %v", analysis.IsBlocked, tc.expectBlocked)
			}
			if analysis.NeedsConfirmation != tc.expectConfirm {
				t.Errorf("confirm mismatch (analyzer): got %v, want %v", analysis.NeedsConfirmation, tc.expectConfirm)
			}

			blocked, _, confirm := st.analyzeBashCommand(tc.command, "")
			if blocked != tc.expectBlocked {
				t.Errorf("blocked mismatch (shelltool): got %v, want %v", blocked, tc.expectBlocked)
			}
			if confirm != tc.expectConfirm {
				t.Errorf("confirm mismatch (shelltool): got %v, want %v", confirm, tc.expectConfirm)
			}

			// Also test RequiresConfirmation with marshaled args
			args, _ := json.Marshal(map[string]string{"command": tc.command})
			if st.RequiresConfirmation(args) != tc.expectConfirm {
				t.Errorf("RequiresConfirmation mismatch: got %v, want %v", st.RequiresConfirmation(args), tc.expectConfirm)
			}
		})
	}
}
