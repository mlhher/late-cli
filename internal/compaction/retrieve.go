package compaction

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Retrieval read side (implementation_plan.md Step 17) — the port of the
// reference pipeline.py retrieve(): score the STORE's digest entries (cheap
// per-record summaries, capped by a token budget) against the current task,
// take the top-k entries scoring at or above the threshold, log one
// kind=retrieve decision per scored entry, and return the full Records so
// the caller can put them into the WORK AREA of the next request — never
// the frozen prefix.
//
// Documented deviation from the reference: the reference asks
// RETRIEVE_QUESTION, a typed Noul question carrying its own instructions and
// true/false criteria, while the task digest travels in state.task. The Go
// DecisionClient speaks the reference's question format (instructions plus
// true/false criteria — noulQuestionFor in client.go) but carries ONE fixed
// question, the ADMIT_QUESTION port below, with no per-call question
// plumbing, so the retrieve question's text is folded into the task slot
// instead (RetrieveTask): the constants here carry the reference wording
// verbatim, and the request then scores "relevant to <that framing>". The
// semantics are equivalent — a high score means the stored item is relevant
// to the task right now.
//
// A second deviation, also deliberate: the reference's score_items fails
// open with keep-scores on ANY Jev error, so an outage retrieves (and
// injects) arbitrary records at 1.0. That is the right fail-open direction
// for elision (never lose information) and the wrong one for retrieval
// (never stuff the work area with records nobody ranked): this port returns
// no records and logs no decisions when scoring failed — the read side is
// aborted for the turn, not decided against garbage. Auth-class failures
// additionally disable the pipeline's scoring for the rest of the session
// (the same one-warning policy every other scoring path takes).

// RetrieveQuestionInstructions is the reference RETRIEVE_QUESTION
// instructions string (pipeline.py), verbatim. See the RetrieveTask
// documentation for how it reaches the scorer in this port.
const RetrieveQuestionInstructions = "Is this stored item relevant to the task described in `task` right now? " +
	"Answer true if the next step is likely to need it. Answer false if it belongs " +
	"to unrelated work, or is superseded by something more recent."

// RetrieveQuestionTrue is the reference RETRIEVE_QUESTION true-criteria
// string (pipeline.py), verbatim.
const RetrieveQuestionTrue = "The item is relevant to the current step."

// RetrieveQuestionFalse is the reference RETRIEVE_QUESTION false-criteria
// string (pipeline.py), verbatim.
const RetrieveQuestionFalse = "The item is not relevant right now."

// AdmitQuestionInstructions is the ONE question this port's DecisionClient
// asks about every item (noulQuestionFor in client.go, the reference's admit
// end of score_items). Like the reference, the question never embeds the
// task: the task digest travels in state.task.
//
// The wording sharpens the reference ADMIT_QUESTION's boundary while keeping
// its conservative spirit ("answer false ONLY if it is noise"): real-run
// replay showed the reference wording scoring dense work content 0.4–0.89 —
// almost nothing elided — because "facts, identifiers, errors, results" also
// describes the boilerplate progress logs that embed those words. The rewrite
// pins the NOISE side to what it actually is (progress output,
// confirmations, repeated boilerplate, large repetitive dumps whose key
// facts are retained nearby or re-derivable) and the ESSENTIAL side to the
// concrete facts a later step may have to refer back to, and tells the
// scorer explicitly that verbose intermediate logs are noise even when they
// mention relevant words while their final results/summaries are essential.
const AdmitQuestionInstructions = "Will this item still be needed later in the task described in `task`? " +
	"Answer false if it is NOISE: progress output, success or progress confirmations, repeated " +
	"boilerplate, or a large repetitive dump (verbose intermediate logs, build or test output, " +
	"file listings) whose key facts — file paths, commands, error messages, final results — are " +
	"retained in the surrounding kept content or can be re-derived by rerunning the step. " +
	"Verbose intermediate logs are noise even when they mention relevant words; the final result " +
	"or summary of such a log is essential. " +
	"Answer true only if it is ESSENTIAL: it contains concrete facts a later step may have to " +
	"refer back to — file paths, commands and their outcomes, error messages, decisions, user " +
	"preferences, todo state, numbers or results, or the key fields of an API response — that " +
	"are not retained elsewhere and cannot be re-derived."

// AdmitQuestionTrue is the admit question's true-criterion: what a "true"
// answer asserts about the item.
const AdmitQuestionTrue = "The item carries concrete facts a later step may need — paths, commands, " +
	"errors, decisions, results, or key response fields — that are not retained elsewhere and " +
	"cannot be re-derived."

// AdmitQuestionFalse is the admit question's false-criterion: what a "false"
// answer asserts about the item.
const AdmitQuestionFalse = "The item is progress noise, a confirmation, repeated boilerplate, or a " +
	"verbose dump whose useful facts are retained nearby or re-derivable — eliding it loses " +
	"nothing a later step cannot recover."

// RetrieveActionInjected and RetrieveActionSkipped are the actions recorded
// on kind=retrieve decision entries (the reference's "injected"/"skipped"):
// the entry's record was put into the work area, or it was scored below the
// retrieval threshold and left in the store.
const (
	RetrieveActionInjected = "injected"
	RetrieveActionSkipped  = "skipped"
)

// Production defaults for Retrieve (the reference retrieve() signature:
// k=5, budget_tokens=24_000, threshold=0.5). A non-positive k or budget and
// a threshold outside (0, 1] fall back to these.
const (
	DefaultRetrieveK            = 5
	DefaultRetrieveBudgetTokens = 24_000
	DefaultRetrieveThreshold    = 0.5
)

// RetrieveTask composes the scoring task for one retrieval from the
// caller's task digest. The reference sends RETRIEVE_QUESTION as the
// question and task_digest as the state task; this port's fixed question
// envelope embeds the task, so the framing travels with it: the composed
// string carries the reference question's instructions and true/false
// criteria, then the task digest itself. Deterministic per digest, so
// HashTask of the result groups a digest's retrieval decisions.
func RetrieveTask(taskDigest string) string {
	var b strings.Builder
	b.WriteString(RetrieveQuestionInstructions)
	b.WriteString(" (true: ")
	b.WriteString(RetrieveQuestionTrue)
	b.WriteString(" false: ")
	b.WriteString(RetrieveQuestionFalse)
	b.WriteString(") Task: ")
	b.WriteString(taskDigest)
	return b.String()
}

// DigestEntry is the cheap view of one stored Record (the reference
// types.py DigestEntry): small enough that hundreds fit in one scoring
// state. Summary is what gets scored; Tokens is the record's own token
// count — the budget packs by it (conservative: the summaries actually sent
// are far smaller than the runs they stand for).
type DigestEntry struct {
	ID          string
	Summary     string
	Kind        string
	Tokens      int
	CreatedTurn int
}

// Digest builds the retrieval digest: one cheap entry per record, packed
// under budgetTokens. Port of the reference store's digest(budget_tokens)
// (the plan's Step 17 spec — the vendored reference trimmed store.py, so
// the wording there is the source of truth):
//
//   - One entry per record. Summary is the record's pointer summary; a
//     record without one falls back to a truncated first line
//     (Summarise of the record text at the pointer-summary limit).
//   - Drop oldest-first when over budget: entries start in first-appearance
//     (oldest first) order and the oldest are dropped until the token sum
//     fits, so a store larger than the budget keeps its NEWEST records —
//     the ones least likely to be superseded. A non-positive budget keeps
//     nothing (nothing fits a non-positive budget); Retrieve normalizes
//     its budget before calling.
//   - Largest-last: the returned entries are ordered ascending by tokens
//     (stable — equal sizes keep their first-appearance order), so the big
//     summaries sit at the end of the digest. Scoring batches by id, so
//     this order is presentation-only; it is pinned by test anyway.
func (s *Store) Digest(budgetTokens int) []DigestEntry {
	if s == nil {
		return nil
	}
	records := s.Records() // first-appearance order: oldest first
	entries := make([]DigestEntry, 0, len(records))
	for _, rec := range records {
		summary := rec.Summary
		if summary == "" {
			// No pointer summary (a record written before summaries were
			// threaded, or by a writer that skipped it): fall back to a
			// truncated first line of the original.
			summary = Summarise(rec.Text, SummaryMaxChars)
		}
		entries = append(entries, DigestEntry{
			ID:          rec.ID,
			Summary:     summary,
			Kind:        rec.Kind,
			Tokens:      rec.Tokens,
			CreatedTurn: rec.CreatedTurn,
		})
	}

	// Drop oldest-first until the token sum fits the budget.
	total := 0
	for _, e := range entries {
		total += e.Tokens
	}
	drop := 0
	for total > budgetTokens && drop < len(entries) {
		total -= entries[drop].Tokens
		drop++
	}
	entries = entries[drop:]

	// Largest-last ordering (stable, so equal sizes keep first-appearance
	// order).
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Tokens < entries[j].Tokens })
	return entries
}

// RetrievedContextHeader is the first line of the work-area block the
// caller renders from Retrieve's records (session.InjectRetrieved).
const RetrievedContextHeader = "Retrieved context (scored relevant to the current task):"

// RetrievedBlock renders the records as the single context block injected
// into the work area: the header line, then each record's full text
// separated by a "---" rule. Records with empty text are skipped; a
// selection with no text at all renders as an empty string (nothing to
// inject).
func RetrievedBlock(records []Record) string {
	var b strings.Builder
	b.WriteString(RetrievedContextHeader)
	wrote := false
	for _, rec := range records {
		if rec.Text == "" {
			continue
		}
		if wrote {
			b.WriteString("\n---\n")
		} else {
			b.WriteString("\n")
		}
		b.WriteString(rec.Text)
		wrote = true
	}
	if !wrote {
		return ""
	}
	return b.String()
}

// Retrieve scores the store's digest against taskDigest and returns the
// top-k records — the port of the reference pipeline.py retrieve(). k, the
// budget, and the threshold fall back to DefaultRetrieveK,
// DefaultRetrieveBudgetTokens, and DefaultRetrieveThreshold when
// non-positive (or, for the threshold, outside (0, 1]).
//
// The pipeline's own decision log (shadow, when attached) receives one
// kind=retrieve decision entry per scored entry — Action (and Decision)
// "injected" for records that made the cut, "skipped" for the rest — with
// the retrieval threshold recorded so replay tooling can re-run the
// selection from the log. Shadow-log append failures are swallowed:
// logging is best-effort by contract and must not break retrieval (and a
// logging failure must not be reportable as a scoring error — that would
// cancel a selection that already happened).
//
// The error return is non-nil exactly when scoring failed; NO records and
// NO decisions are returned or logged then (see the package-level deviation
// note). A nil store is an empty digest (nil, nil); a nil pipeline (or one
// without a client) is an error, like every other pipeline entry point.
func (p *Pipeline) Retrieve(ctx context.Context, taskDigest string, store *Store, k int, budgetTokens int, threshold float64) ([]Record, error) {
	if p == nil || p.client == nil {
		return nil, fmt.Errorf("compaction: pipeline has no decision client")
	}
	if store == nil {
		return nil, nil
	}
	// Zero values are production defaults (the reference retrieve
	// signature's k=5, budget_tokens=24_000, threshold=0.5).
	if k <= 0 {
		k = DefaultRetrieveK
	}
	if budgetTokens <= 0 {
		budgetTokens = DefaultRetrieveBudgetTokens
	}
	if threshold <= 0 || threshold > 1 {
		threshold = DefaultRetrieveThreshold
	}

	// Auth-poisoned pipeline: scoring is off for the session (the one-time
	// warning was emitted when the rejection was first seen). Retrieval is
	// silently off — no doomed request, no records, no decisions.
	if dead, _ := p.authDisabled(); dead {
		return nil, nil
	}

	entries := store.Digest(budgetTokens)
	if len(entries) == 0 {
		return nil, nil
	}

	task := RetrieveTask(taskDigest)
	items := make(map[string]Item, len(entries))
	for _, e := range entries {
		items[e.ID] = Item{Text: e.Summary, Tokens: e.Tokens}
	}
	scores, err := p.client.ScoreBatch(ctx, task, items)
	if err != nil {
		var ce *Error
		if errors.As(err, &ce) && ce.Kind == KindAuth {
			// The backend refused the credentials (401/403 — the reference's
			// JevAuthError): disable scoring for the session exactly like
			// the other scoring paths, and stop the retrieval here. Nothing
			// was ranked, so nothing is logged.
			p.noteAuthFailure(ce.Error())
			return nil, err
		}
		// Any other scoring failure: the returned scores are (partially)
		// fail-open keep-scores whose only safe use is "keep everything" —
		// the elision direction. Retrieval's mirror-image safe direction is
		// "inject nothing": an unreliable ranking must not stuff the work
		// area with arbitrary records. Abort the read side for this turn.
		return nil, err
	}

	type scoredEntry struct {
		entry DigestEntry
		score float64
	}
	order := make([]scoredEntry, 0, len(entries))
	for _, e := range entries {
		score, ok := scores[e.ID]
		if !ok {
			score = keepScore // defensive; ScoreBatch fills every id
		}
		order = append(order, scoredEntry{entry: e, score: score})
	}
	// Rank: score descending; ties break by ascending record id so the
	// selection is deterministic (map iteration order is not).
	sort.Slice(order, func(i, j int) bool {
		if order[i].score != order[j].score {
			return order[i].score > order[j].score
		}
		return order[i].entry.ID < order[j].entry.ID
	})

	chosen := make(map[string]bool, k)
	records := make([]Record, 0, k)
	for _, r := range order {
		if len(records) == k {
			break
		}
		if r.score < threshold {
			continue
		}
		rec, ok := store.GetRecord(r.entry.ID)
		if !ok {
			continue // vanished between Digest and now; skip
		}
		chosen[r.entry.ID] = true
		records = append(records, *rec)
	}

	// Shadow log: one kind=retrieve decision per scored entry, with the
	// selection threshold recorded so the injected/skipped split can be
	// replayed from the log without re-scoring. Append failures are
	// swallowed (see the doc comment).
	if p.shadow != nil {
		now := p.now()
		taskHash := HashTask(task)
		for _, r := range order {
			action := RetrieveActionSkipped
			if chosen[r.entry.ID] {
				action = RetrieveActionInjected
			}
			// Decision carries the action too: it is what a reader that
			// ignores the newer Action field sees, and "injected"/"skipped"
			// is the honest value for a retrieve decision (the empty
			// default would be rewritten to "keep", admit vocabulary).
			_ = p.shadow.Append(ShadowEntry{
				TS:        now,
				TaskHash:  taskHash,
				SegmentID: r.entry.ID,
				Tokens:    r.entry.Tokens,
				Score:     r.score,
				Decision:  action,
				Type:      EntryTypeDecision,
				Kind:      DecisionKindRetrieve,
				Action:    action,
				Threshold: threshold,
			})
		}
	}
	return records, nil
}
