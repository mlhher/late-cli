package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// isValidPathElement reports whether id is a safe single path element
// under the sessions directory: non-empty, not "." or "..", and free of
// path separators (/ and \) and NUL bytes.
func isValidPathElement(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	return !strings.ContainsAny(id, "/\\") && !strings.ContainsRune(id, 0)
}

// SubagentHistoryDir returns the directory holding a session's subagent
// histories: <sessionsDir>/<sessionID>/subagents. It is created lazily by
// SaveHistory (MkdirAll 0700), so this helper does not create it.
func SubagentHistoryDir(sessionID string) (string, error) {
	if !isValidPathElement(sessionID) {
		return "", fmt.Errorf("invalid session ID: %q", sessionID)
	}
	sessionsDir, err := SessionDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(sessionsDir, sessionID, "subagents"), nil
}

// SubagentHistoryPath returns the full path of a subagent history file:
// <sessionsDir>/<parentSessionID>/subagents/<childID>.json.
func SubagentHistoryPath(parentSessionID, childID string) (string, error) {
	if !isValidPathElement(parentSessionID) {
		return "", fmt.Errorf("invalid session ID: %q", parentSessionID)
	}
	if !isValidPathElement(childID) {
		return "", fmt.Errorf("invalid child ID: %q", childID)
	}
	dir, err := SubagentHistoryDir(parentSessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, childID+".json"), nil
}

// RemoveSessionFolder deletes a session's folder (containing its subagent
// histories) if it exists. Flat files <sessionID>.json / .meta.json are
// distinct names and are never touched. Returns nil when the folder does
// not exist (legacy sessions) or for unsafe IDs (empty, ".", "..", path
// separators, NUL bytes).
func RemoveSessionFolder(sessionID string) error {
	if !isValidPathElement(sessionID) {
		return nil
	}
	sessionsDir, err := SessionDir()
	if err != nil {
		return fmt.Errorf("failed to get session directory: %w", err)
	}
	return os.RemoveAll(filepath.Join(sessionsDir, sessionID))
}
