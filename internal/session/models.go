package session

import (
	"encoding/json"
	"fmt"
	"late/internal/common"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SessionMeta represents metadata about a saved session
type SessionMeta struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"` // Short title derived from first user message
	CreatedAt      time.Time `json:"created_at"`
	LastUpdated    time.Time `json:"last_updated"`
	HistoryPath    string    `json:"history_path"`     // Full path to history file
	LastUserPrompt string    `json:"last_user_prompt"` // Last 100 chars of last user message
	MessageCount   int       `json:"message_count"`

	// Archive compaction metadata (Phase 8 observability).
	CompactionCount      int       `json:"compaction_count,omitempty"`
	ArchivedMessageCount int       `json:"archived_message_count,omitempty"`
	LastCompactionAt     time.Time `json:"last_compaction_at,omitempty"`
}

// SessionDir returns the directory where session metadata and histories are stored
var SessionDir = func() (string, error) {
	return common.LateSessionDir()
}

// SaveSessionMeta saves session metadata to the sessions directory
func SaveSessionMeta(meta SessionMeta) error {
	sessionsDir, err := SessionDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(sessionsDir, 0700); err != nil {
		return fmt.Errorf("failed to create sessions directory: %w", err)
	}

	metaPath := filepath.Join(sessionsDir, meta.ID+".meta.json")
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session meta: %w", err)
	}

	// Atomic write
	tmpFile, err := os.CreateTemp(sessionsDir, "meta-*.json.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to write to temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	if err := os.Rename(tmpFile.Name(), metaPath); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// LoadSessionMeta loads session metadata by ID or prefix
func LoadSessionMeta(id string) (*SessionMeta, error) {
	sessionsDir, err := SessionDir()
	if err != nil {
		return nil, err
	}

	// Try exact match first
	exactPath := filepath.Join(sessionsDir, id+".meta.json")
	if _, err := os.Stat(exactPath); err == nil {
		return loadMetaFile(exactPath)
	}

	// Try prefix match
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return nil, err
	}

	var matches []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".meta.json") {
			name := strings.TrimSuffix(entry.Name(), ".meta.json")
			if strings.HasPrefix(name, id) {
				matches = append(matches, name)
			}
		}
	}

	if len(matches) == 0 {
		return nil, nil // Not found
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("session ID %q is ambiguous, matches: %s", id, strings.Join(matches, ", "))
	}

	// Exactly one match — use the matched name to build exact path
	matchedName := matches[0]
	exactPath = filepath.Join(sessionsDir, matchedName+".meta.json")
	return loadMetaFile(exactPath)
}

// loadMetaFile handles the actual reading and unmarshaling
func loadMetaFile(path string) (*SessionMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read session meta: %w", err)
	}

	var meta SessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session meta: %w", err)
	}

	return &meta, nil
}

// ListSessions returns all session metadata, sorted by last_updated descending
func ListSessions() ([]SessionMeta, error) {
	sessionsDir, err := SessionDir()
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []SessionMeta{}, nil
		}
		return nil, fmt.Errorf("failed to read sessions directory: %w", err)
	}

	var metas []SessionMeta
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".meta.json") {
			id := strings.TrimSuffix(entry.Name(), ".meta.json")
			meta, err := LoadSessionMeta(id)
			if err == nil && meta != nil {
				metas = append(metas, *meta)
			}
		}
	}

	// Sort by last_updated ascending (oldest first)
	sort.Slice(metas, func(i, j int) bool {
		return metas[i].LastUpdated.Before(metas[j].LastUpdated)
	})

	return metas, nil
}
