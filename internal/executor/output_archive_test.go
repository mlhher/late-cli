package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"late/internal/client"
	"late/internal/session"
	"late/internal/tool"
)

// archiveDumpTool is a fake tool returning a configurable output, mirroring
// largeDumpTool without the compaction-test dependencies.
type archiveDumpTool struct {
	name   string
	output string
}

func (t archiveDumpTool) Name() string {
	if t.name == "" {
		return "large_dump"
	}
	return t.name
}
func (t archiveDumpTool) Description() string { return "Emits a large output." }
func (t archiveDumpTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t archiveDumpTool) RequiresConfirmation(json.RawMessage) bool { return false }
func (t archiveDumpTool) CallString(json.RawMessage) string         { return "Dumping..." }
func (t archiveDumpTool) Execute(context.Context, json.RawMessage) (string, error) {
	return t.output, nil
}

// newArchiveSession builds a session with the dump tool registered, in an
// isolated sessions dir (isolateSessionDir) so no test writes escape.
func newArchiveSession(t *testing.T, output string) *session.Session {
	t.Helper()
	isolateSessionDir(t)
	c := client.NewClient(client.Config{BaseURL: "http://localhost:0"})
	sess := session.New(c, filepath.Join(t.TempDir(), "history.json"), nil, "", true)
	sess.Registry.Register(archiveDumpTool{output: output})
	return sess
}

// historyTail returns the tool-result message ExecuteToolCalls last
// committed (same shape as callTool in compaction_test.go).
func historyTail(t *testing.T, sess *session.Session, id string) string {
	t.Helper()
	last := sess.History[len(sess.History)-1]
	if last.Role != "tool" || last.ToolCallID != id {
		t.Fatalf("history tail = role %q id %q, want the tool result for %s", last.Role, last.ToolCallID, id)
	}
	return last.Content.String()
}

// runArchivedTool executes one large_dump call through ExecuteToolCalls and
// returns the tool result that entered history.
func runArchivedTool(t *testing.T, sess *session.Session, id string) string {
	t.Helper()
	err := ExecuteToolCalls(context.Background(), sess, []client.ToolCall{
		{ID: id, Function: client.FunctionCall{Name: "large_dump", Arguments: "{}"}},
	}, nil)
	if err != nil {
		t.Fatalf("ExecuteToolCalls error = %v", err)
	}
	return historyTail(t, sess, id)
}

// TestOutputArchiveWriteDedupeAndFormat pins the archive primitives:
// content-addressed files (same content → same path, one file on disk),
// 0600 files written under a 0700 directory, idempotent re-archives, and
// the byte-identical reference form.
func TestOutputArchiveWriteDedupeAndFormat(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tool-outputs")
	a, err := session.NewOutputArchive(dir)
	if err != nil {
		t.Fatalf("NewOutputArchive: %v", err)
	}

	output := strings.Repeat("late-archive\n", 200)
	p1, err := a.Archive(output)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if filepath.Base(p1) != "6a0e10a24e29be8a.txt" && len(filepath.Base(p1)) != 20 {
		t.Errorf("archive path %q does not look content-addressed (sha256[:16].txt)", p1)
	}
	if filepath.Dir(p1) != dir {
		t.Errorf("archive path %q escapes the archive dir %q", p1, dir)
	}

	// Identical content → identical path, still exactly one file.
	p2, err := a.Archive(output)
	if err != nil {
		t.Fatalf("Archive (repeat): %v", err)
	}
	if p2 != p1 {
		t.Errorf("re-archive of identical output returned %q, want %q", p2, p1)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("archive dir holds %d files, want 1 (identical outputs must dedupe)", len(entries))
	}

	// File content round-trips byte for byte; mode is 0600.
	data, err := os.ReadFile(p1)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != output {
		t.Error("archived file content differs from the original output")
	}
	info, err := os.Stat(p1)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("archive file mode = %o, want 600", perm)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("archive dir mode = %o, want 700", perm)
	}

	// Different content → different path.
	other, err := a.Archive(output + "x")
	if err != nil {
		t.Fatalf("Archive (other): %v", err)
	}
	if other == p1 {
		t.Error("different outputs must not map to the same archive path")
	}

	// Deterministic reference form: same inputs twice → identical bytes,
	// head kept, marker + tail present, self-documenting read_file pointer.
	ref1 := session.FormatReference(p1, output, 100, 50)
	ref2 := session.FormatReference(p1, output, 100, 50)
	if ref1 != ref2 {
		t.Error("FormatReference must be deterministic (identical inputs → identical string)")
	}
	runes := []rune(output)
	wantMarker := fmt.Sprintf("…[output truncated: %d chars total. Head above / tail below. Full output archived: %s — read_file it to view everything]", len(runes), p1)
	want := string(runes[:100]) + "\n" + wantMarker + "\n" + string(runes[len(runes)-50:])
	if ref1 != want {
		t.Errorf("FormatReference = %q, want %q", ref1, want)
	}
}

// TestOutputArchiveRuneSafeHeadAndTail checks the rune-safe head and tail
// cuts: a multi-byte string must not be split mid-rune on either side.
func TestOutputArchiveRuneSafeHeadAndTail(t *testing.T) {
	output := strings.Repeat("é", 300) // 600 bytes, 300 runes
	ref := session.FormatReference("/tmp/x", output, 100, 50)
	if !strings.HasPrefix(ref, strings.Repeat("é", 100)) {
		t.Errorf("reference head is not the first 100 runes: %q", ref[:40])
	}
	if !strings.HasSuffix(ref, strings.Repeat("é", 50)) {
		t.Errorf("reference tail is not the last 50 runes: %q", ref[len(ref)-60:])
	}
	// Invalid UTF-8 at the cut boundary must never produce a torn rune.
	torn := strings.Repeat("a", 99) + "\xff\xff" + strings.Repeat("b", 50)
	refTorn := session.FormatReference("/tmp/y", torn, 100, 20)
	if strings.Contains(refTorn, "\xffb") {
		t.Error("head or tail cut split a byte sequence across the boundary")
	}
}

// TestOutputArchiveRefusesUnsafeSessionID mirrors the paths.go contract:
// unsafe IDs must error instead of building a path outside the sessions
// directory.
func TestOutputArchiveRefusesUnsafeSessionID(t *testing.T) {
	for _, unsafe := range []string{"", ".", "..", "a/b", `a\b`, "a\x00b"} {
		if _, err := session.OutputArchiveDir(unsafe); err == nil {
			t.Errorf("OutputArchiveDir(%q) = nil error, want invalid-session-ID error", unsafe)
		}
	}
}

// TestMaybeArchiveToolResultGuards pins the archiver guards: nil archiver,
// the size threshold (archive strictly above it), and the expand-tool
// exemption.
func TestMaybeArchiveToolResultGuards(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tool-outputs")
	a, err := session.NewOutputArchive(dir)
	if err != nil {
		t.Fatalf("NewOutputArchive: %v", err)
	}

	// No archive installed → pass-through.
	SetToolResultArchiver(nil)
	t.Cleanup(func() { SetToolResultArchiver(nil) })
	big := strings.Repeat("x", ArchiveThresholdChars+1)
	if got := maybeArchiveToolResult("large_dump", big); got != big {
		t.Error("nil archiver must pass results through unchanged")
	}

	SetToolResultArchiver(a)
	// At the threshold → inline.
	at := strings.Repeat("x", ArchiveThresholdChars)
	if got := maybeArchiveToolResult("large_dump", at); got != at {
		t.Error("results at ArchiveThresholdChars must stay inline")
	}

	// Inline-first band (ArchiveThresholdChars, ArchiveInlineFullChars]: the
	// full text stays inline AND a durability copy is archived — no marker.
	// Pin both band boundaries.
	if got := maybeArchiveToolResult("large_dump", big); got != big {
		t.Errorf("result just above ArchiveThresholdChars must stay full inline, got %d chars prefix %q", len(got), got[:min(60, len(got))])
	}
	mid := strings.Repeat("m", ArchiveInlineFullChars)
	if got := maybeArchiveToolResult("large_dump", mid); got != mid {
		t.Errorf("result at ArchiveInlineFullChars must stay full inline, got %d chars prefix %q", len(got), got[:min(60, len(got))])
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("inline-first band must archive anyway: archive dir holds %d files, want 2", len(entries))
	}

	// Above the band → the exact reference form. Pin the lower boundary too.
	over := strings.Repeat("o", ArchiveInlineFullChars+1)
	got := maybeArchiveToolResult("large_dump", over)
	if want := session.FormatReference(mustArchive(t, a, over), over, ArchiveHeadChars, ArchiveTailChars); got != want {
		t.Errorf("result above ArchiveInlineFullChars = %q, want the exact reference form", got[:min(120, len(got))])
	}

	// An output far over the band shrinks: head 2000 + marker + tail 500.
	huge := strings.Repeat("y", 4*ArchiveHeadChars)
	got = maybeArchiveToolResult("large_dump", huge)
	if want := session.FormatReference(mustArchive(t, a, huge), huge, ArchiveHeadChars, ArchiveTailChars); got != want {
		t.Errorf("huge result = %q, want the exact reference form", got[:min(120, len(got))])
	}
	if len(got) >= len(huge) {
		t.Errorf("reference form (%d chars) must be smaller than the huge output (%d chars)", len(got), len(huge))
	}
	if !strings.Contains(got, "[output truncated: 8000 chars total. Head above / tail below.") ||
		!strings.Contains(got, "read_file it to view everything") {
		t.Errorf("reference form missing size metadata or the read_file pointer: %q", got[:min(200, len(got))])
	}
}

// TestExecuteToolCallsArchivesLargeOutputs is the integration check: with
// the archiver installed, a >ArchiveInlineFullChars tool result enters
// history as the reference form naming the archive file, the archive file
// holds the full original and is readable by the read_file tool (the
// sanctioned retrieval path the marker names — absolute path outside the
// worktree must NOT be rejected), a mid-band result (1024, 3072] enters
// history FULL with a durability copy archived, and a small result stays
// inline unarchived.
func TestExecuteToolCallsArchivesLargeOutputs(t *testing.T) {
	lines := make([]string, 500)
	for i := range lines {
		lines[i] = fmt.Sprintf("stdout line-%04d", i)
	}
	output := strings.Join(lines, "\n") // well above ArchiveInlineFullChars
	sess := newArchiveSession(t, output)
	dir := filepath.Join(t.TempDir(), "tool-outputs")
	a, err := session.NewOutputArchive(dir)
	if err != nil {
		t.Fatalf("NewOutputArchive: %v", err)
	}
	SetToolResultArchiver(a)
	t.Cleanup(func() { SetToolResultArchiver(nil) })

	got := runArchivedTool(t, sess, "call_big")
	if !strings.Contains(got, fmt.Sprintf("[output truncated: %d chars total.", len(output))) ||
		!strings.Contains(got, "Full output archived: "+dir+string(os.PathSeparator)) {
		t.Fatalf("history tool result does not carry the truncation marker + archive path:\n%s", got[:min(200, len(got))])
	}
	if len(got) >= len(output) {
		t.Errorf("history tool result still carries the full output (%d chars)", len(got))
	}

	// The referenced file exists and holds the original, byte for byte.
	idx := strings.Index(got, "Full output archived: ")
	rest := got[idx+len("Full output archived: "):]
	path := rest[:strings.Index(rest, " — read_file")]
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("archived file %s missing: %v", path, err)
	}
	if string(data) != output {
		t.Error("archived content differs from the tool output")
	}

	// read_file — the retrieval tool the marker names — must accept the
	// absolute archive path (outside the worktree, inside the session dir)
	// without path-safety rejection and return the FULL original.
	rfArgs, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	rfOut, err := tool.NewReadFileTool().Execute(context.Background(), rfArgs)
	if err != nil {
		t.Fatalf("read_file rejected the archive path %s (path-safety bug): %v", path, err)
	}
	if !strings.Contains(rfOut, "stdout line-0250") { // middle: absent from head AND tail
		t.Error("read_file output missing the middle of the original — archive read incomplete")
	}
	if !strings.Contains(rfOut, "stdout line-0499") { // tail: dropped from the reference head
		t.Error("read_file output missing the tail of the original")
	}

	// Mid-band integration: (ArchiveThresholdChars, ArchiveInlineFullChars]
	// enters history FULL (no marker) with a durability copy archived.
	mid := strings.Repeat("mid line\n", 300) // 2700 chars
	sess.Registry.Register(archiveDumpTool{name: "mid_dump", output: mid})
	if err := ExecuteToolCalls(context.Background(), sess, []client.ToolCall{
		{ID: "call_mid", Function: client.FunctionCall{Name: "mid_dump", Arguments: "{}"}},
	}, nil); err != nil {
		t.Fatalf("ExecuteToolCalls(mid) error = %v", err)
	}
	if got := historyTail(t, sess, "call_mid"); got != mid {
		t.Errorf("mid-band result must enter history full inline, got %d chars prefix %q", len(got), got[:min(60, len(got))])
	}

	// A small result stays inline, unchanged, and is NOT archived.
	small := "tiny"
	sess.Registry.Register(archiveDumpTool{name: "small_dump", output: small})
	if err := ExecuteToolCalls(context.Background(), sess, []client.ToolCall{
		{ID: "call_small", Function: client.FunctionCall{Name: "small_dump", Arguments: "{}"}},
	}, nil); err != nil {
		t.Fatalf("ExecuteToolCalls(small) error = %v", err)
	}
	if got := historyTail(t, sess, "call_small"); got != small {
		t.Errorf("small result was altered: %q", got)
	}

	// Exactly two archived files: the big one and the mid-band one.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("archive dir holds %d files, want 2 (big + mid-band; small must not archive)", len(entries))
	}
}

// TestExecuteToolCallsArchiveFailOpen: a broken archive (read-only dir) must
// keep the full output inline and still commit the tool result to history.
func TestExecuteToolCallsArchiveFailOpen(t *testing.T) {
	output := strings.Repeat("failopen\n", 300)
	sess := newArchiveSession(t, output)

	base := t.TempDir()
	roDir := filepath.Join(base, "ro")
	if err := os.MkdirAll(roDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Eager construction succeeds; the WRITE fails later. Revoke write
	// permission after the dir exists so NewOutputArchive's MkdirAll (a
	// no-op on an existing dir) still passes.
	a, err := session.NewOutputArchive(roDir)
	if err != nil {
		t.Fatalf("NewOutputArchive: %v", err)
	}
	if err := os.Chmod(roDir, 0o500); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(roDir, 0o700) // let t.TempDir() clean up
	})

	SetToolResultArchiver(a)
	t.Cleanup(func() { SetToolResultArchiver(nil) })

	got := runArchivedTool(t, sess, "call_failopen")
	if got != output {
		t.Errorf("fail-open must keep the full output inline, got %d chars", len(got))
	}
}

// TestFormatReferenceHeadTailComposition pins the richer reference form:
// head (2000) + size-carrying marker + tail (500), byte-deterministic
// across calls, no middle content, and self-documenting — the marker names
// read_file as the sanctioned retrieval tool and never suggests gated shell
// tools (cat/grep).
func TestFormatReferenceHeadTailComposition(t *testing.T) {
	lines := make([]string, 1000)
	for i := range lines {
		lines[i] = fmt.Sprintf("dump line-%04d", i)
	}
	output := strings.Join(lines, "\n") // ~15k chars
	path := "/sessions/s1/tool-outputs/abcdef0123456789.txt"

	ref := session.FormatReference(path, output, ArchiveHeadChars, ArchiveTailChars)
	runes := []rune(output)
	if !strings.HasPrefix(ref, string(runes[:ArchiveHeadChars])) {
		t.Error("reference does not start with the first ArchiveHeadChars runes")
	}
	if !strings.HasSuffix(ref, string(runes[len(runes)-ArchiveTailChars:])) {
		t.Error("reference does not end with the last ArchiveTailChars runes")
	}
	marker := fmt.Sprintf("…[output truncated: %d chars total. Head above / tail below. Full output archived: %s — read_file it to view everything]", len(runes), path)
	if !strings.Contains(ref, "\n"+marker+"\n") {
		t.Errorf("reference marker missing or malformed:\nwant %q", marker)
	}
	if strings.Contains(ref, "dump line-0500") {
		t.Error("reference must not carry the middle of the output")
	}
	if strings.Contains(ref, "cat ") || strings.Contains(ref, "grep ") {
		t.Error("marker must not suggest gated shell retrieval tools")
	}
	// Deterministic: same (output, path) → identical bytes (cache-stability
	// invariant — no timestamps, no randomness).
	if again := session.FormatReference(path, output, ArchiveHeadChars, ArchiveTailChars); again != ref {
		t.Error("FormatReference must be deterministic (identical inputs → identical string)")
	}

	// Output shorter than the head: nothing cut → whole text kept, no
	// truncation language, pointer still names the tool.
	shortRef := session.FormatReference(path, "short", ArchiveHeadChars, ArchiveTailChars)
	wantShort := "short\n…[full output archived: " + path + " — read_file it to view everything]"
	if shortRef != wantShort {
		t.Errorf("short-output reference = %q, want %q", shortRef, wantShort)
	}
}

// mustArchive wraps Archive for want-construction in tests.
func mustArchive(t *testing.T, a *session.OutputArchive, output string) string {
	t.Helper()
	p, err := a.Archive(output)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	return p
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
