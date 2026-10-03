package compaction

import (
	"context"
	"crypto/sha256"
	"fmt"
)

// The offline scripted scorer (implementation_plan.md Step 18): a
// deterministic, fully local stand-in for the System One decision backend so
// the whole compaction flow — shadow scoring, the gate, pointers, the expand
// round trip, history compaction, the preflight — can be tried WITHOUT an
// API key or network access, exactly like the reference repo's testing.py,
// which drives the same flow against a scripted echo scorer.
//
// FOR DEMOS AND TESTS ONLY. The scripted scores measure nothing about
// essentiality — they are a hash of the input — so anything elided by them is
// elided by hash luck, not by judgment. Never wire it as a production
// default: production compaction always goes through ResolveBackendEnv and
// the DecisionClient; "offline" is an explicit config.json choice
// (compaction-backend: "offline") that exists to make the flow demonstrable.

// Scorer is the batch-scoring interface the Pipeline consumes; see
// pipeline.go. ScriptedScorer implements it, as does *DecisionClient (the
// compile-time assertion lives next to the interface).

// OfflineBackendName is the compaction-backend value that selects the
// scripted scorer. It lives here so the resolver, main, and docs spell the
// one string the same way. (internal/config re-exports it as
// CompactionBackendOffline for its own API surface.)
const OfflineBackendName = "offline"

// ScriptedScorer scores every item deterministically from its content: the
// first byte of sha256(task + "\x00" + text) mapped into [0, 1) (byte/256 —
// 1.0 is unreachable, so the keep score never collides with a scripted
// score). Same task and text → same score, always, in every process and on
// every platform; no randomness, no clock, no I/O. It never fails and never
// touches the network, so the pipeline's fail-open path stays cold offline.
//
// The zero value is ready to use. It is safe for concurrent use (no state).
//
// Demos and tests only — see the package-level note above.
type ScriptedScorer struct{}

// ScoreBatch implements Scorer with the deterministic content hash described
// on the type. An empty task falls back to the client's defaultTask so the
// score depends only on content even when the caller has no task text (the
// same normalization DecisionClient applies).
func (ScriptedScorer) ScoreBatch(_ context.Context, task string, items map[string]Item) (map[string]float64, error) {
	if task == "" {
		task = defaultTask
	}
	scores := make(map[string]float64, len(items))
	for id, it := range items {
		scores[id] = scriptedScore(task, it.Text)
	}
	return scores, nil
}

// scriptedScore maps sha256(task + "\x00" + text)'s first byte into [0, 1).
// Stable across processes by construction: sha256 is unkeyed and the string
// encoding is Go-independent UTF-8.
func scriptedScore(task, text string) float64 {
	sum := sha256.Sum256([]byte(task + "\x00" + text))
	return float64(sum[0]) / 256.0
}

// NewOfflinePipeline builds a Pipeline over the ScriptedScorer: the same
// segmentation, gate, shadow log, pointer, and expand machinery as
// NewPipeline, with scoring done locally and deterministically instead of
// through the decisions protocol. No backend is resolved, no API key is
// consulted, and no request is ever sent — the pipeline is safe to drive in
// an airgapped environment. opts.Shadow behaves exactly as NewPipeline's
// shadow argument (a nil shadow disables logging); opts.MaxSegChars falls
// back to DefaultMaxSegChars.
//
// Demos and tests only: the scripted scorer's scores do not measure
// essentiality (see the package-level note).
func NewOfflinePipeline(opts PipelineOptions) *Pipeline {
	return newPipelineWithScorer(ScriptedScorer{}, opts.Shadow, opts.MaxSegChars)
}

// offlineBackendDetail is stage 0's opening line in the offline preflight
// report: there is no endpoint, no model, and no key — the scorer is the
// compiled-in scripted one.
func offlineBackendDetail() string {
	return fmt.Sprintf("backend %q url none model %q (api key: none — deterministic offline scorer; no network)",
		OfflineBackendName, "scripted-scorer")
}

// RunPreflightOffline runs the same three preflight stages as RunPreflight
// (questions, gate, expand) against the ScriptedScorer: everything runs
// locally, no backend is resolved, and no request is sent, so the check
// passes without an API key or network. It backs `-check-compaction` when
// config.json selects compaction-backend "offline" — the demo path's
// self-test. The boolean is the overall verdict; the stage contract
// (stop at the first failure, stage 0 first) is RunPreflight's. ctx only
// bounds the local work (cancellation), never network I/O — there is none.
func RunPreflightOffline(ctx context.Context) ([]CheckResult, bool) {
	return runPreflightStages(ctx, ScriptedScorer{}, offlineBackendDetail())
}
