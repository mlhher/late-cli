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
// history for an archived output: the first headChars characters of the
// original output (rune-safe), then a self-documenting marker carrying the
// total size and the archive path, then the last tailChars characters
// (rune-safe) when anything was actually cut. The marker names the
// sanctioned retrieval tool — read_file with the archive path — so the agent
// can view the rest without guessing (cat/grep are gated).
//
// It contains no timestamps and no randomness — the same (output, path) pair
// always produces the byte-identical form, which is what keeps the request
// prefix (and the prompt cache behind it) stable. See the OutputArchive
// determinism contract above: this form is written to history once and
// never regenerated.
//
// Sizes in the marker are rune counts ("chars"); callers band on byte
// length (see executor.ArchiveThresholdChars and
// executor.ArchiveInlineFullChars). When the output fits in headChars
// nothing is cut and the tail is omitted; head and tail can only overlap
// when the output is at most headChars+tailChars runes — impossible for the
// executor, which renders this form only above ArchiveInlineFullChars
// (3072 > 2000+500).
func FormatReference(path string, output string, headChars, tailChars int) string {
	if headChars < 0 {
		headChars = 0
	}
	if tailChars < 0 {
		tailChars = 0
	}
	runes := []rune(output)
	head := truncateRunes(output, headChars)
	if len(runes) <= headChars {
		// Nothing was cut: keep the whole output, still point at the archive.
		return fmt.Sprintf("%s\n…[full output archived: %s — read_file it to view everything]", head, path)
	}
	tail := ""
	if tailChars > 0 {
		if tailChars > len(runes) {
			tailChars = len(runes)
		}
		tail = string(runes[len(runes)-tailChars:])
	}
	return fmt.Sprintf("%s\n…[output truncated: %d chars total. Head above / tail below. Full output archived: %s — read_file it to view everything]\n%s",
		head, len(runes), path, tail)
}

// truncateRunes cuts s to at most max runes, rune-safe and without a suffix.
func truncateRunes(s string, max int) string {
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
