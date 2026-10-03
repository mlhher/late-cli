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
	ID                    string    `json:"id"`
	Title                 string    `json:"title"` // Short title derived from first user message
	CreatedAt             time.Time `json:"created_at"`
	LastUpdated           time.Time `json:"last_updated"`
	HistoryPath           string    `json:"history_path"`     // Full path to history file
	LastUserPrompt        string    `json:"last_user_prompt"` // Last 100 chars of last user message
	MessageCount          int       `json:"message_count"`
	SubagentSeq           int       `json:"subagent_seq"`
	SaveSubagentHistories *bool     `json:"save_subagent_histories,omitempty"`
	WorkingDir            string    `json:"working_dir,omitempty"` // Absolute path of the project directory where the session was started
	// CompactionHighWater is the history compaction high-water mark: the
	// monotonic message index below which the frozen prefix ends. The
	// compactor never re-scores or rewrites a message with a smaller index,
	// so the prompt-cache anchor survives across compaction runs and
	// restarts. omitempty keeps legacy sidecars byte-identical while the
	// mark is zero. See compact.go for the invariants it enforces.
	CompactionHighWater int `json:"compaction_high_water,omitempty"`
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

// GetLatestSession returns the metadata of the latest session (most recently
// updated), regardless of which project directory sessions were started in.
// Each sidecar is loaded by its exact enumerated path, so a sidecar that
// disappears or fails to load is merely skipped and the scan never falls back
// to a different session with a matching ID prefix. If no sessions exist, it
// returns nil, nil.
func GetLatestSession() (*SessionMeta, error) {
	sessionsDir, err := SessionDir()
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read sessions directory: %w", err)
	}

	var latest *SessionMeta
	var latestModTime time.Time
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		meta, err := loadEnumeratedMeta(filepath.Join(sessionsDir, entry.Name()))
		if err != nil || meta == nil {
			continue
		}
		if latest == nil || info.ModTime().After(latestModTime) {
			latest = meta
			latestModTime = info.ModTime()
		}
	}
	return latest, nil
}

// loadEnumeratedMeta loads one enumerated sidecar by its exact path. It is a
// package-level variable so tests can simulate sidecars that vanish or turn
// unreadable between enumeration (os.ReadDir + entry.Info()) and load — the
// race window that historically produced a (nil, nil) result and a startup
// panic. Production always uses loadMetaFile.
var loadEnumeratedMeta = loadMetaFile

// GetLatestSessionForDir returns the metadata of the most recently updated
// session that was started in dir, matched against the working_dir recorded
// in each session's metadata. Sessions created before working_dir was
// recorded (empty WorkingDir) are ignored. If no matching session exists,
// it returns nil, nil.
//
// Each enumerated .meta.json sidecar is loaded by its exact path rather than
// through LoadSessionMeta, whose ID-prefix fallback could silently return a
// different session sharing the ID prefix; a sidecar that fails to load or
// yields no metadata is skipped (defensively including a nil meta, although
// loadMetaFile never returns (nil, nil)).
//
// Directory matching takes a lexical fast path (filepath.Clean equality) and
// then compares directory identity with os.Stat + os.SameFile when both paths
// exist, so a session recorded under a real directory is still found when the
// same directory is addressed through a symlink. SameFile also covers
// case-variant aliases on case-insensitive filesystems, so paths are never
// lowercased or case-folded here. When either directory is missing the
// identity check is unavailable and the lexical result stands.
func GetLatestSessionForDir(dir string) (*SessionMeta, error) {
	sessionsDir, err := SessionDir()
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read sessions directory: %w", err)
	}

	want := filepath.Clean(dir)
	var latest *SessionMeta
	var latestModTime time.Time
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		// Load the exact enumerated file: no ID-prefix re-resolution.
		meta, err := loadEnumeratedMeta(filepath.Join(sessionsDir, entry.Name()))
		if err != nil || meta == nil {
			continue
		}
		if meta.WorkingDir == "" || !sameProjectDir(meta.WorkingDir, want) {
			continue
		}
		if latest == nil || info.ModTime().After(latestModTime) {
			latest = meta
			latestModTime = info.ModTime()
		}
	}
	return latest, nil
}

// sameProjectDir reports whether a session's recorded project directory and a
// wanted directory refer to the same directory. The lexical comparison is the
// fast path; when it misses and both paths exist, identity is compared via
// os.SameFile (os.Stat follows symlinks), which also matches case-variant
// aliases on case-insensitive filesystems. If either directory does not
// exist, only the lexical result is available. Paths are never lowercased.
func sameProjectDir(recorded, want string) bool {
	recorded = filepath.Clean(recorded)
	want = filepath.Clean(want)
	if recorded == want {
		return true
	}
	wantInfo, wantErr := os.Stat(want)
	recordedInfo, recordedErr := os.Stat(recorded)
	if wantErr != nil || recordedErr != nil {
		// Missing or inaccessible directory on either side: the identity
		// check cannot run, so lexical equality (already ruled out) stands.
		return false
	}
	return os.SameFile(wantInfo, recordedInfo)
}
