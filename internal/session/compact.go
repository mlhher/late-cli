package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"late/internal/client"
	"late/internal/common"
	"late/internal/compaction"
	"late/internal/tool"
)

// Full-history context compaction.
//
// CompactContext is the core both the /jev-compact-context command and the
// auto-trigger call: it walks the session history, segments every eligible
// message after the frozen prefix, scores the segments against the ongoing
// task, and relocates low-scoring segments into the elided-original store,
// replacing them in place with [[elided …]] pointer lines that the expand
// tool resolves. It reuses the compaction port's segmentation and scoring
// contract (internal/compaction) but owns the history walk, because the
// pipeline's tool-output path (CompactToolOutput) works one tool result at a
// time and knows nothing about history structure.
//
// Invariants (the upstream jev-compaction design):
//
//   - Frozen prefix: the work walk starts at index max(high-water mark,
//     max(1, len(history)/4)) and never below it, so the prompt-cache
//     anchor — the system prompt at index 0 plus the earliest exchanges —
//     stays byte-identical across compactions. The high-water mark is the
//     per-session, monotonic message index every completed mutating walk
//     has covered; it persists in the session meta sidecar
//     (CompactionHighWater), so the prefix is append-only across runs AND
//     restarts: it never shrinks, and messages below it are never scored or
//     rewritten. A walk that would mutate a message below the mark (a stale
//     mark over a shrunken history) fails loudly with ErrFrozenPrefix and
//     changes nothing — the reference's FrozenPrefixError analog.
//   - Pointer-bearing messages are final: any message whose content carries
//     an [[elided …]] pointer (compaction.FindPointers) is skipped
//     entirely — never re-segmented, never re-scored, never rewritten — so
//     an earlier run's pointers cannot be nested or invalidated.
//   - Only a completing mutating walk advances the high-water mark (to the
//     history length it covered): shadow runs mutate nothing and report
//     only, mid-walk scorer aborts leave the mark where it was, and a
//     failed mark persistence rolls the in-memory advance back.
//   - User messages are never compacted. Pure-prose assistant messages are
//     never compacted either: an assistant message with NO tool calls
//     (decisions, explanations, plans) is the conversation's narrative, not
//     recoverable work output, and stays byte-identical. Compaction
//     candidates are tool results and assistant messages WITH ToolCalls
//     (whose Content annotates the calls). Assistant ToolCalls are
//     structurally required and are never touched: only Content shrinks.
//   - Tool results produced by protected tools (activate_skill — see
//     compaction.ProtectedTool) are never compacted: the result IS the
//     instructions the agent was told to follow, and eliding them would
//     silently strip the guidance out of the conversation.
//   - Segments are scored against the ongoing task (the last user message);
//     a segment scoring strictly below the threshold is elided into the
//     store, and each run of consecutive elided segments is replaced by one
//     [[elided …]] pointer line standing exactly where the run stood —
//     content-addressed (compaction.ContentID with salt "" and the "r"
//     prefix), carrying the run's [first, last] line range and a 120-char
//     escaped summary. compaction.Reconstruct over the rewritten message
//     and the store is the byte-for-byte inverse of the rewrite.
//   - Fail-open: a scorer error that still answers every requested id (the
//     pipeline's contract — unscoreable items come back as keep-scores)
//     does not stop the walk: those scores are used, the scorer's errors
//     accumulate and surface at the end via errors.Join. The walk stops
//     mid-flight only when the scorer returns no usable scores for a
//     message, or when the scorer reports an auth-class failure
//     (compaction.KindAuth — a bad or missing API key, which no later
//     message can score either): that stops the walk immediately with the
//     typed error instead of firing one doomed request per message.
//     Messages already rewritten stay rewritten (their pointers and stored
//     originals are valid) and the returned error reports how far the walk
//     got. Compaction never breaks a session.
//   - Shadow mode (compaction-mode "shadow") runs the full scoring walk and
//     computes the honest would-save report without mutating history.
//
// The scorer and the store are passed in (dependency injection): the session
// never constructs the compaction pipeline — the TUI/main holds it and hands
// CompactContext its scoring client and original-text store.
//
// ErrFrozenPrefix is the fail-loud sentinel for a frozen-prefix violation:
// a walk asked to score or rewrite a message below the persisted high-water
// mark. Like the reference's FrozenPrefixError it fires before anything is
// changed — a violating run leaves the history, the store, and the mark
// exactly as they were.
var ErrFrozenPrefix = errors.New("compaction: would mutate the frozen prefix")

const (
	// minCompactChars is the content size above which a post-frozen-prefix
	// assistant or tool-result message becomes a compaction candidate. It is
	// the segment default: a message shorter than one maximum segment offers
	// no elidable granularity.
	minCompactChars = compaction.DefaultMaxSegChars

	// defaultFrozenPercent is the share of history messages (by count) the
	// frozen prefix keeps byte-identical.
	defaultFrozenPercent = 25

	// compactionTaskFallback is the derived task when the history contains
	// no user message to score against.
	compactionTaskFallback = "general context compaction"

	// maxTaskChars caps the derived task so one giant prompt does not become
	// a giant scoring request.
	maxTaskChars = 500

	// keepScoreFallback is the defensive fail-open score for a segment whose
	// id is missing from the scorer's answer map: fully essential, keep.
	// (compaction.DecisionClient.ScoreBatch fills every id; this only guards
	// against a non-conforming HistoryScorer.)
	keepScoreFallback = 1.0
)

// HistoryScorer scores one batch of segments against an ongoing task. It is
// satisfied by compaction.DecisionClient (the pipeline's scoring client);
// tests inject map-based stubs. Like the pipeline's contract, an error
// return that still carries a score for every requested id is the fail-open
// shape (unscoreable items answered as keep-scores): CompactContext keeps
// walking with those scores and surfaces the errors at the end. An error
// with scores missing means the batch could not be scored — CompactContext
// stops the walk rather than eliding on unusable answers.
type HistoryScorer interface {
	ScoreBatch(ctx context.Context, task string, items map[string]compaction.Item) (map[string]float64, error)
}

// ElideStore is the write side of the elided-original store CompactContext
// relocates runs into: PutRecord stores each elided run's full record under
// the pointer's id so the expand tool can retrieve it (tool.ExpandStore
// reads the text back) and Step 13's outcomes can attribute it (origin,
// token count, summary, contributing segment ids). New pointers are
// content-addressed (compaction.ContentID, "r:<8hex>" — same run text, same
// id, same record), so puts are idempotent: an existing id keeps its first
// record. NextID remains only for legacy "elide-<n>" ids minted by older
// builds; new code never calls it. One store is one id space; production
// wiring backs it with the compaction pipeline's store so tool-output and
// history pointers share it.
type ElideStore interface {
	NextID() string
	Put(id, text string)
	PutRecord(rec compaction.Record)
}

// CompactStore is the in-memory original-text store backing history
// compaction: CompactContext Puts every elided run's original under its
// content id, and the expand tool Gets it back — it satisfies
// tool.ExpandStore directly. Put is idempotent (an existing id keeps its
// first record); NextID mints legacy "elide-<n>" ids only. Safe for
// concurrent use.
type CompactStore struct {
	mu        sync.Mutex
	originals map[string]string
	next      int
}

// NewCompactStore returns an empty original-text store.
func NewCompactStore() *CompactStore {
	return &CompactStore{originals: make(map[string]string)}
}

// NextID mints the next legacy elide-pointer id ("elide-<n>", 1-based).
func (s *CompactStore) NextID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	return fmt.Sprintf("elide-%d", s.next)
}

// Put stores text under id. Idempotent: an existing id keeps its first
// record (content ids hash the text, so two runs with the same id carry the
// same text anyway).
func (s *CompactStore) Put(id, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.originals == nil {
		s.originals = make(map[string]string)
	}
	if _, exists := s.originals[id]; exists {
		return
	}
	s.originals[id] = text
}

// PutRecord stores a record's text under its id. The in-memory session
// store keeps originals only — origin, kind, and counter metadata are the
// file-backed compaction store's business (main.go wires the same
// *compaction.Store into the history walk in enabled mode) — and nothing
// here relies on them: shadow runs never store, and tests only read text
// back. Idempotent like Put.
func (s *CompactStore) PutRecord(rec compaction.Record) {
	s.Put(rec.ID, rec.Text)
}

// Get returns the original stored for id (the tool.ExpandStore read side).
// An unknown id reports ("", false).
func (s *CompactStore) Get(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	text, ok := s.originals[id]
	return text, ok
}

// Len reports how many records the store holds (tests and diagnostics).
func (s *CompactStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.originals)
}

// The expand tool reads history-elided originals straight from this store.
var _ tool.ExpandStore = (*CompactStore)(nil)

// The compaction pipeline's scoring client is the production HistoryScorer.
var _ HistoryScorer = (*compaction.DecisionClient)(nil)

// CompactionOptions tunes CompactContext; zero values are production
// defaults.
type CompactionOptions struct {
	// Threshold is the score strictly below which a segment is elided.
	// Values outside (0, 1] fall back to compaction.DefaultRelocationThreshold
	// (0.35), matching Pipeline.EnableRelocation.
	Threshold float64
	// FrozenPercent is the share of history messages (by count) kept
	// byte-identical in the frozen prefix. Values outside (0, 100] fall back
	// to defaultFrozenPercent (25); 100 freezes everything (a no-op run).
	FrozenPercent int
	// ShadowOnly computes the would-save report without mutating history or
	// storing originals (compaction-mode "shadow").
	ShadowOnly bool
}

// CompactionReport summarizes one CompactContext run. In shadow mode the
// counts are what a mutating run would have done (honest staging: the same
// numbers a real run reports, nothing applied).
type CompactionReport struct {
	// MessagesScanned is the number of history messages the walk examined —
	// everything after the frozen prefix.
	MessagesScanned int
	// MessagesScored is the number of messages whose segments were actually
	// scored, including fail-open-scored ones (a scorer error answered with
	// a complete keep-score map still counts the message as scored).
	MessagesScored int
	// MessagesCompacted is the number of messages actually rewritten (or
	// would-be rewritten in shadow mode).
	MessagesCompacted int
	// SegmentsElided is the number of segments relocated into the store.
	SegmentsElided int
	// TokensBefore is the summed EstimateMessageTokens of the whole history
	// before the run.
	TokensBefore int
	// TokensAfter is the same sum after the run (the would-be sum in shadow
	// mode).
	TokensAfter int
	// TokensSaved is TokensBefore - TokensAfter.
	TokensSaved int
	// StoreSize is the total byte size of the original texts this run
	// relocated into the store (what they would be in shadow mode).
	StoreSize int
	// ShadowOnly reports whether the run mutated anything.
	ShadowOnly bool
	// TaskHash is the compaction.HashTask digest of the scoring task this
	// run scored against (the derived ongoing task, never its text). The
	// shadow log's per-run summary lines group under it, the same way the
	// per-segment decision lines do.
	TaskHash string
}

// CompactContext compacts the session history in place (unless
// opts.ShadowOnly) per the package doc. scorer scores segments; store
// receives the elided originals. A nil store leaves relocation disarmed —
// like the pipeline with relocation off, nothing is elided, because a
// pointer whose original cannot be stored must never enter history. The
// returned error is non-nil when the scorer reported failures: a walk that
// completed on fail-open scores joins those scorer errors at the end, and a
// walk that had to stop (no usable scores for a message) reports how far it
// got. A walk that would mutate a message below the persisted high-water
// mark fails with ErrFrozenPrefix before changing anything. The report covers
// everything completed either way, and already-rewritten messages stay
// rewritten; history persistence is the caller's job (SaveHistory), while a
// completing mutating run persists the advanced high-water mark itself
// (SessionMeta.CompactionHighWater).
func (s *Session) CompactContext(ctx context.Context, scorer HistoryScorer, store ElideStore, opts CompactionOptions) (CompactionReport, error) {
	var report CompactionReport
	if scorer == nil {
		return report, fmt.Errorf("context compaction: no scorer provided")
	}
	// Zero values are production defaults (mirrors Pipeline.EnableRelocation
	// and the frozen-prefix default).
	if opts.Threshold <= 0 || opts.Threshold > 1 {
		opts.Threshold = compaction.DefaultRelocationThreshold
	}
	if opts.FrozenPercent <= 0 || opts.FrozenPercent > 100 {
		opts.FrozenPercent = defaultFrozenPercent
	}
	report.ShadowOnly = opts.ShadowOnly

	// The frozen prefix is append-only: it starts at the persisted high-water
	// mark — every completed mutating walk covered the history below it, so
	// those bytes are the prompt-cache anchor — and only grows to the
	// count-based floor, never shrinks.
	mark := s.CompactionHighWater()
	if mark > len(s.History) {
		// The mark outlives the history it froze (a stale sidecar over a
		// truncated history). Any progress would rewrite below-mark
		// messages; fail loudly and change nothing — the reference's
		// FrozenPrefixError analog.
		return report, fmt.Errorf("compaction: high-water mark %d is beyond the %d-message history: %w", mark, len(s.History), ErrFrozenPrefix)
	}
	frozen := max(mark, frozenPrefix(len(s.History), opts.FrozenPercent))
	// startLen is the history length this walk covers: a completing walk
	// advances the mark to it (never mid-walk, never in shadow mode).
	startLen := len(s.History)
	report.MessagesScanned = len(s.History) - frozen

	report.TokensBefore = historyMessageTokens(s.History)
	report.TokensAfter = report.TokensBefore

	// The scoring task is hashed up front so every report carries its group
	// key even when the walk stops early: the run summary the caller appends
	// to the shadow log uses this digest, and the per-segment decisions the
	// scorer logs share it.
	task := compactionTask(s.History)
	report.TaskHash = compaction.HashTask(task)

	if store == nil {
		// Relocation disarmed: nothing can be stored for the expand tool, so
		// nothing is elided. The scan/token counts above still stand.
		return report, nil
	}

	scored := 0
	var walkErrs []error

	// Tool origins: tool_call_id → tool name from the assistant messages
	// that issued the calls, so the history surface knows which tool
	// produced each tool result — the same "tool:<name>" origin the
	// tool-output path stamps on its records. Tool results from protected
	// tools (compaction.ProtectedTool — activate_skill) are skipped before
	// segmentation: the gate's protected score floor makes them unelidable
	// on the tool-output path, and here the honest equivalent is to never
	// even score them (a kept decision is guaranteed, not scored into).
	originTool := make(map[string]string)
	for i := range s.History {
		for _, tc := range s.History[i].ToolCalls {
			if tc.ID != "" && tc.Function.Name != "" {
				originTool[tc.ID] = tc.Function.Name
			}
		}
	}

	for i := frozen; i < len(s.History); i++ {
		// Unreachable while frozen >= mark holds (it is how frozen is
		// computed); fail loudly rather than silently mutate the anchor if
		// that computation ever regresses.
		if i < mark {
			return report, fmt.Errorf("compaction: message %d sits below the high-water mark %d: %w", i, mark, ErrFrozenPrefix)
		}
		msg := &s.History[i]
		if msg.Role == "tool" {
			if name, ok := originTool[msg.ToolCallID]; ok && compaction.ProtectedTool(name) {
				// A protected tool's result (the skill's instructions):
				// never re-segmented, never scored, never rewritten.
				continue
			}
		}
		// Already-elided messages are final: their pointers stand in history
		// and their originals live in the store. Re-scoring one would let a
		// pointer line become segment text and nest new pointers.
		if len(compaction.FindPointers(msg.Content.Text)) > 0 {
			continue
		}
		if !compactableContent(msg) {
			continue
		}
		segs := compaction.SegmentSegments(msg.Content.Text, minCompactChars)
		if len(segs) == 0 {
			continue
		}
		items := make(map[string]compaction.Item, len(segs))
		for _, seg := range segs {
			items[seg.ID] = compaction.Item{Text: seg.Text, Tokens: seg.Tokens}
		}
		scores, err := scorer.ScoreBatch(ctx, task, items)
		if err != nil {
			var ae *compaction.Error
			if errors.As(err, &ae) && ae.Kind == compaction.KindAuth {
				// Auth (the reference's JevAuthError: 401/403 — bad or
				// missing API key) is a session-level failure: no later
				// message can score either, so continuing the walk would
				// only fire one doomed request per message. Stop right
				// here and surface the typed error; everything already
				// rewritten stays rewritten.
				return report, fmt.Errorf("context compaction stopped after %d messages: %w", scored, err)
			}
			if !scoresComplete(items, scores) {
				// Wholesale failure: the scorer returned no usable scores
				// for this message. Fail-open mid-walk: stop the walk.
				// Messages already rewritten stay rewritten — their pointers
				// and stored originals are valid — and the error says how
				// far the walk got.
				return report, fmt.Errorf("context compaction stopped after %d messages: %w", scored, err)
			}
			// Fail-open complete: every requested id came back (the
			// pipeline's contract — unscoreable items are answered with
			// keep-scores), so the scores are usable. Keep the walk going;
			// the scorer's errors surface at the end.
			walkErrs = append(walkErrs, err)
		}
		scored++
		report.MessagesScored++

		// Reference flush_run pattern (shared with the tool-output path via
		// compaction.BuildElidedRun): consecutive below-threshold segments
		// group into ONE run sharing a single record and pointer; kept
		// segments flush the pending run and keep their exact text in place.
		// Pointer lines stand exactly where the runs stood, so
		// compaction.Reconstruct of the compacted text (with the store)
		// restores the original byte for byte. Pointers are content
		// addressed: id = ContentID(runText, salt="", "r"). The salt is
		// empty deliberately — content addressing is per-text, and the
		// history walk has no stable origin ref to namespace it with; the
		// same run text always maps to the same id.
		var (
			pointers   []string
			stored     int
			elidedSegs int
			b          strings.Builder
			run        []compaction.Segment
		)
		flushRun := func() {
			if len(run) == 0 {
				return
			}
			er := compaction.BuildElidedRun(run, "")
			pointers = append(pointers, er.Pointer)
			stored += len(er.Text)
			elidedSegs += er.Segments
			if !opts.ShadowOnly {
				// The record mirrors the reference store.py Record for a
				// history-elided run: kind elided_segment, origin
				// "history" (the walk has no finer ref to attribute the
				// run to), the run's token count and pointer summary, and
				// the contributing segment ids the outcomes ledger
				// attributes back to. CreatedTurn stays 0 — turn plumbing
				// does not exist yet.
				store.PutRecord(compaction.Record{
					ID:          er.ID,
					Text:        er.Text,
					Kind:        compaction.RecordKindElidedSegment,
					Origin:      compaction.Origin{Source: compaction.OriginSourceHistory},
					Tokens:      er.Tokens,
					CreatedTurn: 0,
					Summary:     er.Summary,
					SegmentIDs:  er.SegmentIDs,
				})
			}
			b.WriteString(er.Pointer)
			b.WriteString("\n")
			run = run[:0]
		}
		// Per-segment scores and the (flat) elide floor, then the shared
		// paragraph-atomicity decision (same rule as the tool-output path):
		// pieces cut from the same oversized paragraph share ONE decision,
		// made on the minimum sibling score — one low piece elides the whole
		// paragraph (stored whole, restorable whole through the pointer), so
		// a cut JSON blob is never partially elided into an unparseable
		// remnant, and pieces above the floor keep the paragraph fully.
		segScores := make([]float64, len(segs))
		segFloors := make([]float64, len(segs))
		for i, seg := range segs {
			score, ok := scores[seg.ID]
			if !ok {
				score = keepScoreFallback
			}
			segScores[i] = score
			segFloors[i] = opts.Threshold
		}
		elide := compaction.AtomicElideDecisions(segs, segScores, segFloors)
		for i, seg := range segs {
			if !elide[i] {
				flushRun()
				b.WriteString(seg.Text)
				continue
			}
			run = append(run, seg)
		}
		flushRun()
		if len(pointers) == 0 {
			continue
		}
		compacted := b.String()

		report.MessagesCompacted++
		report.SegmentsElided += elidedSegs
		report.StoreSize += stored
		// Only Content.Text changes, so the per-message token delta is the
		// text delta — exact for both the mutating and the shadow run.
		report.TokensAfter -= common.EstimateTokenCount(msg.Content.Text) - common.EstimateTokenCount(compacted)
		if !opts.ShadowOnly {
			// In-place content swap: Role, ToolCalls, ToolCallID
			// and ReasoningContent are preserved; only Content shrinks.
			msg.Content = client.TextContent(compacted)
		}
	}

	report.TokensSaved = report.TokensBefore - report.TokensAfter
	// The walk reached the end of history: a mutating run freezes everything
	// it covered by advancing the mark to the run-start length and persisting
	// it (shadow runs mutate nothing and report only; a mid-walk abort
	// returned above without advancing). A failed persistence rolls the
	// in-memory advance back and surfaces here — the next run re-walks the
	// uncovered tail, where pointer-bearing messages are skipped, so nothing
	// is ever rewritten twice.
	if !opts.ShadowOnly && startLen > mark {
		if err := s.UpdateCompactionHighWater(startLen); err != nil {
			walkErrs = append(walkErrs, fmt.Errorf("persisting compaction high-water mark: %w", err))
		}
	}
	// Fail-open scorer errors accumulated along the walk surface here; a
	// clean walk returns a nil error.
	return report, errors.Join(walkErrs...)
}

// scoresComplete reports whether scores answers every id in items — the
// pipeline's fail-open contract shape (compaction.DecisionClient.ScoreBatch
// fills every id, unscoreable items with keep-scores, even when it also
// reports errors). A missing id means the scorer had nothing usable for that
// segment.
func scoresComplete(items map[string]compaction.Item, scores map[string]float64) bool {
	for id := range items {
		if _, ok := scores[id]; !ok {
			return false
		}
	}
	return true
}

// frozenPrefix computes how many leading history messages are never
// compacted: max(1, len(history)*percent/100), rounded down, capped at the
// history length. The floor of 1 always keeps message[0] — the system
// prompt — inside the prefix (prompt-cache preservation); for histories of
// twelve or more messages at the default 25% the prefix is a quarter of the
// messages, rounded down.
func frozenPrefix(n, percent int) int {
	if n <= 0 {
		return 0
	}
	frozen := n * percent / 100
	if frozen < 1 {
		frozen = 1
	}
	if frozen > n {
		frozen = n
	}
	return frozen
}

// compactableContent reports whether msg is a compaction candidate: a
// tool-result message, or an assistant message WITH tool calls, whose
// text-only content exceeds minCompactChars.
//
// User messages are never compacted. Pure-prose assistant messages — an
// assistant message with NO tool calls: decisions, explanations, plans — are
// never compacted either: they are the conversation's narrative, not
// recoverable work output, and the agent's later reasoning builds on them
// verbatim. Only tool results and the content annotating assistant tool
// calls are work output that the store can hold and the expand tool can
// restore. Multimodal messages pass through untouched (parts cannot be
// rebuilt losslessly here); shorter messages offer no elidable granularity.
func compactableContent(msg *client.ChatMessage) bool {
	switch msg.Role {
	case "tool":
		// Tool results are the compaction surface: recoverable from the
		// store through the expand tool.
	case "assistant":
		// Assistant content is compactable only when it annotates tool
		// calls; pure prose stays byte-identical.
		if len(msg.ToolCalls) == 0 {
			return false
		}
	default:
		return false
	}
	if len(msg.Content.Parts) != 0 {
		return false
	}
	return len(msg.Content.Text) > minCompactChars
}

// compactionTask derives the ongoing-task description the scorer scores
// against: the last user message's content truncated to maxTaskChars runes,
// or compactionTaskFallback when the history has no user message.
func compactionTask(history []client.ChatMessage) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" {
			return truncateRunes(history[i].Content.String(), maxTaskChars)
		}
	}
	return compactionTaskFallback
}

// truncateRunes cuts s to at most max runes, rune-safe and without a suffix.
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

// historyMessageTokens sums EstimateMessageTokens over the history. The
// system prompt lives outside the history (Session.systemPrompt) and is
// never touched, so it is not part of the before/after math.
func historyMessageTokens(history []client.ChatMessage) int {
	total := 0
	for _, msg := range history {
		total += common.EstimateMessageTokens(msg)
	}
	return total
}
