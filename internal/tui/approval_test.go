package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"late/internal/client"
	"late/internal/common"
	"late/internal/tool"

	"github.com/charmbracelet/x/ansi"
)

func toolCall(name string, args any) client.ToolCall {
	raw, _ := json.Marshal(args)
	return client.ToolCall{ID: "call_1", Function: client.FunctionCall{Name: name, Arguments: string(raw)}}
}

func TestFencedBlockOutgrowsBackticksInContent(t *testing.T) {
	got := fencedBlock("bash", "echo ```hi```")
	if !strings.HasPrefix(got, "````bash\n") || !strings.HasSuffix(got, "\n````") {
		t.Fatalf("fence did not grow past the content's backtick run:\n%s", got)
	}
	if got := fencedBlock("json", "{}\n\n"); got != "```json\n{}\n```" {
		t.Fatalf("plain block = %q", got)
	}
}

func TestShellApprovalShowsCommandCwdAndWhatWillBeRemembered(t *testing.T) {
	p := buildApprovalPreview(toolCall("bash", map[string]string{
		"command": "go test -race ./... && git status",
		"cwd":     "/work/repo",
	}))
	for _, want := range []string{"go test -race ./... && git status", "/work/repo", "```bash"} {
		if !strings.Contains(p.heading, want) {
			t.Errorf("heading missing %q:\n%s", want, p.heading)
		}
	}
	for _, want := range []string{"go test (flags: -race)", "git status (no flags)"} {
		if !strings.Contains(p.remember, want) {
			t.Errorf("remember line missing %q: %q", want, p.remember)
		}
	}
}

func TestShellApprovalUnrememberableCommandSaysSo(t *testing.T) {
	p := buildApprovalPreview(toolCall("bash", map[string]string{"command": "$CMD --flag"}))
	if !strings.Contains(p.remember, "can't be remembered") {
		t.Fatalf("expected an honest note for a command the allow-list cannot hold, got %q", p.remember)
	}
}

func TestDescribeCommandScopeDedupesAndCaps(t *testing.T) {
	got := describeCommandScope(map[string][]string{
		"a": {"-x", "-x", "-b"}, "b": nil, "c": nil, "d": nil, "e": nil, "f": nil,
	})
	if !strings.Contains(got, "a (flags: -b, -x)") {
		t.Errorf("flags not deduped and sorted: %q", got)
	}
	if !strings.Contains(got, "+2 more") {
		t.Errorf("long scope not capped: %q", got)
	}
	if describeCommandScope(nil) != "" {
		t.Error("empty scope should describe as empty")
	}
}

func TestWriteFileApprovalDiffsAgainstExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(path, []byte("package main\n\nfunc a() {}\nfunc b() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := buildApprovalPreview(toolCall("write_file", map[string]string{
		"path": path, "content": "package main\n\nfunc a() {}\nfunc c() {}\nfunc d() {}\n",
	}))
	added, deleted := diffStats(p.diff)
	if added != 2 || deleted != 1 {
		t.Fatalf("stats = +%d -%d, want +2 -1", added, deleted)
	}
	if strings.Contains(p.heading, "new file") {
		t.Error("existing file reported as new")
	}
	if !strings.Contains(p.remember, "write_file") || !strings.Contains(p.remember, "any path") {
		t.Errorf("remember line should warn that the grant covers every path: %q", p.remember)
	}
}

func TestWriteFileApprovalNewFileAndNoOp(t *testing.T) {
	dir := t.TempDir()

	fresh := buildApprovalPreview(toolCall("write_file", map[string]string{
		"path": filepath.Join(dir, "new.txt"), "content": "x\ny\n",
	}))
	if !strings.Contains(fresh.heading, "(new file)") || !strings.Contains(fresh.note, "2 lines") {
		t.Errorf("new file not described: heading=%q note=%q", fresh.heading, fresh.note)
	}
	if added, deleted := diffStats(fresh.diff); added != 2 || deleted != 0 {
		t.Errorf("new file stats = +%d -%d", added, deleted)
	}

	same := filepath.Join(dir, "same.txt")
	_ = os.WriteFile(same, []byte("hello\n"), 0o644)
	noop := buildApprovalPreview(toolCall("write_file", map[string]string{"path": same, "content": "hello\n"}))
	if noop.diff != nil || !strings.Contains(noop.note, "no changes") {
		t.Errorf("identical write should say so: diff=%v note=%q", noop.diff, noop.note)
	}
}

func TestWriteFileApprovalTooLargeSkipsTheDiff(t *testing.T) {
	huge := strings.Repeat("x\n", maxApprovalPreviewBytes)
	p := buildApprovalPreview(toolCall("write_file", map[string]string{"path": "big.txt", "content": huge}))
	if p.diff != nil || !strings.Contains(p.note, "too large to preview") {
		t.Fatalf("oversized write should skip the diff and say why: note=%q", p.note)
	}
}

func TestTargetEditApprovalNumbersLinesFromTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.go")
	_ = os.WriteFile(path, []byte("one\ntwo\nthree\nfour\nfive\n"), 0o644)

	p := buildApprovalPreview(toolCall("target_edit", map[string]string{
		"file": path, "search": "three\nfour", "replace": "three\nFOUR",
	}))
	var deleted *diffLine
	for i := range p.diff {
		if p.diff[i].kind == diffDel {
			deleted = &p.diff[i]
		}
	}
	if deleted == nil || deleted.text != "four" || deleted.oldNo != 4 {
		t.Fatalf("deleted line = %+v, want 'four' at line 4", deleted)
	}
}

func TestTargetEditApprovalWithoutFileHasNoLineNumbers(t *testing.T) {
	p := buildApprovalPreview(toolCall("target_edit", map[string]string{
		"file": filepath.Join(t.TempDir(), "missing.go"), "search": "a", "replace": "b",
	}))
	for _, op := range p.diff {
		if op.oldNo != 0 || op.newNo != 0 {
			t.Fatalf("expected no numbers when the file cannot be located, got %+v", op)
		}
	}
}

func TestGenericApprovalPrettyPrintsJSON(t *testing.T) {
	p := buildApprovalPreview(toolCall("fetch_page", map[string]any{"url": "https://example.com", "depth": 2}))
	if !strings.Contains(p.heading, "```json\n{\n  \"") {
		t.Errorf("arguments not indented:\n%s", p.heading)
	}
	if !strings.Contains(p.remember, "every fetch_page call") {
		t.Errorf("remember line = %q", p.remember)
	}

	broken := buildApprovalPreview(client.ToolCall{Function: client.FunctionCall{Name: "x", Arguments: "{not json"}})
	if !strings.Contains(broken.heading, "{not json") {
		t.Errorf("invalid arguments should be shown verbatim:\n%s", broken.heading)
	}
}

func TestApprovalRenderStaysWithinWidth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.txt")
	_ = os.WriteFile(path, []byte(strings.Repeat("old line\n", 30)), 0o644)
	p := buildApprovalPreview(toolCall("write_file", map[string]string{
		"path": path, "content": strings.Repeat("new line that is quite long indeed\n", 30),
	}))
	for _, width := range []int{20, 33, 60, 120} {
		out := p.render(width, 12)
		for i, line := range strings.Split(out, "\n") {
			if w := ansi.StringWidth(line); w > width {
				t.Errorf("width %d: row %d is %d cells: %q", width, i, w, ansi.Strip(line))
			}
		}
		if !strings.Contains(strings.Join(strings.Fields(ansi.Strip(out)), " "), "more lines not shown") {
			t.Errorf("width %d: capped diff did not say it was capped", width)
		}
	}
}

func TestApprovalRenderListsEveryAnswerKey(t *testing.T) {
	out := ansi.Strip(buildApprovalPreview(toolCall("bash", map[string]string{"command": "make"})).render(80, 10))
	for _, want := range []string{"y allow once", "s session", "p project", "g global", "n deny", "type a reply"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// The durations quoted come from the permission store, not a copy of them.
	session, project, _ := tool.ApprovalTTLs()
	if !strings.Contains(out, shortDuration(session)) || !strings.Contains(out, shortDuration(project)) {
		t.Errorf("approval lifetimes missing in:\n%s", out)
	}
}

func TestShortDuration(t *testing.T) {
	session, project, global := tool.ApprovalTTLs()
	for d, want := range map[string]string{
		shortDuration(session): "30m",
		shortDuration(project): "30d",
		shortDuration(global):  "30d",
	} {
		if d != want {
			t.Errorf("got %q, want %q", d, want)
		}
	}
}

func TestApprovalRenderNarrowWidthWrapsChips(t *testing.T) {
	rows := approvalActionRows(24)
	if len(rows) < 2 {
		t.Fatalf("expected chips to wrap at 24 columns, got %d row(s)", len(rows))
	}
}

func TestConfirmMiddlewareDenyWithReasonTellsTheAgent(t *testing.T) {
	answer := denyReasonPrefix + "  run it in the sandbox instead \n"
	messenger := &mockMessenger{autoConfirm: &answer}
	reg := common.NewToolRegistry()
	reg.Register(&tool.ShellTool{})

	executed := false
	runner := TUIConfirmMiddleware(messenger, reg)(func(context.Context, client.ToolCall) (string, error) {
		executed = true
		return "ran", nil
	})
	result, err := runner(context.Background(), toolCall("bash", map[string]string{"command": "wget https://example.com"}))
	if err != nil {
		t.Fatal(err)
	}
	if executed {
		t.Fatal("a denied tool must not run")
	}
	if want := "Tool execution cancelled by user. The user said: run it in the sandbox instead"; result != want {
		t.Fatalf("result = %q, want %q", result, want)
	}
}

func TestConfirmMiddlewareEmptyReasonIsAPlainDenial(t *testing.T) {
	answer := denyReasonPrefix + "   "
	messenger := &mockMessenger{autoConfirm: &answer}
	reg := common.NewToolRegistry()
	reg.Register(&tool.ShellTool{})
	runner := TUIConfirmMiddleware(messenger, reg)(func(context.Context, client.ToolCall) (string, error) {
		return "ran", nil
	})
	result, _ := runner(context.Background(), toolCall("bash", map[string]string{"command": "wget https://example.com"}))
	if result != "Tool execution cancelled by user" {
		t.Fatalf("result = %q", result)
	}
}

// pendingApprovalModel returns a model whose focused agent is blocked on an
// approval for call, plus the channel the answer will arrive on.
func pendingApprovalModel(t *testing.T, call client.ToolCall) (*Model, *AppState, chan string) {
	t.Helper()
	m, _ := newViewportBenchmarkModel(nil)
	m.SetSize(100, 40)
	ch := make(chan string, 1)
	updated, _ := m.Update(ConfirmRequestMsg{OrchestratorID: m.Focused.ID(), ToolCall: call, ResultCh: ch})
	*m = updated.(Model)
	s := m.GetAgentState(m.Focused.ID())
	if s.State != StateConfirmTool || s.PendingConfirm == nil {
		t.Fatalf("expected a pending confirmation, state = %v", s.State)
	}
	return m, s, ch
}

func TestConfirmRequestBuildsPreviewAndTranscriptShowsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	_ = os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644)
	m, s, _ := pendingApprovalModel(t, toolCall("write_file", map[string]string{"path": path, "content": "alpha\ngamma\n"}))

	if s.PendingConfirm.preview == nil {
		t.Fatal("the preview should be built when the request arrives")
	}
	view := ansi.Strip(testTranscriptContent(m))
	for _, want := range []string{"wants to write", "notes.txt", "+1 -1", "beta", "gamma", "allow once"} {
		if !strings.Contains(view, want) {
			t.Errorf("transcript missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, `\n`) {
		t.Errorf("raw escaped JSON leaked into the prompt:\n%s", view)
	}
}

func TestTypingAReplyWhileApprovalPendingDeniesWithThatReason(t *testing.T) {
	m, s, ch := pendingApprovalModel(t, toolCall("bash", map[string]string{"command": "rm -rf build"}))
	m.Input.SetValue("don't delete build, use make clean")

	updated, _ := m.Update(mockKey{code: '\r', text: "enter"})
	*m = updated.(Model)

	select {
	case got := <-ch:
		if got != denyReasonPrefix+"don't delete build, use make clean" {
			t.Fatalf("answer = %q", got)
		}
	default:
		t.Fatal("the pending approval was never answered")
	}
	if s.PendingConfirm != nil || s.State != StateThinking {
		t.Errorf("approval state not cleared: pending=%v state=%v", s.PendingConfirm != nil, s.State)
	}
	if m.Input.Value() != "" {
		t.Errorf("input not cleared: %q", m.Input.Value())
	}
	if orch := m.Focused.(*benchmarkOrchestrator); orch.submitCount != 0 {
		t.Errorf("the reply was also queued as a new user message (%d submits)", orch.submitCount)
	}
	if n := len(m.InputHistory); n == 0 || m.InputHistory[n-1] != "don't delete build, use make clean" {
		t.Errorf("reply missing from input history: %v", m.InputHistory)
	}
}

func TestApprovalKeysStillAnswerWhenInputEmpty(t *testing.T) {
	for _, key := range []string{"y", "n", "s", "p", "g"} {
		m, s, ch := pendingApprovalModel(t, toolCall("bash", map[string]string{"command": "make"}))
		updated, _ := m.Update(mockKey{code: rune(key[0]), text: key})
		*m = updated.(Model)
		select {
		case got := <-ch:
			if got != key {
				t.Errorf("key %q sent %q", key, got)
			}
		default:
			t.Errorf("key %q did not answer the approval", key)
		}
		if s.PendingConfirm != nil {
			t.Errorf("key %q left the approval pending", key)
		}
	}
}

func TestStatusBarHintsAtTypedReply(t *testing.T) {
	m, _, _ := pendingApprovalModel(t, toolCall("bash", map[string]string{"command": "make"}))
	m.SetSize(160, 40)
	if bar := ansi.Strip(m.statusBarView()); !strings.Contains(bar, "authorize execution (y/s/p/g/n)") || !strings.Contains(bar, "type a reply") {
		t.Fatalf("status bar = %q", bar)
	}
}
