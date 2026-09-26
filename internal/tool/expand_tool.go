package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"late/internal/compaction"
)

// ExpandToolName is the registry name of the compaction expand tool. The
// executor also uses it to exempt expand results from re-compaction: the
// tool exists to return full originals, compacting them again would make
// them unreachable.
const ExpandToolName = "expand"

// ExpandStore is the read side of the compaction original-text store
// (implemented by *compaction.Store). The indirection keeps internal/tool
// decoupled from internal/compaction and lets tests inject a fake.
type ExpandStore interface {
	Get(id string) (string, bool)
}

// expandOutcomeStore is the optional outcome side of the expand store: the
// production *compaction.Store implements it (Touch counters, the attached
// shadow log, record snapshots); minimal test fakes need not. Execute
// type-asserts the store and silently records nothing when it lacks the
// side — outcomes are a ledger, never a precondition for expanding.
type expandOutcomeStore interface {
	// Touch bumps a record's expand/hit counters; reports whether the
	// record exists.
	Touch(id string, expand, hit bool) bool
	// ShadowLog returns the store's attached outcome log, or nil.
	ShadowLog() *compaction.ShadowLog
	// GetRecord snapshots a record — its SegmentIDs attribute the expand
	// back to the score decisions that caused the elision.
	GetRecord(id string) (*compaction.Record, bool)
}

// ExpandTool retrieves the original text of a tool-output run that
// compaction relocated (compaction-mode "enabled"): compacted results
// contain pointer lines like
//
//	[[elided id=r:1a2b3c4d lines=12-40 tokens=310 "first 120 chars of the run …"]]
//
// (content-addressed ids, r:<8 hex>) — or, from older builds,
//
//	[[elided id=elide-3 lines=12-31 tokens=310 "first sixty chars …"]]
//
// (legacy counter ids; the pre-reference count form "lines=12" is not
// parseable and never was — the id alone is what matters here)
//
// and calling this tool with such an id returns the full original text.
// Passing a whole pointer line instead of the bare id works too: the id is
// parsed out of it. It is registered on the main session registry when
// compaction-mode is enabled, and subagents inherit it from the parent
// registry. Each successful retrieval is recorded as an expand outcome (the
// store's Touch counter plus shadow-log outcome lines attributing back to
// the record and its contributing segments) when the store carries a shadow
// log; see recordExpandOutcome.
type ExpandTool struct {
	Store ExpandStore
}

func (t ExpandTool) Name() string { return ExpandToolName }

func (t ExpandTool) Description() string {
	return "Retrieve the ORIGINAL text of an elided (compacted) tool-output run. " +
		"When a large tool result was compacted, history contains pointer lines like " +
		"[[elided id=r:1a2b3c4d lines=12-40 tokens=310 \"first 120 chars of the run\"]] " +
		"(content-addressed id, r: plus 8 hex chars; legacy builds minted elide-N ids) " +
		"instead of the full text. " +
		"Call this tool with that id — or with the whole pointer line — to fetch the complete original."
}

func (t ExpandTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {
				"type": "string",
				"description": "The elided-run id from an [[elided id=...]] pointer line (content id like \"r:1a2b3c4d\", or a legacy \"elide-3\"); a whole pointer line is also accepted."
			}
		},
		"required": ["id"]
	}`)
}

func (t ExpandTool) RequiresConfirmation(args json.RawMessage) bool { return false }

func (t ExpandTool) CallString(args json.RawMessage) string {
	id := expandID(args)
	if id == "" {
		return "Retrieving elided segment..."
	}
	return fmt.Sprintf("Retrieving elided segment %s...", truncate(id, 50))
}

func (t ExpandTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if t.Store == nil {
		return "", fmt.Errorf("no elided-original store is wired — nothing to expand")
	}
	var params struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("invalid parameters for expand: %w", err)
	}
	id := expandIDFromArg(params.ID)
	if id == "" {
		return "", fmt.Errorf("id is required (use an id from an [[elided id=...]] pointer line)")
	}
	original, ok := t.Store.Get(id)
	if !ok {
		return "", fmt.Errorf("unknown elided id %q", id)
	}
	t.recordExpandOutcome(id)
	return original, nil
}

// recordExpandOutcome marks one successful retrieval — every expand is a
// recorded false negative (the reference pipeline.py expand()): the record's
// expand counter moves (Store.Touch), and one "expand" outcome naming the
// record id plus one naming each contributing segment id land in the
// store's attached shadow log, attributing the expand back to the score
// decisions that caused the elision (ShadowLog.FalseNegativeRate and the
// replay table's still-missed column are built on it).
//
// Best-effort and nil-safe by contract: a store without the outcome side
// (test fakes) or without an attached shadow log records nothing, a failed
// outcome append never fails the expand itself, and turn stays 0 (turn
// plumbing does not exist yet, mirroring every other writer).
func (t ExpandTool) recordExpandOutcome(id string) {
	s, ok := t.Store.(expandOutcomeStore)
	if !ok {
		return
	}
	if !s.Touch(id, true, false) {
		return // unknown record: nothing to attribute
	}
	shadow := s.ShadowLog()
	if shadow == nil {
		return // no outcome log attached at wiring: skip silently
	}
	_ = shadow.AppendOutcome(compaction.EntryTypeExpand, id, 0)
	rec, ok := s.GetRecord(id)
	if !ok {
		return
	}
	for _, segID := range rec.SegmentIDs {
		if segID == "" {
			continue
		}
		_ = shadow.AppendOutcome(compaction.EntryTypeExpand, segID, 0)
	}
}

// expandID extracts the id argument from raw tool arguments for the
// progress string; unknown shapes yield "".
func expandID(args json.RawMessage) string {
	var params struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return ""
	}
	return expandIDFromArg(params.ID)
}

// expandIDFromArg normalizes the id argument: surrounding whitespace is
// trimmed, and a whole [[elided …]] pointer line is accepted in place of the
// bare id — its id is parsed out with the shared pointer parser, so content
// ids and legacy counter ids both work.
func expandIDFromArg(arg string) string {
	arg = strings.TrimSpace(arg)
	if strings.Contains(arg, "[[elided") {
		if p, ok := compaction.ParsePointer(arg); ok {
			return p.ID
		}
	}
	return arg
}
