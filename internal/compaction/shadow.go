package compaction

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"late/internal/pathutil"
)

// DecisionKeep is the only decision the shadow-only stage records: every
// scored segment is kept; elision comes with the relocation stage.
const DecisionKeep = "keep"

// Entry types (ShadowEntry.Type). An empty Type is the legacy spelling of
// EntryTypeDecision: every line written before the field existed is a
// per-segment decision.
const (
	EntryTypeDecision   = "decision"
	EntryTypeHistoryRun = "history-run"
	// EntryTypeTripwire marks a gate-tripwire override: the scorer wanted to
	// elide past GateConfig.MaxElideFraction, so nothing was elided. The
	// entry carries Action "tripwire" and the output's total token count;
	// it is not a per-segment decision and Replay skips it.
	EntryTypeTripwire = "tripwire"
	// EntryTypeExpand marks an expand outcome: a stored record whose original
	// the agent fetched back through the expand tool. Every expand is a
	// recorded false negative of the elision decision that relocated the
	// record's content — the input to FalseNegativeRate and the replay
	// table's still-missed column. One outcome names the record id (ItemID);
	// one outcome per contributing segment id accompanies it (the reference
	// pipeline.py expand() attribution).
	EntryTypeExpand = "expand"
	// EntryTypeHit marks a hit outcome: a record whose content was used
	// after retrieval (the reference store's mark_hits). ItemID names the
	// record or segment the hit attributes to.
	EntryTypeHit = "hit"
)

// DecisionKindAdmit and DecisionKindRetrieve are the ShadowEntry.Kind
// decision classes (the reference shadow.py DecisionKind): "admit" — the
// per-segment elision decisions the scoring pipeline records — and
// "retrieve" — the store read side's injected/skipped decisions (Step 17).
const (
	DecisionKindAdmit    = "admit"
	DecisionKindRetrieve = "retrieve"
)

// ShadowEntry is one JSONL line in the shadow log: a single scored segment
// at a single decision point — an outcome line (Type "expand"/"hit") naming
// the record or segment it attributes to via ItemID — or, with Type
// "history-run", a whole history-compaction run's summary (then only TS,
// TaskHash and Run carry data). The raw task text never reaches the log —
// only its TaskHash digest does.
type ShadowEntry struct {
	TS        time.Time `json:"ts"`
	TaskHash  string    `json:"task_hash"`
	SegmentID string    `json:"segment_id"`
	Tokens    int       `json:"tokens"`
	Score     float64   `json:"score"`
	Decision  string    `json:"decision"`
	// Type is the entry kind; empty means a per-segment decision (legacy
	// lines predate the field).
	Type string `json:"type,omitempty"`
	// Action is the machine-readable action carried by non-decision entries
	// ("tripwire" on Type "tripwire") and by kind=retrieve decisions
	// ("injected"/"skipped"). Admit decisions leave it empty — their
	// Decision field carries the action. Additive: older logs simply
	// lack the field.
	Action string `json:"action,omitempty"`
	// Kind is the decision class: "admit" for the per-segment elision
	// decisions the pipeline makes, "retrieve" for the store read side's
	// decisions (Step 17). Empty on legacy lines (written before the field
	// existed); readers treat empty as "admit". Additive: older logs simply
	// lack the field.
	Kind string `json:"kind,omitempty"`
	// Threshold is the score floor the decision was made against — the gate
	// floor in force at decision time (the protected-kind floor for
	// protected kinds, else the relocation threshold). Stats,
	// FalseNegativeRate and ReplayTable re-run the recorded decision from
	// score vs threshold without re-scoring. Additive: legacy lines predate
	// the field (0 = no threshold was in force).
	Threshold float64 `json:"threshold,omitempty"`
	// ItemID is the record or segment id an OUTCOME entry (Type "expand" or
	// "hit") attributes to; decision entries carry SegmentID instead.
	// Additive.
	ItemID string `json:"item_id,omitempty"`
	// Turn is the conversation turn the outcome happened in; 0 while turn
	// plumbing does not exist anywhere in the port. Additive.
	Turn int `json:"turn,omitempty"`
	// Run carries the run totals for Type "history-run" entries.
	Run *RunSummary `json:"run,omitempty"`
}

// isDecision reports whether the entry is a per-segment decision: an
// explicit Type "decision" or empty Type (the legacy spelling). Outcomes,
// tripwire overrides, and run summaries are not.
func (e ShadowEntry) isDecision() bool {
	return e.Type == "" || e.Type == EntryTypeDecision
}

// isElideDecision reports whether the entry is a per-segment ELISION
// decision — the kind the replay table, the Stats elide counters, and the
// false-negative ledger are built on. Retrieve decisions (Kind "retrieve")
// are decisions too, but their score means "relevant to the task right
// now", not "essential enough to keep": counting them as elides would pollute
// the false-negative rate with records the read side skipped on purpose and
// would replay injected/skipped through kept/elided vocabulary. The Go
// table models the elide axis only (the reference's _counterfactual maps
// retrieve kinds onto injected/skipped — a separate axis here). Empty Kind
// is admit: every legacy line behaves exactly as before this field existed.
func (e ShadowEntry) isElideDecision() bool {
	return e.isDecision() && e.Kind != DecisionKindRetrieve
}

// RunSummary is the per-run totals of one history-compaction pass — the
// numbers the TUI status line reports, persisted so runs are auditable
// alongside the per-segment decisions they produced.
type RunSummary struct {
	Shadow       bool   `json:"shadow"`
	Scanned      int    `json:"scanned"`
	Scored       int    `json:"scored"`
	Elided       int    `json:"elided"`
	TokensBefore int    `json:"tokens_before"`
	TokensAfter  int    `json:"tokens_after"`
	TokensSaved  int    `json:"tokens_saved"`
	Err          string `json:"error,omitempty"`
}

// ReplayReport summarizes what WOULD have been elided at a score threshold.
// The counts are entry-based; miss-risk — whether eliding a segment would
// actually have lost information the agent needed — is computable from the
// expand/hit outcome lines once they exist: see FalseNegativeRate and
// ReplayTable (whose StillMissed column is built on them).
type ReplayReport struct {
	Threshold      float64 `json:"threshold"`
	Entries        int     `json:"entries"`         // decision lines read
	UniqueSegments int     `json:"unique_segments"` // distinct segment_ids
	ElidedEntries  int     `json:"elided_entries"`  // decisions scoring below the threshold
	ElidedSegments int     `json:"elided_segments"` // distinct segment_ids elided at least once
	TokensTotal    int     `json:"tokens_total"`    // tokens across all entries
	TokensElided   int     `json:"tokens_elided"`   // tokens on elided entries
	MalformedLines int     `json:"malformed_lines"` // lines that failed to parse
}

// HashTask returns the short SHA-256 hex digest used as task_hash in the
// shadow log (16 hex chars — enough to group decisions by task without
// leaking the task text).
func HashTask(task string) string {
	sum := sha256.Sum256([]byte(task))
	return hex.EncodeToString(sum[:8])
}

// ShadowLog is a JSONL appender for decision records. Appends are
// goroutine-safe (mutex) and crash-atomic per line (one Write call on an
// O_APPEND descriptor, so concurrent late processes interleave whole lines).
type ShadowLog struct {
	path string
	mu   sync.Mutex
}

// DefaultShadowPath returns the shadow log location:
// ~/.local/share/late/compaction-shadow.jsonl, resolved through
// pathutil.LateDataDir (Windows keeps everything under the config dir).
func DefaultShadowPath() (string, error) {
	dir, err := pathutil.LateDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "compaction-shadow.jsonl"), nil
}

// NewShadowLog opens (creating parent directories 0700) the default shadow
// log at DefaultShadowPath.
func NewShadowLog() (*ShadowLog, error) {
	p, err := DefaultShadowPath()
	if err != nil {
		return nil, err
	}
	return NewShadowLogAt(p)
}

// NewShadowLogAt opens the shadow log at path, creating parent directories
// with 0700 (the log file itself is created 0600 on first append).
func NewShadowLogAt(path string) (*ShadowLog, error) {
	if path == "" {
		return nil, fmt.Errorf("compaction: shadow log path is empty")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("compaction: create shadow log dir %s: %w", dir, err)
	}
	return &ShadowLog{path: path}, nil
}

// Path returns the log file path.
func (l *ShadowLog) Path() string { return l.path }

// Append writes one shadow-log line. Zero fields are defaulted: a zero TS
// becomes time.Now() and a decision entry's empty Decision becomes
// DecisionKeep — outcomes (Type "expand"/"hit") and run summaries carry no
// decision and are never defaulted. The entry is serialized first so a
// marshal failure cannot leave a torn line behind.
func (l *ShadowLog) Append(e ShadowEntry) error {
	if e.Decision == "" && e.isDecision() {
		e.Decision = DecisionKeep
	}
	if e.TS.IsZero() {
		e.TS = time.Now()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("compaction: encode shadow entry: %w", err)
	}
	line = append(line, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("compaction: open shadow log %s: %w", l.path, err)
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("compaction: append shadow log %s: %w", l.path, err)
	}
	return nil
}

// AppendRun writes one history-compaction run summary line (Type
// "history-run") carrying run's totals grouped under taskHash. It goes
// through the same crash-atomic append path as decisions; Replay skips
// these lines so run summaries never count as decisions.
func (l *ShadowLog) AppendRun(taskHash string, run RunSummary) error {
	return l.Append(ShadowEntry{
		TaskHash: taskHash,
		Type:     EntryTypeHistoryRun,
		Run:      &run,
	})
}

// AppendOutcome writes one outcome line (Type "expand" or "hit") naming the
// item it attributes to: the record id itself for the record-level outcome,
// plus one outcome per contributing segment id — the reference pipeline.py
// expand() attribution that ties every expand back to the score decisions
// that caused the elision. kind must be one of the two outcome types;
// turn is the conversation turn (0 while turn plumbing does not exist).
// Outcomes go through the same crash-atomic append path as decisions;
// FalseNegativeRate and the replay table's still-missed column are built on
// them.
func (l *ShadowLog) AppendOutcome(kind, itemID string, turn int) error {
	if kind != EntryTypeExpand && kind != EntryTypeHit {
		return fmt.Errorf("compaction: unknown outcome kind %q", kind)
	}
	if itemID == "" {
		return fmt.Errorf("compaction: outcome needs an item id")
	}
	return l.Append(ShadowEntry{
		Type:   kind,
		ItemID: itemID,
		Turn:   turn,
	})
}

// readEntries parses the whole log into entries in file order — appends are
// serialized whole lines, so file order is chronological. Blank lines are
// skipped; malformed lines (the torn residue of a crash mid-append) are
// counted in the second return value and skipped. A missing log file is
// (nil, 0, nil): nothing has been scored yet, not an error.
func (l *ShadowLog) readEntries() ([]ShadowEntry, int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("compaction: open shadow log %s: %w", l.path, err)
	}
	defer f.Close()

	var (
		entries   []ShadowEntry
		malformed int
	)
	sc := bufio.NewScanner(f)
	// Segment IDs are short but the lines carry no payload text; a 4 MiB
	// cap is purely defensive against a corrupted or hostile log.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e ShadowEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			malformed++
			continue
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return nil, malformed, fmt.Errorf("compaction: read shadow log %s: %w", l.path, err)
	}
	return entries, malformed, nil
}

// Replay reads the whole log and reports what WOULD be elided at threshold:
// every decision whose score is strictly below threshold counts as elided.
// History-run summaries, tripwire records, and outcome lines are not
// decisions — they carry no segment score — so they are skipped, not counted
// as malformed. A missing log file is an empty report, not an error (nothing
// has been scored yet).
func (l *ShadowLog) Replay(threshold float64) (ReplayReport, error) {
	entries, malformed, err := l.readEntries()
	if err != nil {
		return ReplayReport{}, err
	}

	report := ReplayReport{Threshold: threshold, MalformedLines: malformed}
	seen := make(map[string]bool)
	elidedSeen := make(map[string]bool)
	for _, e := range entries {
		if !e.isElideDecision() {
			// Non-elide entries — history-run summaries, tripwire records,
			// expand/hit outcomes, and kind=retrieve decisions — are not
			// elision decisions: counting them would inflate
			// Entries/TokensTotal and mint a "" unique segment (or, for
			// retrieve decisions, replay "skipped" as "elided").
			continue
		}
		report.Entries++
		report.TokensTotal += e.Tokens
		if !seen[e.SegmentID] {
			seen[e.SegmentID] = true
			report.UniqueSegments++
		}
		if e.Score < threshold {
			report.ElidedEntries++
			report.TokensElided += e.Tokens
			if !elidedSeen[e.SegmentID] {
				elidedSeen[e.SegmentID] = true
				report.ElidedSegments++
			}
		}
	}
	return report, nil
}

// Stats summarizes a shadow log's decisions and outcomes: how many
// per-segment decisions were recorded, how many of those the log itself
// vouches were — or, in shadow mode, would have been — elided at the
// threshold recorded with them, and how many distinct items carry expand
// and hit outcomes. A missing log file is a zero Stats, not an error.
type Stats struct {
	// Decisions is the number of per-segment decision entries.
	Decisions int `json:"decisions"`
	// ElidedDecisions is the number of decision entries whose score is
	// strictly below the threshold recorded with them. Legacy entries
	// without a recorded threshold (0 = none in force) can never count: no
	// score is below an unknown floor.
	ElidedDecisions int `json:"elided_decisions"`
	// Expands is the number of distinct item ids with an expand outcome.
	Expands int `json:"expands"`
	// Hits is the number of distinct item ids with a hit outcome.
	Hits int `json:"hits"`
}

// Stats reads the whole log and counts decisions and outcomes. Outcome
// entries (Type "expand"/"hit") never count as decisions, and outcome item
// ids are counted distinct — one record expanded five times is one expand.
func (l *ShadowLog) Stats() (Stats, error) {
	entries, _, err := l.readEntries()
	if err != nil {
		return Stats{}, err
	}
	var st Stats
	expanded := make(map[string]bool)
	hit := make(map[string]bool)
	for _, e := range entries {
		switch {
		case e.isElideDecision():
			// Elide decisions only: retrieve decisions (Step 17) carry a
			// relevance score with injected/skipped actions — they are
			// tracked through the raw log, not through the elide counters.
			st.Decisions++
			if e.Threshold > 0 && e.Score < e.Threshold {
				st.ElidedDecisions++
			}
		case e.Type == EntryTypeExpand && e.ItemID != "":
			expanded[e.ItemID] = true
		case e.Type == EntryTypeHit && e.ItemID != "":
			hit[e.ItemID] = true
		}
	}
	st.Expands = len(expanded)
	st.Hits = len(hit)
	return st, nil
}

// FalseNegativeRate reports the share of elided segments the agent later had
// to expand — every expand is a recorded false negative of the elision
// decision that relocated its content (the reference's outcome ledger).
//
// Precisely: the numerator counts the DISTINCT segment ids with a decision
// entry whose score is strictly below the threshold recorded with it AND a
// later expand outcome naming that id (later = appended after the decision
// line); the denominator counts the DISTINCT segment ids elided at their own
// recorded threshold. 0 when nothing was elided — an empty ledger is not an
// error. Legacy entries without a recorded threshold never count as elided.
func (l *ShadowLog) FalseNegativeRate() (float64, error) {
	entries, _, err := l.readEntries()
	if err != nil {
		return 0, err
	}
	elided := make(map[string]bool)
	for _, e := range entries {
		if e.isElideDecision() && e.SegmentID != "" && elidedAtOwnThreshold(e) {
			elided[e.SegmentID] = true
		}
	}
	if len(elided) == 0 {
		return 0, nil
	}
	missed := stillMissedIDs(entries, elidedAtOwnThreshold)
	return float64(len(missed)) / float64(len(elided)), nil
}

// elidedAtOwnThreshold is the elision predicate on a decision entry's own
// recorded data: score strictly below the threshold recorded with it. An
// entry without a recorded threshold (legacy lines, or no threshold in
// force) is never elided — an unknown floor vouches for nothing.
func elidedAtOwnThreshold(e ShadowEntry) bool {
	return e.Threshold > 0 && e.Score < e.Threshold
}

// stillMissedIDs returns the distinct segment ids the elide predicate
// selects — a "would relocate" set — that ALSO have a later expand outcome
// naming them: content the predicate would have removed that the agent
// demonstrably had to fetch back. Expand outcomes may name a record id
// (the record-level outcome) instead of a segment id; record ids never
// match a segment id here, so only the per-segment attribution links.
func stillMissedIDs(entries []ShadowEntry, elided func(ShadowEntry) bool) map[string]bool {
	// lastExpandAt maps an item id to the LAST line index of an expand
	// outcome naming it (the scan runs in ascending order, so the final
	// assignment is the maximum).
	lastExpandAt := make(map[string]int)
	for i, e := range entries {
		if e.Type == EntryTypeExpand && e.ItemID != "" {
			lastExpandAt[e.ItemID] = i
		}
	}
	missed := make(map[string]bool)
	for i, e := range entries {
		if !e.isElideDecision() || e.SegmentID == "" || !elided(e) {
			continue
		}
		if j, ok := lastExpandAt[e.SegmentID]; ok && j > i {
			missed[e.SegmentID] = true
		}
	}
	return missed
}

// ReplayRow is one threshold's replay of the recorded decisions — what the
// gate WOULD have done at that threshold, re-decided from the recorded
// scores without re-running the scorer (the reference's replay table; that
// is the point of replay: the same run, re-decided at other settings).
type ReplayRow struct {
	// Threshold is the replayed keep threshold.
	Threshold float64 `json:"threshold"`
	// Kept is the number of decision entries whose score is at or above the
	// threshold — they would have stayed in the output.
	Kept int `json:"kept"`
	// Relocated is the number of decision entries whose score is strictly
	// below the threshold — they would have been elided. Entries, not
	// distinct segments: a segment re-scored twice counts twice.
	Relocated int `json:"relocated"`
	// TokensSaved is the total token count of the relocated entries — the
	// tokens the threshold would have saved.
	TokensSaved int `json:"tokens_saved"`
	// StillMissed is the number of DISTINCT relocated segment ids that have
	// a later expand outcome: content the threshold would have removed that
	// the agent demonstrably needed back.
	StillMissed int `json:"still_missed"`
}

// ReplayTable replays the log's recorded scores at every given threshold,
// returning one row per threshold sorted ascending. Each entry is re-decided
// from its own recorded score against the GIVEN threshold — the entry's own
// recorded threshold plays no part here. A missing log file yields a zero
// row per threshold, not an error.
func (l *ShadowLog) ReplayTable(thresholds []float64) ([]ReplayRow, error) {
	entries, _, err := l.readEntries()
	if err != nil {
		return nil, err
	}
	rows := make([]ReplayRow, 0, len(thresholds))
	for _, th := range thresholds {
		row := ReplayRow{Threshold: th}
		for _, e := range entries {
			if !e.isElideDecision() {
				// See isElideDecision: the table replays the elide axis;
				// retrieve decisions and outcomes never count.
				continue
			}
			if e.Score < th {
				row.Relocated++
				row.TokensSaved += e.Tokens
			} else {
				row.Kept++
			}
		}
		row.StillMissed = len(stillMissedIDs(entries, func(e ShadowEntry) bool {
			return e.Score < th
		}))
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Threshold < rows[j].Threshold })
	return rows, nil
}
