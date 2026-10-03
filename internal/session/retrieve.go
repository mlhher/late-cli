package session

import (
	"context"

	"late/internal/compaction"
)

// Retrieval read side (implementation_plan.md Step 17, config-gated by
// compaction-retrieval): before a stream request, the orchestrator calls
// InjectRetrieved, which scores the compaction store's digest against the
// current task (the session's own last-user-message task, the same one
// history compaction scores against) and stages the top-k relevant records
// as an ephemeral block. StartStream then appends that block as the LAST
// outgoing message — the tail of the message list is the work area; the
// frozen prefix (the head) is never touched — and nothing reaches History
// or disk: the block is request-scoped and every call overwrites it.

// retrievedContextRole is the role of the ephemeral retrieved-context
// message. "system" marks the block as harness-injected context rather
// than a user turn (a trailing user message would read as a fresh prompt);
// the message exists only in the outgoing request copy, so the transcript
// is never polluted either way.
const retrievedContextRole = "system"

// InjectRetrieved scores the store's digest against the session's current
// task through p.Retrieve and stages the results for the next StartStream
// request. It returns the number of records injected.
//
// Overwrite semantics: every call replaces the staged block — an empty
// selection (nothing relevant, empty store, or a scoring error) clears it,
// so a stale block can never outlive the turn that staged it. Scoring
// errors are returned to the caller (which decides how to warn; the hook
// main installs warns once) and stage nothing: an unreliable ranking must
// not stuff the work area, mirroring Pipeline.Retrieve's abort-on-error
// contract. A nil pipeline or store is a no-op clear.
func (s *Session) InjectRetrieved(ctx context.Context, p *compaction.Pipeline, store *compaction.Store, k, budget int, threshold float64) (int, error) {
	if p == nil || store == nil {
		s.setRetrievedBlock("")
		return 0, nil
	}
	task := compactionTask(s.History)
	records, err := p.Retrieve(ctx, task, store, k, budget, threshold)
	if err != nil {
		s.setRetrievedBlock("")
		return 0, err
	}
	if len(records) == 0 {
		s.setRetrievedBlock("")
		return 0, nil
	}
	s.setRetrievedBlock(compaction.RetrievedBlock(records))
	return len(records), nil
}

// setRetrievedBlock replaces the staged block (thread-safe; an empty string
// clears it).
func (s *Session) setRetrievedBlock(block string) {
	s.retrievedMu.Lock()
	defer s.retrievedMu.Unlock()
	s.retrievedBlock = block
}

// retrievedBlockForRequest returns the staged block for the outgoing
// request ("" when nothing was staged).
func (s *Session) retrievedBlockForRequest() string {
	s.retrievedMu.Lock()
	defer s.retrievedMu.Unlock()
	return s.retrievedBlock
}
