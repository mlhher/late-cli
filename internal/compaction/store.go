package compaction

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"late/internal/common"
	"late/internal/pathutil"
)

// RecordKindElidedSegment is the Kind of every record the elide paths
// write (reference store.py parity): a run of segments relocated out of a
// tool output or a history message.
const RecordKindElidedSegment = "elided_segment"

// OriginSourceToolPrefix prefixes the Origin.Source of records relocated
// from tool outputs: "tool:" + the tool's registry name (the pipeline's
// CompactToolOutput is the only writer on that path and always knows the
// tool it is compacting for).
const OriginSourceToolPrefix = "tool:"

// OriginSourceHistory is the Origin.Source of records relocated from
// session history by session.CompactContext: the walk scores whole
// messages and has no finer-grained ref to attribute the run to.
const OriginSourceHistory = "history"

// Origin names where a stored record's content came from (the reference
// store.py Origin). Turn is the conversation turn the content was produced
// in — turn plumbing does not exist anywhere in the port yet, so every
// writer leaves it 0 for now; the field is here so threading a turn
// through later is a one-line change per writer, not a schema migration.
type Origin struct {
	// Source is the producing surface: "tool:<name>" for tool outputs,
	// "history" for history-compaction runs.
	Source string `json:"source"`
	// Ref is an optional pointer into the source (a tool-call id, a
	// message index); empty when the writer has nothing stable to name.
	Ref string `json:"ref,omitempty"`
	// Turn is the conversation turn the content was produced in; 0 until
	// turn plumbing exists (see the type doc).
	Turn int `json:"turn"`
}

// Record is one entry of the elided-original store (the reference
// store.py Record): the full description of a relocated run, not just its
// text. ID is the pointer id the [[elided …]] line carries —
// content-addressed ("r:<8hex>", ContentID of the run text) for runs this
// pipeline produced, or a legacy "elide-<n>" counter id. The counters
// (ExpandCount/HitCount) are the expand-tool usage ledger Step 13's
// outcomes build on.
type Record struct {
	ID          string   `json:"id"`
	Text        string   `json:"text"`
	Kind        string   `json:"kind,omitempty"`
	Origin      Origin   `json:"origin"`
	Tokens      int      `json:"tokens"`
	CreatedTurn int      `json:"created_turn"`
	Summary     string   `json:"summary,omitempty"`
	SegmentIDs  []string `json:"segment_ids,omitempty"`
	ExpandCount int      `json:"expand_count"`
	HitCount    int      `json:"hit_count"`
}

// Store holds the original text of elided runs, keyed by pointer id, so the
// expand tool can retrieve what compaction removed. It is safe for
// concurrent use: the root agent and every subagent share one store.
//
// Records: the store keeps full Records — text, kind, origin, token count,
// pointer summary, contributing segment ids, and the expand/hit counters —
// not just strings. Get still returns the record's text so the legacy read
// side (tool.ExpandStore, session.ElideStore, Reconstruct) works unchanged;
// GetRecord exposes the rest (Step 13 outcomes and Step 17 retrieval read
// it). An empty Kind defaults to RecordKindElidedSegment when stored.
//
// Persistence: NewStore() is purely in-memory (tests, shadow mode — nothing
// stored there anyway). OpenStore(path) backs the store with an append-only
// JSONL file (0600, parent directories 0700): every Put/PutRecord that
// creates a record and every Touch that bumps a counter appends ONE line —
// a single Write call on an O_APPEND descriptor while holding the mutex, so
// a reader (including another late process) only ever sees whole lines,
// exactly like ShadowLog.Append. Loading is last-writer-wins per id: Touch
// re-appends the whole record rather than a delta line (a few wasted bytes
// per touch in exchange for one append path), and content-addressed ids
// make duplicate puts the exception. Lines that fail to parse are skipped,
// so a torn trailing line — the only damage a crash mid-append can leave —
// cannot poison the records written before it (OpenStore also terminates a
// torn tail so the next append cannot weld itself onto it).
//
// Ids are content-addressed for runs produced by this pipeline
// (ContentID, "r:<8hex>"); the legacy counter (NextID, "elide-<n>") remains
// only so pointers minted by older builds still resolve — on a reloaded
// store the counter resumes past every legacy id already on disk, keeping
// the minted keys distinct. Put is idempotent: storing under an id that
// already exists keeps the first record and appends nothing — two
// compactions of identical text share one record instead of duplicating it,
// and legacy ids, being unique per NextID call, never collide anyway.
//
// Persistence is best-effort by contract: an append failure leaves the
// in-memory record intact (the session keeps working; only cross-restart
// persistence for that record is lost) and is not reported — the no-error
// shape matches Put's existing signature, and open-time failures are where
// callers warn and degrade to the in-memory store. The backing file is
// deliberately never fsynced and no descriptor is held between appends
// (each persist opens, writes, and closes): a process crash can lose at
// most the records of the appends still in the OS page cache, and the
// torn-tail repair below covers the mid-line residue. Durability against a
// machine crash is accepted debt for an append-only JSONL log of
// re-retrievable originals — exactly the trade ShadowLog.Append makes.
type Store struct {
	mu      sync.Mutex
	records map[string]*Record
	// order lists the ids in first-appearance order, so Records() is
	// deterministic (map iteration is not; last-writer-wins loads keep the
	// first position).
	order []string
	// next backs the legacy NextID counter.
	next int
	// path is the JSONL backing file; "" is in-memory only.
	path string
	// shadow is the outcome log the expand tool attributes expand outcomes
	// through (Step 13): wiring attaches the session's shadow log with
	// WithShadowLog, and the expand tool — the only expand path — reads it
	// back to append one outcome per record id and per contributing segment
	// id. nil (the default) disables outcome logging: the expand tool skips
	// it silently.
	shadow *ShadowLog
}

// NewStore returns an empty in-memory original-text store.
func NewStore() *Store {
	return &Store{records: make(map[string]*Record)}
}

// DefaultStorePath returns the record store location:
// ~/.local/share/late/compaction-store.jsonl, resolved through
// pathutil.LateDataDir (mirroring DefaultShadowPath; Windows keeps everything
// under the config dir).
func DefaultStorePath() (string, error) {
	dir, err := pathutil.LateDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "compaction-store.jsonl"), nil
}

// OpenStore opens the record store at path, loading previously persisted
// records (last-writer-wins per id) so [[elided …]] pointers in resumed
// sessions still resolve after a restart. The parent directory is created
// 0700 if missing; the file itself is created 0600 on first append. A
// missing file is an empty store, not an error (nothing persisted yet); an
// empty path keeps the store in-memory (NewStore). Callers that cannot
// afford a failure degrade to NewStore() with a warning.
func OpenStore(path string) (*Store, error) {
	if path == "" {
		return NewStore(), nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("compaction: create store dir %s: %w", dir, err)
	}
	s := NewStore()
	s.path = path
	s.repairTornTail()
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Path returns the backing file path ("" when the store is in-memory).
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// WithShadowLog attaches l as the store's outcome log and returns the store
// for call-site chaining: the expand tool reads it back through ShadowLog
// and appends one "expand" outcome per record id and per contributing
// segment id on every retrieval — the reference pipeline.py expand()
// attribution that ShadowLog.FalseNegativeRate and the replay table's
// still-missed column are built on. A nil log (or a nil store) is accepted
// and simply disables outcome logging, mirroring how a nil shadow log
// disables decision logging on the pipeline. Wiring calls this once at
// startup, before any agent can run a tool call.
func (s *Store) WithShadowLog(l *ShadowLog) *Store {
	if s == nil {
		return s
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shadow = l
	return s
}

// ShadowLog returns the store's attached outcome log, or nil when none was
// set (the expand tool skips outcome logging then).
func (s *Store) ShadowLog() *ShadowLog {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shadow
}

// Get returns the original text stored for id — the record's Text field, so
// the pre-Record call sites (the expand tool, Reconstruct, the session's
// ElideStore read side) keep working unchanged. A nil store — or an unknown
// id — reports ("", false); the expand tool turns the miss into an
// "unknown elided id" error result.
func (s *Store) Get(id string) (string, bool) {
	rec, ok := s.GetRecord(id)
	if !ok {
		return "", false
	}
	return rec.Text, true
}

// GetRecord returns a snapshot of the record stored for id. The returned
// pointer names a copy (including the segment-id slice), so callers can
// read it without racing Touch's counter updates. A nil store — or an
// unknown id — reports (nil, false).
func (s *Store) GetRecord(id string) (*Record, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return nil, false
	}
	snap := *rec
	snap.SegmentIDs = cloneStrings(rec.SegmentIDs)
	return &snap, true
}

// Records returns snapshots of every record in first-appearance order (a
// nil store yields nil). Like GetRecord, each pointer names a copy safe to
// read without the lock.
func (s *Store) Records() []*Record {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Record, 0, len(s.order))
	for _, id := range s.order {
		rec, ok := s.records[id]
		if !ok {
			continue // records and order move together; defensive only
		}
		snap := *rec
		snap.SegmentIDs = cloneStrings(rec.SegmentIDs)
		out = append(out, &snap)
	}
	return out
}

// Put stores text under id. Idempotent: an existing id keeps its first
// record (see the Store doc). A nil store is a no-op. The synthesized
// record carries the default kind, an estimated token count, and the
// pointer summary — Put is the metadata-free legacy entry point; writers
// that know more (the pipeline and the history walk) use PutRecord. An
// empty id or empty text is a no-op: an idless record is unreachable through
// any pointer, and an empty original has nothing to reconstruct — storing
// either would only pollute the digest and the record order.
func (s *Store) Put(id, text string) {
	if s == nil || id == "" || text == "" {
		return
	}
	s.PutRecord(Record{
		ID:      id,
		Text:    text,
		Tokens:  common.EstimateTokenCount(text),
		Summary: Summarise(text, SummaryMaxChars),
	})
}

// PutRecord stores rec under rec.ID — the origin-threaded write side. The
// pipeline's CompactToolOutput writes Origin{Source: "tool:<name>"} with
// the run's token count, summary, and contributing segment ids; the
// session's history walk writes Origin{Source: "history"}. Idempotent like
// Put: an existing id keeps its first record and appends nothing. A record
// with an empty id or empty text is a no-op (see Put). A nil store is a
// no-op.
func (s *Store) PutRecord(rec Record) {
	if s == nil || rec.ID == "" || rec.Text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if stored, inserted := s.upsertLocked(rec, false); inserted {
		s.persistLocked(*stored)
	}
}

// Touch bumps an existing record's counters — expand when the expand tool
// retrieved the original, hit when the content was used after retrieval
// (the reference store's touch; Step 13's expand outcomes build on these).
// It reports whether the record exists; a nil store or unknown id is a
// no-op. With both flags false it is a pure existence check. The updated
// record is re-appended whole when the store is file-backed
// (last-writer-wins on load, so counters survive restarts) — the simplest
// crash-atomic update, documented on the Store type.
func (s *Store) Touch(id string, expand, hit bool) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return false
	}
	if expand {
		rec.ExpandCount++
	}
	if hit {
		rec.HitCount++
	}
	if expand || hit {
		s.persistLocked(*rec)
	}
	return true
}

// Len reports how many records the store holds (tests and diagnostics).
func (s *Store) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

// NextID mints the next legacy elide-pointer id ("elide-<n>", 1-based).
// New pointers are content-addressed and never mint ids; the counter exists
// only so pre-content-id stores keep minting distinct legacy keys — on a
// reloaded file-backed store it resumes past every legacy id already on
// disk. A nil store returns "".
func (s *Store) NextID() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	return fmt.Sprintf("elide-%d", s.next)
}

// upsertLocked inserts rec when its id is new (copying it, with its
// segment ids, into the store and returning the stored record and true) or
// — with lastWriterWins — replaces the existing record in place (keeping
// its order position; returning false, nothing new to persist). Without
// lastWriterWins an existing id keeps its first record (Put idempotency).
// Caller holds mu.
func (s *Store) upsertLocked(rec Record, lastWriterWins bool) (*Record, bool) {
	if existing, ok := s.records[rec.ID]; ok {
		if !lastWriterWins {
			return existing, false
		}
		*existing = rec
		return existing, false
	}
	if rec.Kind == "" {
		rec.Kind = RecordKindElidedSegment
	}
	stored := rec
	stored.SegmentIDs = cloneStrings(rec.SegmentIDs)
	s.records[rec.ID] = &stored
	s.order = append(s.order, rec.ID)
	return &stored, true
}

// persistLocked appends rec as one JSON line to the backing file. Caller
// holds mu. One marshal, one OpenFile (O_CREATE|O_WRONLY|O_APPEND, 0600),
// one Write of the whole line — the ShadowLog.Append crash-atomic shape:
// concurrent processes interleave whole lines, and a crash leaves either a
// complete line or a torn trailing one that loads skip.
func (s *Store) persistLocked(rec Record) {
	if s.path == "" {
		return
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return // Record fields are all JSON-native; defensive only
	}
	line = append(line, '\n')
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(line)
}

// repairTornTail terminates a torn trailing line — the residue of a crash
// mid-append — with a newline, so the next append starts on a fresh line
// instead of welding a good record onto the unparsable tail (the torn bytes
// stay on their own skipped line). A no-op when the file ends cleanly.
func (s *Store) repairTornTail() {
	f, err := os.OpenFile(s.path, os.O_RDWR, 0o600)
	if err != nil {
		return // missing file: nothing to repair
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return
	}
	last := make([]byte, 1)
	if _, err := f.ReadAt(last, st.Size()-1); err != nil {
		return
	}
	if last[0] != '\n' {
		if _, err := f.WriteAt([]byte{'\n'}, st.Size()); err != nil {
			return
		}
	}
}

// load reads the backing file into memory: one JSON record per line,
// last-writer-wins per id (Touch re-appends whole records, so a later line
// carries newer counters). Malformed or id-less lines are skipped — a torn
// trailing line must not poison the records before it. Legacy "elide-<n>"
// ids advance the NextID counter so minted keys stay distinct from loaded
// ones.
func (s *Store) load() error {
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // first run: nothing persisted yet
		}
		return fmt.Errorf("compaction: open store %s: %w", s.path, err)
	}
	defer f.Close()

	// Records carry full run text (a giant tool output is a legal record),
	// so lines have no fixed bound; bufio.Reader grows per line as needed
	// and the last line may lack its newline (EOF returns it whole).
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		line, rerr := r.ReadString('\n')
		if rec, ok := parseRecordLine(line); ok {
			s.mu.Lock()
			s.upsertLocked(rec, true)
			if n, ok := legacyElideNumber(rec.ID); ok && n > s.next {
				s.next = n
			}
			s.mu.Unlock()
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return nil
			}
			return fmt.Errorf("compaction: read store %s: %w", s.path, rerr)
		}
	}
}

// parseRecordLine decodes one JSONL record line; blank and malformed lines
// (torn appends) report false.
func parseRecordLine(line string) (Record, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return Record{}, false
	}
	var rec Record
	if err := json.Unmarshal([]byte(line), &rec); err != nil || rec.ID == "" {
		return Record{}, false
	}
	return rec, true
}

// legacyElideNumber parses the counter of a legacy "elide-<n>" id; false
// for content ids and anything else.
func legacyElideNumber(id string) (int, bool) {
	rest, ok := strings.CutPrefix(id, "elide-")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// cloneStrings copies a string slice (nil stays nil).
func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string(nil), in...)
}
