package session

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	// outputArchiveDirName is the per-session folder tool outputs are
	// archived into, a sibling of the subagents dir under the session
	// folder (<sessionsDir>/<sessionID>/tool-outputs).
	outputArchiveDirName = "tool-outputs"

	// outputArchiveFileMode keeps archived tool outputs as private as
	// session history files (persistence.go historyFileMode).
	outputArchiveFileMode os.FileMode = 0o600
)

// OutputArchive stores oversized tool outputs on disk so a compact,
// deterministic reference can stand in the conversation instead of the
// full text.
//
// The archive is cache-neutral by design. The reference form is generated
// EXACTLY ONCE — at admission, inside ExecuteToolCalls, before the result
// enters history — and is then stored in history verbatim: the request
// renderer copies history unchanged, so the bytes sent to the model never
// change afterward. Nothing ever re-reads the archive to rewrite history
// (no retroactive substitution anywhere); the file exists only so a human
// — or a future harness feature — can inspect the original output.
//
// Files are content-addressed (<sha256[:16]>.txt): identical outputs dedupe
// to the same file, and the reference path for a given output is therefore
// deterministic — the same output always produces the byte-identical
// reference form, which keeps prompt-cache prefixes stable.
type OutputArchive struct {
	dir string
	mu  sync.Mutex
}

// NewOutputArchive returns an archive rooted at dir, creating it
// (MkdirAll 0700) eagerly. Failures surface here, at wiring time, rather
// than on the first tool result.
func NewOutputArchive(dir string) (*OutputArchive, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create tool-output archive directory %s: %w", dir, err)
	}
	return &OutputArchive{dir: dir}, nil
}

// Archive writes output to a content-addressed file
// <dir>/<sha256[:16]>.txt and returns its path. Idempotent: an existing
// file is left untouched and its path returned (identical content maps to
// the identical name, so rewriting could only ever produce the same bytes).
func (a *OutputArchive) Archive(output string) (string, error) {
	sum := sha256.Sum256([]byte(output))
	name := hex.EncodeToString(sum[:])[:16] + ".txt"
	path := filepath.Join(a.dir, name)

	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := os.WriteFile(path, []byte(output), outputArchiveFileMode); err != nil {
		return "", fmt.Errorf("failed to archive tool output to %s: %w", path, err)
	}
	return path, nil
}

// FormatReference renders the compact, deterministic form that stands in
// history for an archived output: the first headChars characters
// (rune-safe) of the original output followed by the archive pointer. It
// contains no timestamps and no randomness — the same output always
// produces the byte-identical form, which is what keeps the request
// prefix (and the prompt cache behind it) stable. See the OutputArchive
// determinism contract above: this form is written to history once and
// never regenerated.
func FormatReference(path string, output string, headChars int) string {
	if headChars < 0 {
		headChars = 0
	}
	return fmt.Sprintf("%s\n…[full output archived: %s]", truncateRunes(output, headChars), path)
}

// truncateRunes cuts s to at most max runes, rune-safe and without a suffix.
// Local to the archive's reference form so it stays self-contained: the same
// output always maps to the same reference bytes.
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

// OutputArchiveDir returns the directory holding a session's archived tool
// outputs: <sessionsDir>/<sessionID>/tool-outputs. It mirrors
// SubagentHistoryDir (paths.go) — same sessions root, same session folder,
// same validity rules — and, like it, does NOT create the directory.
func OutputArchiveDir(sessionID string) (string, error) {
	if !isValidPathElement(sessionID) {
		return "", fmt.Errorf("invalid session ID: %q", sessionID)
	}
	sessionsDir, err := SessionDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(sessionsDir, sessionID, outputArchiveDirName), nil
}
