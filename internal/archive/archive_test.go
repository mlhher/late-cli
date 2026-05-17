package archive

import (
	"encoding/json"
	"late/internal/client"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// ---- helpers ----

func makeMsg(role, content string) client.ChatMessage {
	return client.ChatMessage{Role: role, Content: content}
}

func makeHistory(n int) []client.ChatMessage {
	msgs := make([]client.ChatMessage, n)
	for i := range msgs {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs[i] = client.ChatMessage{Role: role, Content: "message " + string(rune('A'+i))}
	}
	return msgs
}

func sampleArchive(sessionID string) *SessionArchive {
	now := time.Now().UTC()
	arch := New(sessionID)
	arch.NextSequence = 2
	arch.ArchivedMessageCount = 2
	arch.Chunks = []ArchiveChunk{
		{
			ChunkID:       "chunk-1",
			StartSequence: 0,
			EndSequence:   1,
			CreatedAt:     now,
			Messages: []ArchivedMessage{
				{
					MessageID:  "msg-0",
					Sequence:   0,
					Role:       "user",
					Hash:       HashMessage(makeMsg("user", "hello")),
					ArchivedAt: now,
					Message:    makeMsg("user", "hello"),
				},
				{
					MessageID:  "msg-1",
					Sequence:   1,
					Role:       "assistant",
					Hash:       HashMessage(makeMsg("assistant", "world")),
					ArchivedAt: now,
					Message:    makeMsg("assistant", "world"),
				},
			},
		},
	}
	return arch
}

func defaultCompactionCfg() CompactionConfig {
	return CompactionConfig{
		ThresholdMessages:  10,
		KeepRecentMessages: 3,
		ChunkSize:          4,
		StaleAfterSeconds:  300,
	}
}

// ---- Phase 2: persistence tests ----

// TestSave_FilePermissions verifies that Save() creates the archive file with mode 0600.
func TestSave_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-perm.archive.json")
	arch := sampleArchive("perm")
	if err := Save(path, arch); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("expected file mode 0600, got %04o", got)
	}
}

func TestArchiveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-abc.archive.json")

	arch := sampleArchive("abc")
	if err := Save(path, arch); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path, "abc")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.SessionID != arch.SessionID {
		t.Fatalf("SessionID = %q, want %q", loaded.SessionID, arch.SessionID)
	}
	if len(loaded.Chunks) != 1 {
		t.Fatalf("Chunks len = %d, want 1", len(loaded.Chunks))
	}
	if len(loaded.Chunks[0].Messages) != 2 {
		t.Fatalf("Messages len = %d, want 2", len(loaded.Chunks[0].Messages))
	}
	if loaded.Chunks[0].Messages[0].Role != "user" {
		t.Fatalf("first message role = %q, want user", loaded.Chunks[0].Messages[0].Role)
	}
}

func TestLoad_Missing(t *testing.T) {
	dir := t.TempDir()
	arch, err := Load(filepath.Join(dir, "no-such.archive.json"), "xyz")
	if err != nil {
		t.Fatalf("expected no error for missing archive, got: %v", err)
	}
	if arch == nil || len(arch.Chunks) != 0 {
		t.Fatal("expected empty archive")
	}
}

func TestLoad_Corrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.archive.json")
	if err := os.WriteFile(path, []byte(`{not valid`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, "s")
	if err == nil {
		t.Fatal("expected error for corrupt archive")
	}
}

func TestLoad_VersionMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ver.archive.json")
	data, _ := json.Marshal(map[string]any{
		"session_id":     "s",
		"schema_version": 99,
		"chunks":         []any{},
	})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, "s")
	if err == nil {
		t.Fatal("expected error for schema version mismatch")
	}
}

func TestSave_AtomicCleanup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-abc.archive.json")
	if err := Save(path, sampleArchive("abc")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("stray temp file: %s", e.Name())
		}
	}
}

func TestDeleteFiles(t *testing.T) {
	dir := t.TempDir()
	histPath := filepath.Join(dir, "session-del.json")
	for _, p := range []string{ArchivePath(histPath), LockPath(histPath)} {
		if err := os.WriteFile(p, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := DeleteFiles(histPath); err != nil {
		t.Fatalf("DeleteFiles: %v", err)
	}
	for _, p := range []string{ArchivePath(histPath), LockPath(histPath)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be deleted", p)
		}
	}
}

func TestDeleteFiles_MissingIsOK(t *testing.T) {
	dir := t.TempDir()
	if err := DeleteFiles(filepath.Join(dir, "session-gone.json")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReconstruct(t *testing.T) {
	arch := sampleArchive("abc")
	active := []client.ChatMessage{makeMsg("user", "third"), makeMsg("assistant", "fourth")}
	full := Reconstruct(arch, active)
	if len(full) != 4 {
		t.Fatalf("reconstructed %d messages, want 4", len(full))
	}
	if full[0].Content != "hello" {
		t.Fatalf("full[0].Content = %q, want hello", full[0].Content)
	}
	if full[2].Content != "third" {
		t.Fatalf("full[2].Content = %q, want third", full[2].Content)
	}
}

func TestReconstruct_NilArchive(t *testing.T) {
	active := []client.ChatMessage{makeMsg("user", "hi")}
	full := Reconstruct(nil, active)
	if len(full) != 1 || full[0].Content != "hi" {
		t.Fatal("expected unchanged active history")
	}
}

func TestArchivePath_JsonSuffix(t *testing.T) {
	if got := ArchivePath("/s/session-abc.json"); got != "/s/session-abc.archive.json" {
		t.Fatalf("ArchivePath = %q", got)
	}
}

func TestArchivePath_NonJsonSuffix(t *testing.T) {
	if got := ArchivePath("/s/session-abc.dat"); got != "/s/session-abc.dat.archive.json" {
		t.Fatalf("ArchivePath = %q", got)
	}
}

func TestLockPath_JsonSuffix(t *testing.T) {
	if got := LockPath("/s/session-abc.json"); got != "/s/session-abc.archive.lock" {
		t.Fatalf("LockPath = %q", got)
	}
}

func TestHashMessage_Stable(t *testing.T) {
	msg := makeMsg("user", "hello world")
	h1 := HashMessage(msg)
	h2 := HashMessage(msg)
	if h1 != h2 || h1 == "" {
		t.Fatalf("hash unstable or empty")
	}
}

// ---- Phase 3: compaction tests ----

func TestCompact_UnderThreshold(t *testing.T) {
	dir := t.TempDir()
	histPath := filepath.Join(dir, "session-t.json")
	active := makeHistory(5)
	arch := New("t")
	res, newActive, _, err := Compact(histPath, "t", active, arch, defaultCompactionCfg())
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if !res.NoOp {
		t.Fatal("expected NoOp=true")
	}
	if len(newActive) != len(active) {
		t.Fatalf("active unchanged: got %d, want %d", len(newActive), len(active))
	}
}

func TestCompact_OverThreshold(t *testing.T) {
	dir := t.TempDir()
	histPath := filepath.Join(dir, "session-t.json")
	active := makeHistory(15)
	if err := saveHistoryHelper(histPath, active); err != nil {
		t.Fatal(err)
	}
	arch := New("t")
	res, newActive, newArch, err := Compact(histPath, "t", active, arch, defaultCompactionCfg())
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if res.NoOp {
		t.Fatal("expected compaction to run")
	}
	if len(newActive) != 3 {
		t.Fatalf("newActive = %d, want 3", len(newActive))
	}
	if res.ArchivedCount != 12 {
		t.Fatalf("ArchivedCount = %d, want 12", res.ArchivedCount)
	}
	if len(newArch.Chunks) == 0 {
		t.Fatal("expected non-empty chunks")
	}
}

func TestCompact_Idempotent(t *testing.T) {
	dir := t.TempDir()
	histPath := filepath.Join(dir, "session-t.json")
	active := makeHistory(15)
	if err := saveHistoryHelper(histPath, active); err != nil {
		t.Fatal(err)
	}
	_, newActive, newArch, err := Compact(histPath, "t", active, New("t"), defaultCompactionCfg())
	if err != nil {
		t.Fatal(err)
	}
	res2, _, _, err := Compact(histPath, "t", newActive, newArch, defaultCompactionCfg())
	if err != nil {
		t.Fatal(err)
	}
	if !res2.NoOp {
		t.Fatal("expected second compaction to be no-op")
	}
}

func TestCompact_LastNUnchanged(t *testing.T) {
	dir := t.TempDir()
	histPath := filepath.Join(dir, "session-t.json")
	active := makeHistory(15)
	if err := saveHistoryHelper(histPath, active); err != nil {
		t.Fatal(err)
	}
	_, newActive, _, err := Compact(histPath, "t", active, New("t"), defaultCompactionCfg())
	if err != nil {
		t.Fatal(err)
	}
	origLast := active[len(active)-3:]
	for i, msg := range newActive {
		if msg.Content != origLast[i].Content {
			t.Fatalf("newActive[%d].Content = %q, want %q", i, msg.Content, origLast[i].Content)
		}
	}
}

func TestCompact_DuplicatePrevention(t *testing.T) {
	dir := t.TempDir()
	histPath := filepath.Join(dir, "session-t.json")
	active := makeHistory(15)
	if err := saveHistoryHelper(histPath, active); err != nil {
		t.Fatal(err)
	}
	_, firstActive, firstArch, err := Compact(histPath, "t", active, New("t"), defaultCompactionCfg())
	if err != nil {
		t.Fatal(err)
	}
	extra := makeHistory(12)
	secondActive := append(firstActive, extra...)
	if err := saveHistoryHelper(histPath, secondActive); err != nil {
		t.Fatal(err)
	}
	_, _, secondArch, err := Compact(histPath, "t", secondActive, firstArch, defaultCompactionCfg())
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, chunk := range secondArch.Chunks {
		for _, am := range chunk.Messages {
			if seen[am.Hash] {
				t.Fatalf("duplicate hash in archive: %s", am.Hash[:8])
			}
			seen[am.Hash] = true
		}
	}
}

func TestCompact_SequenceProgression(t *testing.T) {
	dir := t.TempDir()
	histPath := filepath.Join(dir, "session-t.json")
	active := makeHistory(15)
	if err := saveHistoryHelper(histPath, active); err != nil {
		t.Fatal(err)
	}
	_, firstActive, firstArch, err := Compact(histPath, "t", active, New("t"), defaultCompactionCfg())
	if err != nil {
		t.Fatal(err)
	}
	var maxSeq int64 = -1
	for _, chunk := range firstArch.Chunks {
		for _, am := range chunk.Messages {
			if am.Sequence > maxSeq {
				maxSeq = am.Sequence
			}
		}
	}
	extra := makeHistory(12)
	secondActive := append(firstActive, extra...)
	if err := saveHistoryHelper(histPath, secondActive); err != nil {
		t.Fatal(err)
	}
	_, _, secondArch, err := Compact(histPath, "t", secondActive, firstArch, defaultCompactionCfg())
	if err != nil {
		t.Fatal(err)
	}
	if secondArch.NextSequence <= maxSeq+1 {
		t.Fatalf("next_sequence %d should be > %d", secondArch.NextSequence, maxSeq)
	}
}

func TestCompact_ReconstructionOrdering(t *testing.T) {
	dir := t.TempDir()
	histPath := filepath.Join(dir, "session-t.json")
	active := makeHistory(15)
	if err := saveHistoryHelper(histPath, active); err != nil {
		t.Fatal(err)
	}
	_, newActive, newArch, err := Compact(histPath, "t", active, New("t"), defaultCompactionCfg())
	if err != nil {
		t.Fatal(err)
	}
	reconstructed := Reconstruct(newArch, newActive)
	if len(reconstructed) != len(active) {
		t.Fatalf("reconstructed %d messages, want %d", len(reconstructed), len(active))
	}
	for i, msg := range reconstructed {
		if msg.Content != active[i].Content {
			t.Fatalf("reconstructed[%d].Content = %q, want %q", i, msg.Content, active[i].Content)
		}
	}
}

func TestCompact_GenerationIncrement(t *testing.T) {
	dir := t.TempDir()
	histPath := filepath.Join(dir, "session-t.json")
	active := makeHistory(15)
	if err := saveHistoryHelper(histPath, active); err != nil {
		t.Fatal(err)
	}
	_, _, newArch, err := Compact(histPath, "t", active, New("t"), defaultCompactionCfg())
	if err != nil {
		t.Fatal(err)
	}
	if newArch.ArchiveGeneration != 1 {
		t.Fatalf("ArchiveGeneration = %d, want 1", newArch.ArchiveGeneration)
	}
}

func TestReconcileOnStartup(t *testing.T) {
	msg := makeMsg("user", "duplicate message")
	arch := New("s")
	now := time.Now().UTC()
	arch.Chunks = []ArchiveChunk{{
		ChunkID: "chunk-0",
		Messages: []ArchivedMessage{
			{MessageID: "msg-0", Sequence: 0, Role: "user", Hash: HashMessage(msg), ArchivedAt: now, Message: msg},
		},
	}}
	active := []client.ChatMessage{msg, makeMsg("user", "new message")}
	clean, warnings := ReconcileOnStartup(arch, active)
	if len(warnings) == 0 {
		t.Fatal("expected warnings for duplicate message")
	}
	if len(clean) != 2 {
		t.Fatalf("clean = %d messages, want 2", len(clean))
	}
}

func TestCompact_LockHeld(t *testing.T) {
	dir := t.TempDir()
	histPath := filepath.Join(dir, "session-lock.json")
	active := makeHistory(15)
	if err := saveHistoryHelper(histPath, active); err != nil {
		t.Fatal(err)
	}
	lp := LockPath(histPath)
	pid := os.Getpid()
	lockContent := []byte(`{"pid":` + itoa(pid) + `,"created_at":"2099-01-01T00:00:00Z","session_id":"lock"}`)
	if err := os.WriteFile(lp, lockContent, 0600); err != nil {
		t.Fatal(err)
	}
	res, _, _, err := Compact(histPath, "lock", active, New("lock"), defaultCompactionCfg())
	if err != nil {
		t.Fatalf("Compact with held lock: %v", err)
	}
	if !res.LockHeld {
		t.Fatal("expected LockHeld=true")
	}
}

// ---- Phase 4: search tests ----

func buildTestArchive() *SessionArchive {
	now := time.Now().UTC()
	msgs := []struct{ role, content string }{
		{"user", "How do I configure the network adapter?"},
		{"assistant", "You can use the netctl tool to configure adapters."},
		{"tool", "netctl list output: eth0 wlan0"},
		{"user", "What about the firewall rules?"},
		{"assistant", "Use iptables or nftables for firewall configuration."},
	}
	arch := New("test-session")
	var amList []ArchivedMessage
	for i, m := range msgs {
		msg := client.ChatMessage{Role: m.role, Content: m.content}
		am := ArchivedMessage{
			MessageID:  chunkIDStr(1, i),
			Sequence:   int64(i),
			Role:       m.role,
			Hash:       HashMessage(msg),
			ArchivedAt: now,
			Message:    msg,
		}
		amList = append(amList, am)
	}
	arch.Chunks = []ArchiveChunk{{
		ChunkID:       "chunk-1-0",
		StartSequence: 0,
		EndSequence:   4,
		Messages:      amList,
		CreatedAt:     now,
	}}
	arch.ArchivedMessageCount = len(amList)
	arch.NextSequence = int64(len(amList))
	return arch
}

func TestSearch_CaseInsensitive(t *testing.T) {
	svc := NewSearchService(buildTestArchive())
	results := svc.Search("NETWORK", 10, false)
	if len(results) == 0 {
		t.Fatal("expected results for case-insensitive 'NETWORK'")
	}
}

func TestSearch_CaseSensitive(t *testing.T) {
	svc := NewSearchService(buildTestArchive())
	if len(svc.Search("NETWORK", 10, true)) > 0 {
		t.Fatal("case-sensitive 'NETWORK' should not match lowercase content")
	}
	if len(svc.Search("network", 10, true)) == 0 {
		t.Fatal("case-sensitive 'network' should match")
	}
}

func TestSearch_EmptyQuery(t *testing.T) {
	svc := NewSearchService(buildTestArchive())
	if len(svc.Search("", 10, false)) != 0 {
		t.Fatal("expected 0 results for empty query")
	}
}

func TestSearch_EmptyArchive(t *testing.T) {
	svc := NewSearchService(New("empty"))
	if len(svc.Search("network", 10, false)) != 0 {
		t.Fatal("expected 0 results for empty archive")
	}
}

func TestSearch_MaxResultsCap(t *testing.T) {
	svc := NewSearchService(buildTestArchive())
	results := svc.Search("e", 2, false)
	if len(results) > 2 {
		t.Fatalf("expected <= 2 results, got %d", len(results))
	}
}

func TestSearch_ScoringDeterminism(t *testing.T) {
	arch := buildTestArchive()
	r1 := NewSearchService(arch).Search("network adapter", 10, false)
	r2 := NewSearchService(arch).Search("network adapter", 10, false)
	if len(r1) != len(r2) {
		t.Fatalf("result count differs: %d vs %d", len(r1), len(r2))
	}
	for i := range r1 {
		if r1[i].MessageID != r2[i].MessageID {
			t.Fatalf("result[%d] differs: %q vs %q", i, r1[i].MessageID, r2[i].MessageID)
		}
	}
}

func TestSearch_LazyIndex(t *testing.T) {
	svc := NewSearchService(buildTestArchive())
	svc.mu.Lock()
	built := svc.built
	svc.mu.Unlock()
	if built {
		t.Fatal("index should not be built before first search")
	}
	_ = svc.Search("network", 10, false)
	svc.mu.Lock()
	built = svc.built
	svc.mu.Unlock()
	if !built {
		t.Fatal("index should be built after first search")
	}
}

func TestSearch_DirtyRebuild(t *testing.T) {
	svc := NewSearchService(buildTestArchive())
	_ = svc.Search("network", 10, false)
	svc.MarkDirty()
	svc.mu.Lock()
	dirty := svc.dirty
	svc.mu.Unlock()
	if !dirty {
		t.Fatal("expected dirty=true after MarkDirty")
	}
	_ = svc.Search("network", 10, false)
	svc.mu.Lock()
	dirty = svc.dirty
	svc.mu.Unlock()
	if dirty {
		t.Fatal("expected dirty=false after rebuild")
	}
}

func TestSearch_SessionIsolation(t *testing.T) {
	r1 := NewSearchService(buildTestArchive()).Search("network", 10, false)
	r2 := NewSearchService(New("other")).Search("network", 10, false)
	if len(r2) > 0 {
		t.Fatal("empty archive should return no results")
	}
	if len(r1) == 0 {
		t.Fatal("expected results from non-empty archive")
	}
}

func TestSearch_TokenScoringOrder(t *testing.T) {
	results := NewSearchService(buildTestArchive()).Search("configure firewall", 10, false)
	for i := 1; i < len(results); i++ {
		if results[i].Score > results[i-1].Score {
			t.Fatalf("results not sorted descending by score at index %d", i)
		}
	}
}

// TestSearch_ReasoningContentNotIndexed verifies ReasoningContent is excluded from index.
func TestSearch_ReasoningContentNotIndexed(t *testing.T) {
	now := time.Now().UTC()
	secretThought := "secret_reasoning_token_xyz"
	msg := client.ChatMessage{
		Role:             "assistant",
		Content:          "Here is my answer.",
		ReasoningContent: secretThought,
	}
	am := ArchivedMessage{
		MessageID:  "msg-r",
		Sequence:   0,
		Role:       "assistant",
		Hash:       HashMessage(msg),
		ArchivedAt: now,
		Message:    msg,
	}
	arch := New("reasoning-test")
	arch.Chunks = []ArchiveChunk{{ChunkID: "chunk-r", Messages: []ArchivedMessage{am}}}
	svc := NewSearchService(arch)
	results := svc.Search(secretThought, 10, false)
	if len(results) != 0 {
		t.Fatalf("reasoning_content should not be indexed; got %d result(s)", len(results))
	}
	// Visible content should still be searchable.
	if len(svc.Search("answer", 10, false)) == 0 {
		t.Fatal("visible content should be indexed")
	}
}

// TestCompact_StaleLockRecovery verifies that an expired lock is removed and compaction proceeds.
func TestCompact_StaleLockRecovery(t *testing.T) {
	dir := t.TempDir()
	histPath := filepath.Join(dir, "session-stale.json")
	active := makeHistory(15)
	if err := saveHistoryHelper(histPath, active); err != nil {
		t.Fatal(err)
	}
	lp := LockPath(histPath)
	// Write a lock owned by a non-existent PID with a created_at that's old enough.
	// We can't control ModTime via JSON content, but we can set a stale timeout of 0
	// so that any existing lock is immediately considered stale.
	lockContent := []byte(`{"pid":999999999,"created_at":"2000-01-01T00:00:00Z","session_id":"stale"}`)
	if err := os.WriteFile(lp, lockContent, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := defaultCompactionCfg()
	cfg.StaleAfterSeconds = 1 // tiny threshold so ModTime check is "stale"

	// Backdate the lock file mtime to guarantee staleness.
	staleTime := time.Now().Add(-2 * time.Second)
	if err := os.Chtimes(lp, staleTime, staleTime); err != nil {
		t.Fatal(err)
	}

	res, _, _, err := Compact(histPath, "stale", active, New("stale"), cfg)
	if err != nil {
		t.Fatalf("Compact with stale lock: %v", err)
	}
	if res.LockHeld {
		t.Fatal("stale lock should have been recovered; expected LockHeld=false")
	}
	if res.NoOp {
		t.Fatal("expected compaction to run after stale lock recovery")
	}
}

// TestCompact_CounterProgression verifies CompactionCount increments on each non-no-op compaction.
func TestCompact_CounterProgression(t *testing.T) {
	dir := t.TempDir()
	hist := filepath.Join(dir, "session-cp.json")

	mkActive := func(prefix string) []client.ChatMessage {
		msgs := make([]client.ChatMessage, 15)
		for i := range msgs {
			role := "user"
			if i%2 == 1 {
				role = "assistant"
			}
			msgs[i] = client.ChatMessage{Role: role, Content: prefix + "-" + itoa(i)}
		}
		return msgs
	}

	active := mkActive("r1")
	if err := saveHistoryHelper(hist, active); err != nil {
		t.Fatal(err)
	}
	arch := New("cp")
	var err error
	_, active, arch, err = Compact(hist, "cp", active, arch, defaultCompactionCfg())
	if err != nil {
		t.Fatal(err)
	}
	if arch.CompactionCount != 1 {
		t.Fatalf("after cycle 1: CompactionCount=%d, want 1", arch.CompactionCount)
	}

	extra := mkActive("r2")
	active = append(active, extra...)
	if err := saveHistoryHelper(hist, active); err != nil {
		t.Fatal(err)
	}
	_, _, arch, err = Compact(hist, "cp", active, arch, defaultCompactionCfg())
	if err != nil {
		t.Fatal(err)
	}
	if arch.CompactionCount != 2 {
		t.Fatalf("after cycle 2: CompactionCount=%d, want 2", arch.CompactionCount)
	}
}

// TestCompact_MultiCycleLossless verifies lossless reconstruction across multiple compaction cycles.
func TestCompact_MultiCycleLossless(t *testing.T) {
	dir := t.TempDir()
	histPath := filepath.Join(dir, "session-mc.json")

	makeDistinct := func(prefix string, n int) []client.ChatMessage {
		msgs := make([]client.ChatMessage, n)
		for i := range msgs {
			role := "user"
			if i%2 == 1 {
				role = "assistant"
			}
			msgs[i] = client.ChatMessage{Role: role, Content: prefix + "-msg-" + itoa(i)}
		}
		return msgs
	}

	// Cycle 1: first batch.
	active := makeDistinct("cycle1", 15)
	var canonical []client.ChatMessage
	canonical = append(canonical, active...)
	if err := saveHistoryHelper(histPath, active); err != nil {
		t.Fatal(err)
	}
	arch := New("mc")
	var err error
	_, active, arch, err = Compact(histPath, "mc", active, arch, defaultCompactionCfg())
	if err != nil {
		t.Fatal("cycle 1:", err)
	}
	reconstructed := Reconstruct(arch, active)
	if len(reconstructed) != len(canonical) {
		t.Fatalf("cycle 1: reconstructed %d, want %d", len(reconstructed), len(canonical))
	}

	// Cycle 2: distinct second batch.
	extra := makeDistinct("cycle2", 12)
	canonical = append(canonical, extra...)
	active = append(active, extra...)
	if err := saveHistoryHelper(histPath, active); err != nil {
		t.Fatal(err)
	}
	_, active, arch, err = Compact(histPath, "mc", active, arch, defaultCompactionCfg())
	if err != nil {
		t.Fatal("cycle 2:", err)
	}
	reconstructed = Reconstruct(arch, active)
	if len(reconstructed) != len(canonical) {
		t.Fatalf("cycle 2: reconstructed %d, want %d", len(reconstructed), len(canonical))
	}
	for i, msg := range reconstructed {
		if msg.Content != canonical[i].Content {
			t.Fatalf("cycle 2 reconstructed[%d].Content = %q, want %q", i, msg.Content, canonical[i].Content)
		}
	}

	// Cycle 3: no new messages above threshold → no-op.
	res, _, _, err := Compact(histPath, "mc", active, arch, defaultCompactionCfg())
	if err != nil {
		t.Fatal("cycle 3:", err)
	}
	if !res.NoOp {
		t.Fatal("cycle 3 should be no-op")
	}
}

// ---- helpers ----

func saveHistoryHelper(path string, msgs []client.ChatMessage) error {
	data, err := json.MarshalIndent(msgs, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "history-*.json.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
