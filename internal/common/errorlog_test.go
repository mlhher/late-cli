package common

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// TestErrorLog_AppendsJSONLines pins the durable critical-error log: 0600
// file, 0700 parent dirs, one JSON line per entry shaped
// {"ts":RFC3339,"component":...,"message":...}, appends across calls.
func TestErrorLog_AppendsJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "late-errors.log")
	l, err := OpenErrorLogAt(path)
	if err != nil {
		t.Fatalf("OpenErrorLogAt() error = %v", err)
	}

	l.Log("compaction", "walk aborted after 2 messages")
	l.Logf("diagnostic", "hook %s failed: %d", "pre-tool", 3)

	// File and directory permissions.
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s) error = %v", path, err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("log file mode = %o, want 600", perm)
	}
	if dirSt, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("Stat(dir) error = %v", err)
	} else if perm := dirSt.Mode().Perm(); perm != 0o700 {
		t.Errorf("log dir mode = %o, want 700", perm)
	}

	// Line shape and count.
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []errorLogLine
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e errorLogLine
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("log line %q is not valid JSON: %v", line, err)
		}
		if _, err := time.Parse(time.RFC3339, e.TS); err != nil {
			t.Errorf("log line ts %q is not RFC3339: %v", e.TS, err)
		}
		lines = append(lines, e)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("got %d log lines, want 2", len(lines))
	}
	if lines[0].Component != "compaction" || lines[0].Message != "walk aborted after 2 messages" {
		t.Errorf("first line = %+v, want the compaction entry", lines[0])
	}
	if lines[1].Component != "diagnostic" || lines[1].Message != "hook pre-tool failed: 3" {
		t.Errorf("second line = %+v, want the formatted diagnostic entry", lines[1])
	}
}

// TestErrorLog_BestEffort pins the best-effort contract: logging never
// panics and never fails the caller — a nil log, an empty path, and an
// unwritable location are all silent no-ops.
func TestErrorLog_BestEffort(t *testing.T) {
	var nilLog *ErrorLog
	nilLog.Log("compaction", "must not panic") // nil receiver

	if _, err := OpenErrorLogAt(""); err == nil {
		t.Error("OpenErrorLogAt(\"\") = nil error, want an error")
	}

	// A path whose parent is a FILE cannot be created.
	file := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenErrorLogAt(filepath.Join(file, "late-errors.log")); err == nil {
		t.Error("OpenErrorLogAt under a file-parent = nil error, want an error")
	}
}

// TestProcessWideErrorLog pins the global helpers: LogError goes through the
// lazily opened process log; SetErrorLog re-points it (and nil disables).
func TestProcessWideErrorLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "late-errors.log")
	l, err := OpenErrorLogAt(path)
	if err != nil {
		t.Fatal(err)
	}

	old := processLog
	t.Cleanup(func() { SetErrorLog(old) })

	SetErrorLog(l)
	LogError("test", "via the process log")
	LogErrorf("test", "formatted %d", 42)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "\n"); n != 2 {
		t.Fatalf("process log holds %d lines, want 2:\n%s", n, data)
	}
	if !strings.Contains(string(data), `"component":"test"`) {
		t.Errorf("log missing the component field:\n%s", data)
	}

	// A nil install disables logging; a later LogError must not panic or
	// resurrect the previous log.
	SetErrorLog(nil)
	LogError("test", "silently dropped")
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "silently dropped") {
		t.Error("a nil process log must drop entries, not append them")
	}
}

// TestErrorLog_ConcurrentAppends: appends from many goroutines interleave as
// whole lines — the file always parses line-per-line.
func TestErrorLog_ConcurrentAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "late-errors.log")
	l, err := OpenErrorLogAt(path)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			l.Logf("concurrent", "writer %d says hello hello hello", n)
		}(i)
	}
	wg.Wait()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	count := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e errorLogLine
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("torn or invalid line %q: %v", line, err)
		}
		count++
	}
	if count != 32 {
		t.Errorf("parsed %d lines, want 32", count)
	}
}

// TestTruncateErrorMessage pins the write-side growth bound: messages at or
// under the cap pass through untouched; longer ones are cut to the cap,
// stay valid UTF-8 (never torn mid-rune, whatever the boundary lands on),
// keep their prefix, and name how many bytes were dropped.
func TestTruncateErrorMessage(t *testing.T) {
	if got := truncateErrorMessage("short"); got != "short" {
		t.Errorf("truncateErrorMessage(short) = %q, want it unchanged", got)
	}

	big := strings.Repeat("x", maxErrorLogMessage+100)
	got := truncateErrorMessage(big)
	if !utf8.ValidString(got) {
		t.Error("ASCII truncation produced invalid UTF-8")
	}
	if !strings.HasPrefix(got, strings.Repeat("x", 64)) {
		t.Error("truncation dropped content before the cap")
	}
	if !strings.HasSuffix(got, " bytes truncated]") {
		t.Errorf("truncated message %q… lacks the truncation marker", got[:32])
	}
	if want := maxErrorLogMessage + len(" …[+100 bytes truncated]"); len(got) != want {
		t.Errorf("truncated message is %d bytes, want %d", len(got), want)
	}

	// A cap that lands mid-rune (3-byte runes: 16384 % 3 != 0) backs off to
	// a clean boundary instead of emitting a torn sequence.
	torn := strings.Repeat("日", maxErrorLogMessage/3+10)
	got = truncateErrorMessage(torn)
	if !utf8.ValidString(got) {
		t.Error("rune-boundary truncation produced invalid UTF-8")
	}
	if !strings.HasPrefix(got, strings.Repeat("日", 64)) {
		t.Error("rune-boundary truncation dropped content before the cap")
	}
	if !strings.HasSuffix(got, " bytes truncated]") {
		t.Errorf("rune-boundary truncated message %q… lacks the truncation marker", got[:32])
	}
}

// TestErrorLog_TruncatesHugeMessages pins the bound end to end: a message
// far past the cap lands as ONE valid JSON line carrying the truncation
// marker, and a normal message passes through untouched.
func TestErrorLog_TruncatesHugeMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "late-errors.log")
	l, err := OpenErrorLogAt(path)
	if err != nil {
		t.Fatal(err)
	}

	l.Log("compaction", strings.Repeat("x", 4*maxErrorLogMessage)+" ends here")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var e errorLogLine
	if err := json.Unmarshal(data, &e); err != nil {
		t.Fatalf("truncated line is not valid JSON: %v", err)
	}
	if e.Component != "compaction" {
		t.Errorf("component = %q, want compaction", e.Component)
	}
	if !strings.HasSuffix(e.Message, " bytes truncated]") {
		t.Errorf("truncated message lacks the truncation marker: %q…", e.Message[:min(64, len(e.Message))])
	}

	l.Log("compaction", "small and fine")
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"message":"small and fine"`) {
		t.Errorf("small message was altered:\n%s", data)
	}
}

// TestPublishProcessLog_InstallWinsOverLazyOpen pins the lazy-open race
// fix: a default-log open finishing AFTER an explicit SetErrorLog must not
// clobber the install. Before the fix, ensureProcessLog published the
// default log unconditionally once its sync.Once body ran, so an install
// landing inside the open window was silently reverted.
func TestPublishProcessLog_InstallWinsOverLazyOpen(t *testing.T) {
	oldLog, oldInstalled := processLog, errorLogInstalled
	t.Cleanup(func() {
		errorLogMu.Lock()
		defer errorLogMu.Unlock()
		processLog, errorLogInstalled = oldLog, oldInstalled
	})

	lazy, err := OpenErrorLogAt(filepath.Join(t.TempDir(), "lazy.log"))
	if err != nil {
		t.Fatal(err)
	}
	installed, err := OpenErrorLogAt(filepath.Join(t.TempDir(), "installed.log"))
	if err != nil {
		t.Fatal(err)
	}

	// SetErrorLog runs first (as it does in every test and in any wiring
	// that installs a sink), then the lazy open's Once body finishes and
	// publishes the default.
	SetErrorLog(installed)
	publishProcessLog(lazy)

	errorLogMu.Lock()
	got := processLog
	errorLogMu.Unlock()
	if got != installed {
		t.Error("the lazy open clobbered an explicit SetErrorLog install")
	}
}

// TestProcessWideErrorLog_ConcurrentInstallAndLog smoke-tests the global
// helpers under concurrent use: SetErrorLog racing LogError must neither
// panic nor tear lines (go test -race covers the memory model). Every
// append lands in one of the two installed logs, and every line in either
// file must parse as whole JSON.
func TestProcessWideErrorLog_ConcurrentInstallAndLog(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.log")
	pathB := filepath.Join(dir, "b.log")
	a, err := OpenErrorLogAt(pathA)
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenErrorLogAt(pathB)
	if err != nil {
		t.Fatal(err)
	}
	old := processLog
	t.Cleanup(func() { SetErrorLog(old) })

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			target := a
			if n%2 == 1 {
				target = b
			}
			for j := 0; j < 50; j++ {
				SetErrorLog(target)
				LogError("race", "concurrent install and append")
			}
		}(i)
	}
	wg.Wait()

	for _, p := range []string{pathA, pathB} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line == "" {
				continue
			}
			var e errorLogLine
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Errorf("torn or invalid line in %s: %q: %v", p, line, err)
			}
		}
	}
}
