package archive

import (
	"fmt"
	"late/internal/client"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// CompactionConfig holds parameters for a single compaction pass.
type CompactionConfig struct {
	ThresholdMessages  int
	KeepRecentMessages int
	ChunkSize          int
	StaleAfterSeconds  int
}

// CompactionResult captures the outcome of a single compaction pass.
type CompactionResult struct {
	ArchivedCount int
	NoOp          bool
	LockHeld      bool
}

// chunkIDStr generates a deterministic chunk identifier.
func chunkIDStr(generation int64, idx int) string {
	return fmt.Sprintf("chunk-%d-%d", generation, idx)
}

// acquireLock attempts to write a lock file. Returns true if the lock was acquired.
func acquireLock(lp, sessionID string, staleAfterSeconds int) bool {
	f, err := os.OpenFile(lp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		pid := os.Getpid()
		content := fmt.Sprintf(`{"pid":%d,"created_at":%q,"session_id":%q}`, pid, time.Now().UTC().Format(time.RFC3339), sessionID)
		_, _ = f.WriteString(content)
		_ = f.Close()
		return true
	}
	if !os.IsExist(err) {
		return false
	}

	// Lock file exists — check staleness.
	info, err := os.Stat(lp)
	if err != nil {
		return false
	}
	age := time.Since(info.ModTime())
	stale := time.Duration(staleAfterSeconds) * time.Second
	if age < stale {
		if pid := readLockPID(lp); pid > 0 {
			if processAlive(pid) {
				log.Printf("[archive] compaction lock held by pid %d (age %s), skipping compaction", pid, age.Round(time.Second))
				return false
			}
		} else {
			return false
		}
	}

	log.Printf("[archive] stale compaction lock detected (age %s), recovering", age.Round(time.Second))
	_ = os.Remove(lp)

	f, err = os.OpenFile(lp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return false
	}
	pid := os.Getpid()
	content := fmt.Sprintf(`{"pid":%d,"created_at":%q,"session_id":%q}`, pid, time.Now().UTC().Format(time.RFC3339), sessionID)
	_, _ = f.WriteString(content)
	_ = f.Close()
	return true
}

// releaseLock removes the lock file.
func releaseLock(lp string) {
	_ = os.Remove(lp)
}

// readLockPID parses the pid from a lock file.
func readLockPID(lp string) int {
	data, err := os.ReadFile(lp)
	if err != nil {
		return 0
	}
	s := string(data)
	i := strings.Index(s, `"pid":`)
	if i < 0 {
		return 0
	}
	rest := strings.TrimSpace(s[i+6:])
	end := strings.IndexAny(rest, ",}")
	if end < 0 {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest[:end]))
	if err != nil {
		return 0
	}
	return n
}

// processAlive returns true if the given pid appears to be running.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// Compact performs a single compaction pass for the session identified by historyPath.
func Compact(historyPath, sessionID string, active []client.ChatMessage, archive *SessionArchive, cfg CompactionConfig) (CompactionResult, []client.ChatMessage, *SessionArchive, error) {
	if len(active) <= cfg.ThresholdMessages {
		return CompactionResult{NoOp: true}, active, archive, nil
	}

	lp := LockPath(historyPath)
	if !acquireLock(lp, sessionID, cfg.StaleAfterSeconds) {
		return CompactionResult{LockHeld: true}, active, archive, nil
	}
	defer releaseLock(lp)

	eligible := len(active) - cfg.KeepRecentMessages
	if eligible <= 0 {
		return CompactionResult{NoOp: true}, active, archive, nil
	}

	toArchive := active[:eligible]
	remaining := active[eligible:]

	// Build dedup set of already-archived hashes.
	archivedHashes := make(map[string]bool)
	for _, chunk := range archive.Chunks {
		for _, am := range chunk.Messages {
			archivedHashes[am.Hash] = true
		}
	}

	newGeneration := archive.ArchiveGeneration + 1
	var newChunks []ArchiveChunk
	var totalNewMessages int
	now := time.Now().UTC()

	for start := 0; start < len(toArchive); start += cfg.ChunkSize {
		end := start + cfg.ChunkSize
		if end > len(toArchive) {
			end = len(toArchive)
		}
		batch := toArchive[start:end]

		var archMsgs []ArchivedMessage
		for _, msg := range batch {
			h := HashMessage(msg)
			if archivedHashes[h] {
				log.Printf("[archive] skipping duplicate message (hash %s)", h[:8])
				continue
			}
			seq := archive.NextSequence
			archive.NextSequence++
			am := ArchivedMessage{
				MessageID:  fmt.Sprintf("msg-%d", seq),
				Sequence:   seq,
				Role:       msg.Role,
				Hash:       h,
				ArchivedAt: now,
				Message:    msg,
			}
			archMsgs = append(archMsgs, am)
			archivedHashes[h] = true
		}
		if len(archMsgs) == 0 {
			continue
		}

		idx := len(archive.Chunks) + len(newChunks)
		c := ArchiveChunk{
			ChunkID:       chunkIDStr(newGeneration, idx),
			StartSequence: archMsgs[0].Sequence,
			EndSequence:   archMsgs[len(archMsgs)-1].Sequence,
			Messages:      archMsgs,
			CreatedAt:     now,
		}
		var hashes strings.Builder
		for _, am := range archMsgs {
			hashes.WriteString(am.Hash)
		}
		sumArr := HashBytes([]byte(hashes.String()))
		c.ChunkHash = fmt.Sprintf("%x", sumArr)
		newChunks = append(newChunks, c)
		totalNewMessages += len(archMsgs)
	}

	if totalNewMessages == 0 {
		return CompactionResult{NoOp: true}, active, archive, nil
	}

	newArchive := *archive
	newArchive.Chunks = append(append([]ArchiveChunk{}, archive.Chunks...), newChunks...)
	newArchive.ArchivedMessageCount += totalNewMessages
	newArchive.CompactionCount++
	newArchive.UpdatedAt = now

	ap := ArchivePath(historyPath)
	dir := filepath.Dir(historyPath)

	archTmp, err := WriteAtomicTemp(dir, "archive-*.json.tmp", MustMarshalJSON(&newArchive))
	if err != nil {
		return CompactionResult{}, active, archive, fmt.Errorf("archive temp write failed: %w", err)
	}
	defer os.Remove(archTmp)

	activeTmp, err := WriteAtomicTemp(dir, "history-*.json.tmp", MustMarshalJSON(remaining))
	if err != nil {
		return CompactionResult{}, active, archive, fmt.Errorf("active temp write failed: %w", err)
	}
	defer os.Remove(activeTmp)

	if err := os.Rename(archTmp, ap); err != nil {
		return CompactionResult{}, active, archive, fmt.Errorf("archive rename failed: %w", err)
	}
	if err := os.Rename(activeTmp, historyPath); err != nil {
		return CompactionResult{}, active, archive, fmt.Errorf("active rename failed (partial compaction — will reconcile on restart): %w", err)
	}

	// Persist final generation after full two-file commit.
	newArchive.ArchiveGeneration = newGeneration
	if saveErr := Save(ap, &newArchive); saveErr != nil {
		log.Printf("[archive] warning: failed to persist final archive_generation: %v", saveErr)
	}

	log.Printf("[archive] compaction complete: archived %d messages, generation %d", totalNewMessages, newGeneration)
	return CompactionResult{ArchivedCount: totalNewMessages}, remaining, &newArchive, nil
}

// ReconcileOnStartup detects duplicates between archive and active history.
// Active history is kept as runnable truth; duplicate messages are flagged via warnings.
func ReconcileOnStartup(archive *SessionArchive, active []client.ChatMessage) ([]client.ChatMessage, []string) {
	if archive == nil {
		return active, nil
	}
	archivedHashes := make(map[string]bool)
	for _, chunk := range archive.Chunks {
		for _, am := range chunk.Messages {
			archivedHashes[am.Hash] = true
		}
	}

	var warnings []string
	var clean []client.ChatMessage
	for _, msg := range active {
		h := HashMessage(msg)
		if archivedHashes[h] {
			warnings = append(warnings, fmt.Sprintf("duplicate message detected (hash %s) — keeping in active history, will skip re-archival", h[:8]))
		}
		clean = append(clean, msg)
	}
	return clean, warnings
}
