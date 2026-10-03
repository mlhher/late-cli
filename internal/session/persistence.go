package session

import (
	"encoding/json"
	"fmt"
	"late/internal/client"
	"late/internal/common"
	"os"
	"path/filepath"
	"time"
)

// SaveHistory atomically saves the chat history to the specified path.
func SaveHistory(path string, history []client.ChatMessage) error {
	if path == "" {
		return nil // Skip saving if no path provided
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal history: %w", err)
	}

	// Write to a temporary file first
	tmpFile, err := os.CreateTemp(dir, "history-*.json.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name()) // Clean up if something goes wrong before rename

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to write to temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpFile.Name(), path); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// LoadHistory loads the chat history from the specified path.
func LoadHistory(path string) ([]client.ChatMessage, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return []client.ChatMessage{}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read history file: %w", err)
	}

	// A zero-length file holds zero messages: treat it like a missing file
	// instead of a hard unmarshal failure ("unexpected end of JSON input")
	// that would brick resume for a file late itself never writes empty
	// (SaveHistory only ever renames complete documents over the target).
	if len(data) == 0 {
		return []client.ChatMessage{}, nil
	}

	var history []client.ChatMessage
	if err := json.Unmarshal(data, &history); err != nil {
		return nil, fmt.Errorf("failed to unmarshal history: %w", err)
	}

	return history, nil
}

// LoadHistoryRecovering loads the chat history like LoadHistory, but when
// the load of a NON-EMPTY file fails — a corrupt or truncated document
// (external edit, filesystem damage, a file from an older broken build) —
// it first preserves the raw bytes at <path>.corrupt-<timestamp> so the
// caller's subsequent empty-start + first save cannot destroy them, and
// records the failure in the critical-error log. The load error is still
// returned; callers decide what to do (the resume path warns and starts
// empty). A zero-length file is not corruption: LoadHistory already treats
// it as an empty history, and no backup is made.
func LoadHistoryRecovering(path string) ([]client.ChatMessage, error) {
	history, err := LoadHistory(path)
	if err == nil {
		return history, nil
	}
	// Distinguish "file exists but unreadable/corrupt" from "missing": only
	// the former is worth a backup. ReadFile of a missing path already
	// returned before unmarshal; a stat/permission failure lands here too,
	// where the best-effort copy simply fails and the error still surfaces.
	if data, readErr := os.ReadFile(path); readErr == nil && len(data) > 0 {
		backupPath := path + ".corrupt-" + time.Now().Format("20060102-150405")
		if writeErr := os.WriteFile(backupPath, data, 0o600); writeErr != nil {
			common.LogErrorf("session", "history %s is unreadable (%v) and the raw bytes could not be backed up to %s: %v", path, err, backupPath, writeErr)
		} else {
			common.LogErrorf("session", "history %s is unreadable (%v); raw bytes preserved at %s", path, err, backupPath)
		}
	} else if readErr != nil {
		common.LogErrorf("session", "history %s is unreadable: %v (raw bytes could not be read for backup: %v)", path, err, readErr)
	}
	return history, err
}
