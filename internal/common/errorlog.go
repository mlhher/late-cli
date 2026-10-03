package common

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"late/internal/pathutil"
)

// The durable critical-error log: an append-only JSONL record of the
// operational failures a late session should not lose — compaction walk
// aborts and save failures, store/shadow open failures, auth poisoning, and
// the mid-session diagnostics the TUI surfaces as toasts. A toast disappears
// with the terminal; this file does not, so post-mortems can answer "what
// broke" without reproducing the session.
//
// The mechanics mirror compaction's ShadowLog exactly: goroutine-safe
// (mutex), crash-atomic per line (ONE Write call of the whole line on an
// O_APPEND descriptor, so concurrent late processes interleave whole lines),
// the file is created 0600 and its parent directories 0700, and the path
// follows the same platform pattern as every other late data file
// (~/.local/share/late/late-errors.log; Windows keeps it under the config
// dir).
//
// Logging is best-effort BY CONTRACT: Log never returns an error and never
// fails the operation it was called from — a broken log must not be able to
// break compaction, persistence, or the diagnostic path. Callers go through
// the process-wide LogError/LogErrorf helpers (lazily opened default log,
// installable with SetErrorLog so tests can point it at a temp path); a nil
// or unopenable log makes them silent no-ops.

// DefaultErrorLogPath returns the critical-error log location:
// ~/.local/share/late/late-errors.log, resolved through pathutil.LateDataDir
// — the same platform handling as the session dir, the shadow log, and the
// record store (Windows keeps everything under the config dir).
func DefaultErrorLogPath() (string, error) {
	dir, err := pathutil.LateDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "late-errors.log"), nil
}

// errorLogLine is the shape of one log line:
// {"ts":"RFC3339","component":"...","message":"..."}.
type errorLogLine struct {
	TS        string `json:"ts"`
	Component string `json:"component"`
	Message   string `json:"message"`
}

// ErrorLog is the append-only critical-error log. Safe for concurrent use.
//
// The log is deliberately append-only and NEVER rotated: it is a
// post-mortem record ("what broke in that session"), not a telemetry
// stream, and compaction/store failures are rare by design. Growth is
// bounded on the write side instead — one message is capped at
// maxErrorLogMessage — and the file stays small in practice; if it ever
// outgrows its usefulness, delete it: late recreates it on the next append.
type ErrorLog struct {
	path string
	mu   sync.Mutex
}

// OpenErrorLog opens (creating parent directories 0700) the default
// critical-error log at DefaultErrorLogPath. The file itself is created 0600
// on first append.
func OpenErrorLog() (*ErrorLog, error) {
	p, err := DefaultErrorLogPath()
	if err != nil {
		return nil, err
	}
	return OpenErrorLogAt(p)
}

// OpenErrorLogAt opens the critical-error log at path, creating parent
// directories with 0700 (the log file itself is created 0600 on first
// append). An empty path is an error — a silent no-op log must be a decision
// (SetErrorLog(nil)), not a forgotten argument.
func OpenErrorLogAt(path string) (*ErrorLog, error) {
	if path == "" {
		return nil, fmt.Errorf("error log path is empty")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create error log dir %s: %w", dir, err)
	}
	return &ErrorLog{path: path}, nil
}

// Path returns the log file path.
func (l *ErrorLog) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Log appends one line naming the component ("compaction", "store",
// "diagnostic", ...) and the message. The line is serialized first so a
// marshal failure cannot leave a torn line behind; every failure — marshal,
// open, write — is swallowed: logging is best-effort and must never fail the
// operation it was called from. A nil log is a no-op. Messages longer than
// maxErrorLogMessage are truncated (truncateErrorMessage) — the log is
// never rotated, so one huge error must not dictate the file's growth.
func (l *ErrorLog) Log(component, message string) {
	if l == nil {
		return
	}
	line, err := json.Marshal(errorLogLine{
		TS:        time.Now().UTC().Format(time.RFC3339),
		Component: component,
		Message:   truncateErrorMessage(message),
	})
	if err != nil {
		return // plain-string struct; defensive only
	}
	line = append(line, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(line)
}

// Logf is Log with formatting.
func (l *ErrorLog) Logf(component, format string, args ...any) {
	if l == nil {
		return
	}
	l.Log(component, fmt.Sprintf(format, args...))
}

// maxErrorLogMessage caps one entry's message. The log is append-only and
// never rotated (see ErrorLog), so one enormous error — a provider failure
// embedding a full response body, say — must not dictate the file's
// growth. Longer messages are cut at the cap with an explicit truncation
// marker; the line stays valid single-line JSON.
const maxErrorLogMessage = 16 << 10 // 16 KiB

// truncateErrorMessage caps message at maxErrorLogMessage bytes, backing off
// to a clean rune boundary so the capped text stays valid UTF-8, and appends
// a marker naming how many bytes were dropped.
func truncateErrorMessage(message string) string {
	if len(message) <= maxErrorLogMessage {
		return message
	}
	cut := message[:maxErrorLogMessage]
	for len(cut) > 0 {
		if r, size := utf8.DecodeLastRuneInString(cut); r != utf8.RuneError || size > 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return cut + fmt.Sprintf(" …[+%d bytes truncated]", len(message)-len(cut))
}

// The process-wide critical-error log. Tests install a temp-path log with
// SetErrorLog; production wiring opens the default once at startup.
// errorLogInstalled records that SetErrorLog ran, so the lazily opened
// default can never overwrite an explicit install (see publishProcessLog).
var (
	errorLogOnce      sync.Once
	errorLogMu        sync.Mutex
	processLog        *ErrorLog
	errorLogInstalled bool
)

// SetErrorLog installs l as the process-wide critical-error log; nil
// disables it (every LogError call becomes a no-op).
func SetErrorLog(l *ErrorLog) {
	errorLogMu.Lock()
	defer errorLogMu.Unlock()
	processLog = l
	errorLogInstalled = true
	// A later lazy open must not clobber an explicit install.
	errorLogOnce.Do(func() {})
}

// LogError appends one line to the process-wide critical-error log, opening
// it lazily at the default path on first use (an unopenable log stays nil:
// logging is best-effort). Never fails the caller.
func LogError(component, message string) {
	ensureProcessLog()
	errorLogMu.Lock()
	l := processLog
	errorLogMu.Unlock()
	l.Log(component, message)
}

// LogErrorf is LogError with formatting.
func LogErrorf(component, format string, args ...any) {
	ensureProcessLog()
	errorLogMu.Lock()
	l := processLog
	errorLogMu.Unlock()
	l.Logf(component, format, args...)
}

// ensureProcessLog lazily opens the default log exactly once. A failure
// leaves processLog nil and is swallowed — best-effort by contract.
func ensureProcessLog() {
	errorLogOnce.Do(func() {
		l, err := OpenErrorLog()
		if err != nil {
			return // no log this process; logging must not break anything
		}
		publishProcessLog(l)
	})
}

// publishProcessLog installs the lazily opened default log unless an
// explicit SetErrorLog landed while the open was in flight — the install
// wins, whatever the interleaving. Both writers hold errorLogMu, and the
// sync.Once guarantees the lazy open runs at most once, so this closes the
// one clobber window the lazy open used to have.
func publishProcessLog(l *ErrorLog) {
	errorLogMu.Lock()
	defer errorLogMu.Unlock()
	if !errorLogInstalled {
		processLog = l
	}
}
