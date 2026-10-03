package compaction

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"late/internal/common"
)

// Scorer is the batch-scoring interface the Pipeline consumes: score one
// batch of items against one ongoing task. *DecisionClient (the System One
// backend client) is the production implementation; compaction.ScriptedScorer
// is the deterministic offline one (compaction-backend "offline" — demos and
// tests only). The same method set is session.HistoryScorer's, so a pipeline
// scores both the tool-output path and full-history compaction.
type Scorer interface {
	ScoreBatch(ctx context.Context, task string, items map[string]Item) (map[string]float64, error)
}

// The production scoring client is the reference Scorer.
var _ Scorer = (*DecisionClient)(nil)

// Pipeline ties segmentation, scoring, and the shadow log together for the
// tool layer. Stage 1 (shadow-only) is ScoreToolOutput: it records decisions
// and returns scores without ever mutating agent behavior. Stage 2
// (relocation, armed with EnableRelocation) additionally elides low-scoring
// segments from tool results and stores their originals for the expand tool.
type Pipeline struct {
	// client scores every segment batch. Held as the Scorer interface, not
	// the concrete *DecisionClient, so the deterministic offline scorer
	// (NewOfflinePipeline) can drive the same pipeline without HTTP.
	client      Scorer
	shadow      *ShadowLog
	maxSegChars int
	// now is the clock for shadow-log timestamps; a var solely for tests.
	now func() time.Time

	// Relocation state (stage 2). relocMu guards the armed store, the
	// elision threshold, and the gate configuration: one pipeline is shared
	// by the root agent and every subagent, whose tool calls run
	// concurrently. The elide-id counter lives in the Store itself so the
	// session's history compaction shares the same id space (Store.NextID).
	//
	// gate/gateSet hold the GateConfig applied via ApplyGateConfig; unset,
	// the pipeline runs on DefaultGateConfig (see gate.go).
	relocMu   sync.Mutex
	reloc     *Store
	threshold float64
	gate      GateConfig
	gateSet   bool

	// Auth-poison state (the reference's JevAuthError policy): the moment
	// scoring sees an auth-class error (401/403) the pipeline disables
	// scoring for the rest of the session — a bad key will not heal — and
	// emits ONE clear warning no matter how many tool calls trip over it
	// (the sync.Once-style guard: authMu guards dead/reason/warned, so
	// concurrent first failures produce exactly one note). warnTo is where
	// that note goes; os.Stderr in production, swappable in tests.
	authMu     sync.Mutex
	authDead   bool
	authReason string
	authWarned bool
	warnTo     io.Writer

	// warnFn, when installed, receives the same one-time auth warning
	// INSTEAD of warnTo: the TUI wiring installs it (SetWarningSink) so a
	// mid-session poisoning surfaces as a toast instead of a raw stderr
	// write painting over the alt-screen. nil (the default, and every
	// headless/CLI flow) keeps the warnTo behavior.
	warnFn func(msg string)
}

// PipelineOptions tunes the pipeline; zero values are production defaults.
type PipelineOptions struct {
	// MaxSegChars caps one segment's size in bytes (DefaultMaxSegChars when
	// 0 or negative).
	MaxSegChars int
	// HTTPClient overrides the decision client's transport (tests inject
	// fast/recorded transports here). Nil uses the stdlib default with a
	// 30s per-attempt timeout. Only read by NewPipeline (the offline
	// pipeline never sends requests).
	HTTPClient *http.Client
	// Shadow is the shadow log for NewOfflinePipeline, which has no backend
	// parameter to hang one on. NewPipeline takes its shadow as its own
	// third argument and ignores this field; nil (the zero value) disables
	// logging exactly as online.
	Shadow *ShadowLog
}

// NewPipeline builds a shadow-only scoring pipeline. backend must be ready
// to call (ResolveBackend fills in the gateway's URL); apiKey overrides
// backend.APIKey when non-empty. shadow may be nil, which disables logging
// (scores still flow — useful for dry runs).
func NewPipeline(backend ResolvedBackend, apiKey string, shadow *ShadowLog, opts PipelineOptions) *Pipeline {
	c := NewDecisionClient(backend, apiKey)
	if opts.HTTPClient != nil {
		c.http = opts.HTTPClient
	}
	return newPipelineWithScorer(c, shadow, opts.MaxSegChars)
}

// newPipelineWithScorer is the shared constructor: one Scorer behind a
// pipeline with the default segment cap and clock. maxSegChars <= 0 means
// DefaultMaxSegChars.
func newPipelineWithScorer(scorer Scorer, shadow *ShadowLog, maxSegChars int) *Pipeline {
	max := maxSegChars
	if max <= 0 {
		max = DefaultMaxSegChars
	}
	return &Pipeline{client: scorer, shadow: shadow, maxSegChars: max, now: time.Now, warnTo: os.Stderr}
}

// noteAuthFailure records an auth rejection: compaction scoring is disabled
// for the rest of the session (the reference's JevAuthError policy — a bad
// or missing key will not heal within the session), and one clear warning is
// emitted, exactly once. The decision client poisons itself the same way;
// this is the pipeline-side half so the tool path stops calling entirely.
func (p *Pipeline) noteAuthFailure(reason string) {
	if p == nil {
		return
	}
	p.authMu.Lock()
	defer p.authMu.Unlock()
	if p.authDead {
		return
	}
	p.authDead = true
	p.authReason = reason
	if !p.authWarned {
		p.authWarned = true
		// Live-console surface: the installed warning sink (TUI toast) when
		// present, the warnTo writer (os.Stderr) otherwise — the exact
		// pre-sink behavior for headless flows and tests.
		if p.warnFn != nil {
			p.warnFn(fmt.Sprintf("Warning: compaction scoring disabled for this session (%v)\n", reason))
		} else {
			fmt.Fprintf(p.warnTo, "Warning: compaction scoring disabled for this session (%v)\n", reason)
		}
		// Durable record of the poisoning (the warning is ephemeral):
		// best-effort, never fails the pipeline.
		common.LogErrorf("compaction", "scoring disabled for this session (auth): %v", reason)
	}
}

// SetWarningSink installs fn as the live-console surface for the pipeline's
// one-time auth-poison warning: noteAuthFailure routes its note to fn instead
// of warnTo (os.Stderr), so a TUI session can surface the warning as a toast
// without raw text painting over the alt-screen. Passing nil removes the sink
// and restores the stderr fallback. The main() wiring installs it together
// with the retrieval-skip warning's diagSink; the -check-compaction
// preflight and every headless flow run with no sink installed and keep the
// stderr behavior.
func (p *Pipeline) SetWarningSink(fn func(msg string)) {
	p.authMu.Lock()
	defer p.authMu.Unlock()
	p.warnFn = fn
}

// DisableAuth is noteAuthFailure's exported form, for the Step 16 startup
// probe: the probe scores on its own throwaway client, so a typed auth
// rejection there would otherwise reach the live pipeline's client only on
// its first real scoring call. Calling this with the probe's reason applies
// the exact same session-disable + one-warning policy a live rejection takes
// (the reference's JevAuthError policy), before the first tool call can burn
// a doomed request. A nil pipeline is a no-op.
func (p *Pipeline) DisableAuth(reason string) {
	p.noteAuthFailure(reason)
}

// authDisabled reports whether scoring was disabled by an auth rejection,
// together with the reason recorded when it happened.
func (p *Pipeline) authDisabled() (bool, string) {
	if p == nil {
		return false, ""
	}
	p.authMu.Lock()
	defer p.authMu.Unlock()
	return p.authDead, p.authReason
}

// HistoryScorer exposes the pipeline's scorer as the scorer for full-history
// compaction: session.CompactContext scores history segments against the
// ongoing task with the same scorer — and the same retry and fail-open
// contract — the tool-output path uses. A nil pipeline yields nil; callers
// treat that as "compaction unavailable". The concrete type behind the
// interface is the pipeline's decision client (the offline pipeline returns
// its ScriptedScorer); session.HistoryScorer is the same method set.
func (p *Pipeline) HistoryScorer() Scorer {
	if p == nil {
		return nil
	}
	return p.client
}

// SegmentScores is the result of scoring one tool output.
type SegmentScores struct {
	// Segments is the segmentation of the tool output.
	Segments []Segment
	// Scores maps segment ID → score in [0,1]; 1.0 also covers items that
	// failed to score (fail-open). Always populated for every segment.
	Scores map[string]float64
	// Errors lists scoring and shadow-log failures (each scoring failure is
	// an *ItemScoreError keyed by segment ID). Non-fatal by contract.
	Errors []error
	// TaskHash is the digest logged alongside each decision.
	TaskHash string
}

// scoreTask derives the ongoing-task description for one tool output. Stage
// 1 has no ambient task to thread through (the tool layer is wired in the
// relocation stage), so the task is derived from the tool's name; the
// relocation stage can pass a richer task through then.
func scoreTask(toolName string) string {
	return fmt.Sprintf("Preserve what the %s tool output contributed toward the ongoing task.", toolName)
}

// ScoreToolOutput segments the tool output, scores every segment against the
// ongoing task, appends one shadow-log line per segment, and returns the
// per-segment scores.
//
// Nothing is elided or relocated here: in shadow mode every decision is
// recorded as "keep" so Replay() can quantify what a threshold would have
// elided; when relocation is armed (EnableRelocation) below-threshold scores
// are recorded as "elide" — the elision itself happens in CompactToolOutput.
//
// The error return is non-nil exactly when at least one segment failed to
// score (mirroring DecisionClient.ScoreBatch's joined item errors); the
// scores themselves are always usable thanks to fail-open. An auth-class
// error (the reference's JevAuthError, 401/403) additionally disables the
// pipeline's scoring for the rest of the session — one warning is logged,
// and CompactToolOutput stops calling the backend entirely.
func (p *Pipeline) ScoreToolOutput(ctx context.Context, toolName, output string) (SegmentScores, error) {
	var out SegmentScores
	if p == nil || p.client == nil {
		return out, fmt.Errorf("compaction: pipeline has no decision client")
	}

	task := scoreTask(toolName)
	out.TaskHash = HashTask(task)
	out.Segments = SegmentSegments(output, p.maxSegChars)
	if len(out.Segments) == 0 {
		return out, nil
	}

	items := make(map[string]Item, len(out.Segments))
	for _, s := range out.Segments {
		items[s.ID] = Item{Text: s.Text, Tokens: s.Tokens}
	}
	scores, err := p.client.ScoreBatch(ctx, task, items)
	out.Scores = scores
	if err != nil {
		out.Errors = append(out.Errors, err)
		var ce *Error
		if errors.As(err, &ce) && ce.Kind == KindAuth {
			// Auth is a session-level configuration failure: disable
			// scoring (one warning) instead of failing every future tool
			// call against a backend that will only say 401 again. The
			// client poisons itself too, so even direct ScoreBatch users
			// stop hitting the network.
			p.noteAuthFailure(ce.Error())
		}
	}

	// Shadow log: one line per segment. In shadow mode (stage 1) the decision
	// is always keep. When relocation is armed (stage 2), the recorded
	// decision reflects what CompactToolOutput does with this score: elide
	// strictly below the segment's floor (the protected-kind floor when the
	// kind has one, else the relocation threshold), with the same paragraph
	// atomicity (AtomicElideDecisions — minimum sibling score for cut
	// paragraphs) and protected-origin clamping (activate_skill at a 1.0
	// score floor) the elide path applies, so the log stays a faithful
	// replay of the real decisions. Append failures are recorded but never
	// fail the call — logging must not be able to break scoring.
	if p.shadow != nil {
		now := p.now()
		relocStore, _ := p.relocationArmed()
		gate := p.resolveGate()
		source := OriginSourceToolPrefix + toolName
		scoresByID := make([]float64, len(out.Segments))
		floors := make([]float64, len(out.Segments))
		for i, s := range out.Segments {
			score, ok := out.Scores[s.ID]
			if !ok {
				score = keepScore
			}
			scoresByID[i] = protectedScore(source, score)
			floors[i] = gate.floor(s.Kind)
		}
		elided := make([]bool, len(out.Segments))
		decide, decFloors := AtomicDecisionScores(out.Segments, scoresByID, floors)
		for i := range out.Segments {
			elided[i] = decide[i] < decFloors[i]
		}
		if relocStore == nil {
			// Shadow mode (stage 1): relocation is not armed, so every
			// decision is keep — the log records what actually happens.
			for i := range elided {
				elided[i] = false
			}
		}
		for i, s := range out.Segments {
			// The score the decision was made on: the origin-protected
			// clamp (1.0) when one applies, the paragraph-minimum sibling
			// score for pieces of a cut paragraph. Recording the decision's
			// own binding score keeps Stats/Replay (score vs threshold)
			// consistent with the recorded decision.
			score := decide[i]
			// The floor this segment's elide decision turns on: the
			// protected-kind floor when the kind has one, else the
			// relocation threshold in force (the paragraph minimum for
			// grouped pieces — AtomicDecisionScores). Recorded with the
			// entry so Stats, FalseNegativeRate, and ReplayTable can re-run
			// the decision from score vs threshold without re-scoring (0
			// when no threshold is in force — never counts as elided).
			floor := decFloors[i]
			decision := DecisionKeep
			if elided[i] {
				decision = DecisionElide
			}
			if aerr := p.shadow.Append(ShadowEntry{
				TS:        now,
				TaskHash:  out.TaskHash,
				SegmentID: s.ID,
				Tokens:    s.Tokens,
				Score:     score,
				Decision:  decision,
				Threshold: floor,
				Kind:      DecisionKindAdmit,
			}); aerr != nil {
				out.Errors = append(out.Errors, fmt.Errorf("shadow log append for %s: %w", s.ID, aerr))
			}
		}
	}
	return out, err
}
