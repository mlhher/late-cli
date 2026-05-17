// Package archive provides session archive persistence, compaction, and search.
// It is dependency-free (stdlib + late/internal/client only) so both internal/session
// and internal/tool can import it without creating a cycle.
package archive

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"late/internal/client"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const SchemaVersion = 1

// ArchivePath derives the archive file path from a history path.
// If historyPath ends in ".json", replaces suffix; otherwise appends.
func ArchivePath(historyPath string) string {
	if strings.HasSuffix(historyPath, ".json") {
		return strings.TrimSuffix(historyPath, ".json") + ".archive.json"
	}
	return historyPath + ".archive.json"
}

// LockPath derives the lock file path from a history path.
func LockPath(historyPath string) string {
	if strings.HasSuffix(historyPath, ".json") {
		return strings.TrimSuffix(historyPath, ".json") + ".archive.lock"
	}
	return historyPath + ".archive.lock"
}

// BaseSessionID extracts the session ID token from a history file path.
// e.g. "/sessions/session-abc.json" → "session-abc"
func BaseSessionID(historyPath string) string {
	base := filepath.Base(historyPath)
	if strings.HasSuffix(base, ".json") {
		return strings.TrimSuffix(base, ".json")
	}
	return base
}

// HashMessage returns a stable sha256 hex hash of a ChatMessage's JSON representation.
func HashMessage(msg client.ChatMessage) string {
	data, _ := json.Marshal(msg)
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}

// HashBytes returns a sha256 checksum of raw bytes.
func HashBytes(data []byte) [32]byte {
	return sha256.Sum256(data)
}

// SessionArchive is the top-level on-disk archive structure.
type SessionArchive struct {
	SessionID            string         `json:"session_id"`
	SchemaVersion        int            `json:"schema_version"`
	ArchiveGeneration    int64          `json:"archive_generation"`
	CompactionCount      int            `json:"compaction_count"`
	ArchivedMessageCount int            `json:"archived_message_count"`
	NextSequence         int64          `json:"next_sequence"`
	CreatedAt            time.Time      `json:"created_at"`
	UpdatedAt            time.Time      `json:"updated_at"`
	Chunks               []ArchiveChunk `json:"chunks"`
}

// ArchiveChunk groups a contiguous slice of archived messages.
type ArchiveChunk struct {
	ChunkID       string            `json:"chunk_id"`
	StartSequence int64             `json:"start_sequence"`
	EndSequence   int64             `json:"end_sequence"`
	Messages      []ArchivedMessage `json:"messages"`
	ChunkHash     string            `json:"chunk_hash"`
	CreatedAt     time.Time         `json:"created_at"`
}

// ArchivedMessage wraps a ChatMessage with archive bookkeeping.
type ArchivedMessage struct {
	MessageID  string             `json:"message_id"`
	Sequence   int64              `json:"sequence"`
	Role       string             `json:"role"`
	Hash       string             `json:"hash"`
	ArchivedAt time.Time          `json:"archived_at"`
	Message    client.ChatMessage `json:"message"`
}

// New constructs an empty SessionArchive for the given session.
func New(sessionID string) *SessionArchive {
	now := time.Now().UTC()
	return &SessionArchive{
		SessionID:     sessionID,
		SchemaVersion: SchemaVersion,
		CreatedAt:     now,
		UpdatedAt:     now,
		Chunks:        []ArchiveChunk{},
	}
}

// Save atomically writes the archive to disk.
func Save(path string, archive *SessionArchive) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory for archive: %w", err)
	}

	data, err := json.MarshalIndent(archive, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal archive: %w", err)
	}

	tmp, err := os.CreateTemp(dir, "archive-*.json.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp archive file: %w", err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write archive temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close archive temp file: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0600); err != nil {
		return fmt.Errorf("failed to set archive file permissions: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("failed to rename archive temp file: %w", err)
	}
	return nil
}

// Load reads and parses the archive from disk.
// Returns a fresh empty archive (no error) if the file does not exist.
// Returns nil + error if the file is corrupt/unreadable.
func Load(path, sessionID string) (*SessionArchive, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return New(sessionID), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read archive file: %w", err)
	}

	var archive SessionArchive
	if err := json.Unmarshal(data, &archive); err != nil {
		return nil, fmt.Errorf("corrupt archive (unmarshal failed): %w", err)
	}

	if archive.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("archive schema version mismatch: got %d, want %d", archive.SchemaVersion, SchemaVersion)
	}

	return &archive, nil
}

// DeleteFiles removes the archive and lock files associated with a history path.
func DeleteFiles(historyPath string) error {
	ap := ArchivePath(historyPath)
	lp := LockPath(historyPath)
	var errs []string
	for _, p := range []string{ap, lp} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors deleting archive files: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Reconstruct returns all messages in canonical order: archived chunks sorted by
// sequence, then active history appended in its current slice order.
func Reconstruct(archive *SessionArchive, active []client.ChatMessage) []client.ChatMessage {
	if archive == nil {
		return active
	}
	var out []client.ChatMessage
	for _, chunk := range archive.Chunks {
		for _, am := range chunk.Messages {
			out = append(out, am.Message)
		}
	}
	out = append(out, active...)
	return out
}

// WriteAtomicTemp creates a temp file in dir, writes data, and returns the path.
// Caller must rename or remove the returned file.
func WriteAtomicTemp(dir, pattern string, data []byte) (string, error) {
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0600); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

// MustMarshalJSON JSON-encodes v, panicking on error.
func MustMarshalJSON(v any) []byte {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("MustMarshalJSON: %v", err))
	}
	return data
}
