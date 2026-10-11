package tool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"late/internal/tool/ast"
)

// withTempGlobalConfigDir points the OS config dir at a fresh temp dir so
// tests of the GLOBAL allow-list store never touch the real user store.
// os.UserConfigDir derives the dir from $HOME on darwin and from
// $XDG_CONFIG_HOME (then $HOME/.config) on unix-like systems.
func withTempGlobalConfigDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("global config dir redirection needs $HOME/$XDG_CONFIG_HOME")
	}
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))
	return tmp
}

// isolatedStoreEnv combines a temp process cwd (local .late store) with a
// temp config dir (global store) and a pinned clock/session state, so every
// store test starts from a pristine, deterministic approval state. It returns
// the temp root and the pinned base time.
func isolatedStoreEnv(t *testing.T) (string, time.Time) {
	t.Helper()
	tmp := withTempGlobalConfigDir(t)
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("failed to chdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	base := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
	resetApprovalState(base)
	t.Cleanup(func() { nowFunc = time.Now })
	ResetOTPRegistry()
	t.Cleanup(ResetOTPRegistry)
	return tmp, base
}

func globalCommandsFile(t *testing.T) persistedCommandsFile {
	t.Helper()
	file, err := loadPersistedCommandsFile(getGlobalConfigPath(commandsFileName))
	if err != nil {
		t.Fatalf("failed to load global commands file: %v", err)
	}
	return file
}

// mustGetwd returns the process working directory (the isolated temp root in
// store tests) or fails the test.
func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	return wd
}

// isolatedTempRoot is the process working directory installed by
// isolatedStoreEnv: the temp root under which cwd-scoping tests create their
// per-directory fixtures.
func isolatedTempRoot(t *testing.T) string {
	return mustGetwd(t)
}

func TestSaveExactAllowedCommand_WritesGlobalEntry(t *testing.T) {
	isolatedStoreEnv(t)
	command := "rm -rf /tmp/build"
	cwd := "" // process working directory (the isolated temp dir)

	if err := SaveExactAllowedCommand(cwd, command, true); err != nil {
		t.Fatalf("SaveExactAllowedCommand failed: %v", err)
	}

	key := ExactCommandKey(cwd, command)
	file := globalCommandsFile(t)
	entry, ok := file.Entries[key]
	if !ok {
		t.Fatalf("expected exact entry %q in global store, got entries %v", key, file.Entries)
	}
	if len(entry.Flags) != 1 || entry.Flags[0] != exactCommandFlagMarker {
		t.Errorf("expected the exact entry to carry the single %q flag marker, got %v", exactCommandFlagMarker, entry.Flags)
	}
	if entry.TimesApproved != 1 {
		t.Errorf("expected times_approved 1 on first approval, got %d", entry.TimesApproved)
	}
	if entry.ExpiresAt == "" {
		t.Errorf("expected a non-empty expires_at on the exact entry")
	}
	if entry.Cwd != canonicalExecCwd(cwd) {
		t.Errorf("expected the entry to record the scoping cwd visibly, got cwd %q, want %q", entry.Cwd, canonicalExecCwd(cwd))
	}

	// The merged allow-list (what every new late instance reloads) must see
	// the exact entry under its reserved key.
	allowed, err := LoadAllAllowedCommands()
	if err != nil {
		t.Fatalf("LoadAllAllowedCommands failed: %v", err)
	}
	if !allowed[key][exactCommandFlagMarker] {
		t.Errorf("expected merged allow-list to contain the exact entry with the marker flag")
	}
	if CountExactCommandApprovals(cwd, command) != 1 {
		t.Errorf("CountExactCommandApprovals = %d, want 1", CountExactCommandApprovals(cwd, command))
	}
}

func TestSaveExactAllowedCommand_TrimsBoundaryWhitespaceOnly(t *testing.T) {
	isolatedStoreEnv(t)

	if err := SaveExactAllowedCommand("", "  rm -rf /tmp/build\t", true); err != nil {
		t.Fatalf("SaveExactAllowedCommand failed: %v", err)
	}
	file := globalCommandsFile(t)
	if _, ok := file.Entries[ExactCommandKey("", "rm -rf /tmp/build")]; !ok {
		t.Errorf("expected the boundary-whitespace-trimmed key to be stored, got entries %v", file.Entries)
	}

	// Interior whitespace is significant: a double space is a DIFFERENT
	// command string and must get its own entry (same exact string only).
	if err := SaveExactAllowedCommand("", "rm  -rf /tmp/build", true); err != nil {
		t.Fatalf("SaveExactAllowedCommand(double space) failed: %v", err)
	}
	file = globalCommandsFile(t)
	if _, ok := file.Entries[ExactCommandKey("", "rm  -rf /tmp/build")]; !ok {
		t.Errorf("expected the interior-whitespace variant to be a separate entry, got entries %v", file.Entries)
	}
	if len(file.Entries) != 2 {
		t.Errorf("expected exactly two exact entries, got %v", file.Entries)
	}
}

func TestSaveExactAllowedCommand_IncrementsCountAndRefreshesTTL(t *testing.T) {
	_, base := isolatedStoreEnv(t)
	command := "rm -rf /tmp/build"
	cwd := ""

	if err := SaveExactAllowedCommand(cwd, command, true); err != nil {
		t.Fatalf("first SaveExactAllowedCommand failed: %v", err)
	}
	first := globalCommandsFile(t).Entries[ExactCommandKey(cwd, command)]

	// Advance the fake clock and approve again: the counter must increment
	// and the TTL must be refreshed (an explicit approval always renews).
	nowFunc = func() time.Time { return base.Add(time.Hour) }
	if err := SaveExactAllowedCommand(cwd, command, true); err != nil {
		t.Fatalf("second SaveExactAllowedCommand failed: %v", err)
	}
	second := globalCommandsFile(t).Entries[ExactCommandKey(cwd, command)]

	if second.TimesApproved != 2 {
		t.Errorf("expected times_approved 2 after two approvals, got %d", second.TimesApproved)
	}
	firstExpiry, ok1 := parseRFC3339OrZero(first.ExpiresAt)
	secondExpiry, ok2 := parseRFC3339OrZero(second.ExpiresAt)
	if !ok1 || !ok2 {
		t.Fatalf("failed to parse expires_at values: %q / %q", first.ExpiresAt, second.ExpiresAt)
	}
	if !secondExpiry.After(firstExpiry) {
		t.Errorf("expected the TTL to be refreshed on re-approval, got %s then %s", first.ExpiresAt, second.ExpiresAt)
	}
	if CountExactCommandApprovals("", command) != 2 {
		t.Errorf("CountExactCommandApprovals = %d, want 2", CountExactCommandApprovals("", command))
	}
}

func TestSaveExactAllowedCommand_LocalScopeWritesLocalFile(t *testing.T) {
	isolatedStoreEnv(t)

	if err := SaveExactAllowedCommand("", "rm -rf /tmp/build", false); err != nil {
		t.Fatalf("SaveExactAllowedCommand(local) failed: %v", err)
	}

	data, err := os.ReadFile(localAllowedCommandsFile)
	if err != nil {
		t.Fatalf("expected a local allow-list file to be written: %v", err)
	}
	var file persistedCommandsFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("failed to unmarshal local store: %v", err)
	}
	entry, ok := file.Entries[ExactCommandKey("", "rm -rf /tmp/build")]
	if !ok {
		t.Fatalf("expected exact entry in LOCAL store, got entries %v", file.Entries)
	}
	if entry.TimesApproved != 1 {
		t.Errorf("expected times_approved 1, got %d", entry.TimesApproved)
	}
}

func TestExactAllowlist_FreshInstanceBypassesGate_ButOnlyExactString(t *testing.T) {
	tmp, _ := isolatedStoreEnv(t)
	command := "rm -rf " + filepath.Join(tmp, "build")
	cwd := filepath.Join(tmp, "project")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("failed to create scoping cwd: %v", err)
	}
	otherCwd := filepath.Join(tmp, "elsewhere")
	if err := os.MkdirAll(otherCwd, 0o755); err != nil {
		t.Fatalf("failed to create other scoping cwd: %v", err)
	}
	args := func(command, cwd string) json.RawMessage {
		raw, err := json.Marshal(struct {
			Command string `json:"command"`
			Cwd     string `json:"cwd"`
		}{Command: command, Cwd: cwd})
		if err != nil {
			t.Fatalf("failed to marshal args: %v", err)
		}
		return raw
	}

	// Fresh tool, no approval yet: the dangerous command must gate.
	freshBefore := &ShellTool{}
	if !freshBefore.RequiresConfirmation(args(command, cwd)) {
		t.Fatalf("expected the dangerous command to require confirmation before any approval")
	}

	// Simulate the OTP approval write (what the gate does on a valid OTP):
	// scoped to the cwd the command would execute in.
	if err := SaveExactAllowedCommand(cwd, command, true); err != nil {
		t.Fatalf("SaveExactAllowedCommand failed: %v", err)
	}

	// A FRESH tool instance (what a new late process constructs after
	// reloading the policy from disk) must auto-approve the exact string in
	// the SAME cwd — with and without an equivalent trailing spelling.
	freshAfter := &ShellTool{}
	if freshAfter.RequiresConfirmation(args(command, cwd)) {
		t.Fatalf("expected the exact command to bypass the gate on a fresh instance")
	}
	if freshAfter.RequiresConfirmation(args(command, cwd+string(filepath.Separator))) {
		t.Fatalf("expected the trailing-separator cwd spelling to hit the same entry (canonicalization)")
	}

	// The same command from a DIFFERENT cwd is a different command.
	if !freshAfter.RequiresConfirmation(args(command, otherCwd)) {
		t.Fatalf("expected the same command in a different cwd to still require confirmation")
	}
	// ...and so is a call with no cwd parameter (resolves to the process
	// working directory, not the approved project dir).
	if !freshAfter.RequiresConfirmation(args(command, "")) {
		t.Fatalf("expected the same command with the default (process) cwd to still require confirmation")
	}

	// A different command is NOT covered by the exact approval.
	other := "rm -rf " + filepath.Join(tmp, "other")
	if !freshAfter.RequiresConfirmation(args(other, cwd)) {
		t.Fatalf("expected a different command to still require confirmation")
	}

	// Interior whitespace makes a different command string: still gated.
	doubleSpace := strings.Replace(command, "rm -rf", "rm  -rf", 1)
	if !freshAfter.RequiresConfirmation(args(doubleSpace, cwd)) {
		t.Fatalf("expected the interior-whitespace variant to still require confirmation")
	}
}

// TestExactAllowlist_RelativeAndAbsolutePathCommandsAreSeparateEntries pins
// the raw-argv rule: the allowlist stores the command string exactly as it
// was approved, so "rm -rf ./bin" and "rm -rf /abs/bin" are SEPARATE entries
// even in the same cwd. The cwd scoping exists so relative paths resolve the
// way execution resolves them; path spelling inside the command stays part
// of the approved identity.
func TestExactAllowlist_RelativeAndAbsolutePathCommandsAreSeparateEntries(t *testing.T) {
	isolatedStoreEnv(t)
	cwd := "" // process working directory

	relative := "rm -rf ./bin"
	absolute := "rm -rf " + filepath.Join(mustGetwd(t), "bin")

	if err := SaveExactAllowedCommand(cwd, relative, true); err != nil {
		t.Fatalf("SaveExactAllowedCommand(relative) failed: %v", err)
	}
	if got := CountExactCommandApprovals(cwd, absolute); got != 0 {
		t.Errorf("expected the absolute-path spelling to be a different (unapproved) entry, got count %d", got)
	}
	if err := SaveExactAllowedCommand(cwd, absolute, true); err != nil {
		t.Fatalf("SaveExactAllowedCommand(absolute) failed: %v", err)
	}
	if got := CountExactCommandApprovals(cwd, relative); got != 1 {
		t.Errorf("relative entry count = %d, want 1", got)
	}
	if got := CountExactCommandApprovals(cwd, absolute); got != 1 {
		t.Errorf("absolute entry count = %d, want 1 (separate entries by design)", got)
	}
}

// TestSaveExactAllowedCommand_CwdScopesEntriesSeparately proves the
// allowlist is keyed per (cwd, command): identical argv in two directories
// produces two independent entries, each recording its cwd visibly, with
// independent times_approved counters.
func TestSaveExactAllowedCommand_CwdScopesEntriesSeparately(t *testing.T) {
	_, base := isolatedStoreEnv(t)
	command := "rm -rf ./bin"
	cwdA := filepath.Join(isolatedTempRoot(t), "project-a")
	cwdB := filepath.Join(isolatedTempRoot(t), "project-b")

	if err := SaveExactAllowedCommand(cwdA, command, true); err != nil {
		t.Fatalf("SaveExactAllowedCommand(cwdA) failed: %v", err)
	}
	nowFunc = func() time.Time { return base.Add(time.Hour) }
	if err := SaveExactAllowedCommand(cwdB, command, true); err != nil {
		t.Fatalf("SaveExactAllowedCommand(cwdB) failed: %v", err)
	}

	file := globalCommandsFile(t)
	if len(file.Entries) != 2 {
		t.Fatalf("expected two separate (cwd, command) entries, got %v", file.Entries)
	}
	for _, cwd := range []string{cwdA, cwdB} {
		entry, ok := file.Entries[ExactCommandKey(cwd, command)]
		if !ok {
			t.Fatalf("expected an entry for cwd %q, got %v", cwd, file.Entries)
		}
		if entry.TimesApproved != 1 {
			t.Errorf("entry for cwd %q: times_approved = %d, want 1", cwd, entry.TimesApproved)
		}
		if entry.Cwd != canonicalExecCwd(cwd) {
			t.Errorf("entry for cwd %q: visible cwd = %q, want %q", cwd, entry.Cwd, canonicalExecCwd(cwd))
		}
		if got := CountExactCommandApprovals(cwd, command); got != 1 {
			t.Errorf("CountExactCommandApprovals(%q) = %d, want 1", cwd, got)
		}
	}

	// A third cwd was never approved.
	if got := CountExactCommandApprovals(filepath.Join(isolatedTempRoot(t), "project-c"), command); got != 0 {
		t.Errorf("expected no approval for an unapproved cwd, got %d", got)
	}
}

// TestExactCommandKey_CwdComponentAndSeparator pins the key format:
// prefix + canonical cwd + NUL + command, with a working split helper and
// stable canonicalization across equivalent cwd spellings.
func TestExactCommandKey_CwdComponentAndSeparator(t *testing.T) {
	isolatedStoreEnv(t)
	root := isolatedTempRoot(t)

	key := ExactCommandKey(filepath.Join(root, "proj"), "rm -rf ./bin")
	if !strings.HasPrefix(key, exactCommandKeyPrefix) {
		t.Fatalf("expected the reserved prefix, got %q", key)
	}
	cwd, command, ok := splitExactCommandKey(key)
	if !ok {
		t.Fatalf("expected the key to split into cwd and command, got %q", key)
	}
	if command != "rm -rf ./bin" {
		t.Errorf("split command = %q, want the raw argv", command)
	}
	if cwd != canonicalExecCwd(filepath.Join(root, "proj")) {
		t.Errorf("split cwd = %q, want the canonical cwd %q", cwd, canonicalExecCwd(filepath.Join(root, "proj")))
	}

	// Different cwd → different key: the scoping is load-bearing.
	if ExactCommandKey(filepath.Join(root, "a"), "rm -rf ./bin") == ExactCommandKey(filepath.Join(root, "b"), "rm -rf ./bin") {
		t.Fatalf("expected different cwds to produce different keys")
	}

	// Equivalent spellings of one directory share a key: trailing
	// separator, interior dot segments, and relative-vs-absolute.
	sameAbs := filepath.Join(root, "proj")
	spellings := []string{
		sameAbs,
		sameAbs + string(filepath.Separator),
		filepath.Join(root, "nested", "..", "proj"),
	}
	// A relative spelling resolves against the process working directory
	// (os/exec semantics for a relative cmd.Dir).
	if rel, err := filepath.Rel(mustGetwd(t), sameAbs); err == nil && !strings.HasPrefix(rel, "..") {
		spellings = append(spellings, rel)
	}
	want := ExactCommandKey(sameAbs, "rm -rf ./bin")
	for _, spelling := range spellings {
		if got := ExactCommandKey(spelling, "rm -rf ./bin"); got != want {
			t.Errorf("cwd spelling %q produced key %q, want the canonical %q", spelling, got, want)
		}
	}

	// "" means the process working directory.
	if ExactCommandKey("", "x") != ExactCommandKey(mustGetwd(t), "x") {
		t.Errorf("expected an empty cwd to canonicalize to the process working directory")
	}
}

// TestOTPBinding_IsCwdScoped pins the OTP registry scoping: a pending code
// issued for a command in one cwd is worthless for the same command in
// another cwd — only a fresh OTP for that cwd validates.
func TestOTPBinding_IsCwdScoped(t *testing.T) {
	isolatedStoreEnv(t)
	root := isolatedTempRoot(t)
	command := "rm -rf ./bin"
	cwdA := filepath.Join(root, "project-a")
	cwdB := filepath.Join(root, "project-b")

	codeA := IssueOTP(cwdA, command)

	// Same command, different cwd: the code does not validate, and the
	// pending entry for cwdA is untouched.
	if ConsumeOTP(cwdB, command, codeA) {
		t.Fatalf("expected the code issued for cwdA to be rejected for cwdB")
	}
	if ConsumeOTP(cwdA, command+" ", codeA) {
		t.Fatalf("expected the code to be bound to the exact argv too")
	}

	// cwdB gets its own, different pending code.
	codeB := IssueOTP(cwdB, command)
	if codeB == codeA {
		t.Fatalf("expected a fresh code for a different cwd, got the same one")
	}
	if !ConsumeOTP(cwdA, command, codeA) {
		t.Fatalf("expected the original code to still validate for its own (cwd, command)")
	}
	if !ConsumeOTP(cwdB, command, codeB) {
		t.Fatalf("expected the fresh code to validate for cwdB")
	}

	// Session counters are per (cwd, command) as well.
	if got := RecordOTPApproval(cwdA, command); got != 1 {
		t.Errorf("first RecordOTPApproval(cwdA) = %d, want 1", got)
	}
	if got := RecordOTPApproval(cwdB, command); got != 1 {
		t.Errorf("first RecordOTPApproval(cwdB) = %d, want 1 (no leakage across cwds)", got)
	}
	if got := SessionOTPApprovals(cwdA, command); got != 1 {
		t.Errorf("SessionOTPApprovals(cwdA) = %d, want 1", got)
	}
}

func TestAnalyze_ExactApprovalNeverBypassesHardBlocks(t *testing.T) {
	isolatedStoreEnv(t)

	// The exact-allowlisted redirect must STILL hard-block: the exact-string
	// bypass never lifts hard refusals. The fixtures are keyed the way
	// SaveExactAllowedCommand stores them — scoped to the analyzer's cwd
	// (here the process working directory).
	redirectAnalyzer := newASTAnalyzer(ast.PlatformUnix, "", map[string]map[string]bool{
		ExactCommandKey("", "echo hi > /tmp/out"): {exactCommandFlagMarker: true},
	})
	analysis := redirectAnalyzer.Analyze("echo hi > /tmp/out")
	if !analysis.IsBlocked {
		t.Fatalf("expected the redirect hard block to survive the exact approval")
	}
	if !analysis.NeedsConfirmation {
		t.Errorf("expected the hard block to keep NeedsConfirmation for the normal refusal flow")
	}

	// An unparseable command with no hard-block signature fails closed
	// without an exact approval...
	baseline := newASTAnalyzer(ast.PlatformUnix, "", map[string]map[string]bool{})
	if got := baseline.Analyze("echo 'unterminated"); got.IsBlocked || !got.NeedsConfirmation {
		t.Fatalf("expected unparseable input to fail closed without an exact approval: %+v", got)
	}
	// ...but an exact approval covers it: the same string already executed
	// once with a valid OTP, so the gate must not re-prompt.
	approvedAnalyzer := newASTAnalyzer(ast.PlatformUnix, "", map[string]map[string]bool{
		ExactCommandKey("", "echo 'unterminated"): {exactCommandFlagMarker: true},
	})
	if got := approvedAnalyzer.Analyze("echo 'unterminated"); got.IsBlocked || got.NeedsConfirmation {
		t.Errorf("expected the exact-approved unparseable command to bypass the soft gate, got %+v", got)
	}

	// The cwd participates in the bypass: the same unparseable string from a
	// DIFFERENT cwd is a different command and stays gated.
	otherCwdAnalyzer := newASTAnalyzer(ast.PlatformUnix, filepath.Join(mustGetwd(t), "elsewhere"), map[string]map[string]bool{
		ExactCommandKey("", "echo 'unterminated"): {exactCommandFlagMarker: true},
	})
	if got := otherCwdAnalyzer.Analyze("echo 'unterminated"); got.IsBlocked || !got.NeedsConfirmation {
		t.Errorf("expected the same string in a different cwd to stay gated, got %+v", got)
	}
}

func TestRecordOTPApproval_CountsAndConversationResetClears(t *testing.T) {
	isolatedStoreEnv(t)
	cwd := ""

	if got := RecordOTPApproval(cwd, "rm -rf /tmp/build"); got != 1 {
		t.Errorf("first RecordOTPApproval = %d, want 1", got)
	}
	if got := RecordOTPApproval(cwd, "rm -rf /tmp/build"); got != 2 {
		t.Errorf("second RecordOTPApproval = %d, want 2", got)
	}
	if got := SessionOTPApprovals(cwd, "rm -rf /tmp/other"); got != 0 {
		t.Errorf("SessionOTPApprovals for a different command = %d, want 0", got)
	}

	ResetOTPRegistry()
	if got := SessionOTPApprovals(cwd, "rm -rf /tmp/build"); got != 0 {
		t.Errorf("expected session approval counters to reset with the OTP registry, got %d", got)
	}
}

func TestOTPRevaluateMessageDetailed_CountersAndAutoAllowNote(t *testing.T) {
	cwd := "/tmp/project"
	msg := OTPRevaluateMessageDetailed(cwd, "1234567", 3, 2)
	if !strings.Contains(msg, "1234567") {
		t.Errorf("expected the message to contain the OTP code")
	}
	if !strings.Contains(msg, "approved and allowlisted globally 3 time(s) before") {
		t.Errorf("expected the message to report 3 prior global approvals, got %q", msg)
	}
	if !strings.Contains(msg, "approved 2 time(s) earlier in this session") {
		t.Errorf("expected the message to report 2 session approvals, got %q", msg)
	}
	if !strings.Contains(msg, "auto-allowed after this approval (global") {
		t.Errorf("expected the message to note the global auto-allow, got %q", msg)
	}
	// The cwd scoping is visible: the agent/human sees which directory the
	// OTP (and the resulting approval) is bound to.
	if !strings.Contains(msg, "working directory "+canonicalExecCwd(cwd)) {
		t.Errorf("expected the message to show the scoping cwd %q, got %q", canonicalExecCwd(cwd), msg)
	}
	if !strings.Contains(msg, "different working directory is a different command") {
		t.Errorf("expected the message to explain the cwd scoping, got %q", msg)
	}
	if !strings.Contains(msg, "same exact string in the same working directory only") {
		t.Errorf("expected the auto-allow note to carry the cwd scoping, got %q", msg)
	}

	firstTime := OTPRevaluateMessageDetailed(cwd, "7654321", 0, 0)
	if !strings.Contains(firstTime, "never been approved and allowlisted before") {
		t.Errorf("expected the zero-count message to say the command was never approved, got %q", firstTime)
	}
	if strings.Contains(firstTime, "earlier in this session") {
		t.Errorf("expected no session clause when the session count is zero, got %q", firstTime)
	}

	// The legacy renderer stays stable for callers/tests that only need the
	// base re-evaluate text.
	legacy := OTPRevaluateMessage("0001111")
	if !strings.Contains(legacy, "0001111") || !strings.Contains(legacy, "re-evaluate") {
		t.Errorf("legacy message unexpectedly changed: %q", legacy)
	}
}
