package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"late/internal/client"
	"late/internal/session"
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
	// head kept, pointer line present.
	ref1 := session.FormatReference(p1, output, 100)
	ref2 := session.FormatReference(p1, output, 100)
	if ref1 != ref2 {
		t.Error("FormatReference must be deterministic (identical inputs → identical string)")
	}
	if got, want := ref1, strings.Repeat("late-archive\n", 13)[:100]+"\n…[full output archived: "+p1+"]"; got != want {
		t.Errorf("FormatReference = %q, want %q", got, want)
	}
}

// TestOutputArchiveRuneSafeHead checks the rune-safe head cut: a multi-byte
// string must not be split mid-rune.
func TestOutputArchiveRuneSafeHead(t *testing.T) {
	output := strings.Repeat("é", 300) // 600 bytes, 300 runes
	ref := session.FormatReference("/tmp/x", output, 100)
	if !strings.HasPrefix(ref, strings.Repeat("é", 100)) {
		t.Errorf("reference head is not the first 100 runes: %q", ref[:40])
	}
	// Invalid UTF-8 at the cut boundary must never produce a torn rune.
	torn := strings.Repeat("a", 99) + "\xff\xff" + strings.Repeat("b", 50)
	refTorn := session.FormatReference("/tmp/y", torn, 100)
	if strings.Contains(refTorn, "\xffb") {
		t.Error("head cut split a byte sequence across the boundary")
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
// and the size threshold (archive strictly above it).
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
	// Above the threshold → reference form. For an output barely over the
	// threshold the head (2000 chars) keeps the whole text, so the form is
	// not necessarily shorter — assert the exact reference bytes instead.
	got := maybeArchiveToolResult("large_dump", big)
	if want := session.FormatReference(mustArchive(t, a, big), big, ArchiveHeadChars); got != want {
		t.Errorf("oversized result = %q, want the exact reference form", got[:min(120, len(got))])
	}
	// An output far over the threshold shrinks: head capped at 2000 chars
	// plus the pointer line.
	huge := strings.Repeat("y", 4*ArchiveHeadChars)
	got = maybeArchiveToolResult("large_dump", huge)
	if want := session.FormatReference(mustArchive(t, a, huge), huge, ArchiveHeadChars); got != want {
		t.Errorf("huge result = %q, want the exact reference form", got[:min(120, len(got))])
	}
	if len(got) >= len(huge) {
		t.Errorf("reference form (%d chars) must be smaller than the huge output (%d chars)", len(got), len(huge))
	}
	if !strings.Contains(got, "[full output archived: ") {
		t.Errorf("reference form missing the archive pointer: %q", got[:min(120, len(got))])
	}
}

// TestExecuteToolCallsArchivesLargeOutputs is the integration check: with
// the archiver installed, a >1024-char tool result enters history as the
// reference form naming the archive file, the archive file holds the full
// original, and a small result stays inline.
func TestExecuteToolCallsArchivesLargeOutputs(t *testing.T) {
	output := strings.Repeat("stdout line\n", 150) // ~1800 chars
	sess := newArchiveSession(t, output)
	dir := filepath.Join(t.TempDir(), "tool-outputs")
	a, err := session.NewOutputArchive(dir)
	if err != nil {
		t.Fatalf("NewOutputArchive: %v", err)
	}
	SetToolResultArchiver(a)
	t.Cleanup(func() { SetToolResultArchiver(nil) })

	got := runArchivedTool(t, sess, "call_big")
	if !strings.Contains(got, "[full output archived: "+dir+string(os.PathSeparator)) {
		t.Fatalf("history tool result does not reference the archive path:\n%s", got[:min(200, len(got))])
	}
	if strings.Contains(got, "stdout line") && len(got) > ArchiveHeadChars+200 {
		t.Errorf("history tool result still carries the full output (%d chars)", len(got))
	}

	// The referenced file exists and holds the original, byte for byte.
	idx := strings.Index(got, "[full output archived: ")
	rest := got[idx+len("[full output archived: "):]
	path := rest[:strings.Index(rest, "]")]
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("archived file %s missing: %v", path, err)
	}
	if string(data) != output {
		t.Error("archived content differs from the tool output")
	}

	// A small result stays inline, unchanged.
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
