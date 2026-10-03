package compaction

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultGateConfig_ReferenceParity pins the reference-parity defaults
// (jev-compaction pipeline.py: keep_threshold=0.35, min_gate_tokens=400,
// max_elide_fraction=0.7, protected_kinds={stacktrace,diff} at 0.05).
func TestDefaultGateConfig_ReferenceParity(t *testing.T) {
	g := DefaultGateConfig()
	if g.KeepThreshold != 0.35 {
		t.Errorf("KeepThreshold = %v, want 0.35", g.KeepThreshold)
	}
	if g.MinGateTokens != 400 {
		t.Errorf("MinGateTokens = %d, want 400", g.MinGateTokens)
	}
	if g.MaxElideFraction != 0.7 {
		t.Errorf("MaxElideFraction = %v, want 0.7", g.MaxElideFraction)
	}
	if len(g.ProtectedKinds) != 2 {
		t.Fatalf("ProtectedKinds = %v, want exactly stacktrace and diff", g.ProtectedKinds)
	}
	for _, kind := range []SegmentKind{KindStacktrace, KindDiff} {
		if g.ProtectedKinds[kind] != 0.05 {
			t.Errorf("ProtectedKinds[%q] = %v, want 0.05", kind, g.ProtectedKinds[kind])
		}
	}
}

// TestPipeline_MinGateTokensUnsetUsesDefault: a pipeline without an applied
// gate config runs on the reference default token floor.
func TestPipeline_MinGateTokensUnsetUsesDefault(t *testing.T) {
	p := NewPipeline(ResolvedBackend{}, "", nil, PipelineOptions{})
	if got := p.minGateTokens(); got != DefaultMinGateTokens {
		t.Errorf("minGateTokens() = %d, want the default %d", got, DefaultMinGateTokens)
	}
}

// TestApplyGateConfig_ClampsInvalidValues: out-of-range gate values fall
// back to safe behavior the way the config resolvers do — an invalid keep
// threshold leaves the armed relocation threshold in force, an invalid
// tripwire fraction falls back to the reference default, and per-kind
// floors clamp into [0, 1].
func TestApplyGateConfig_ClampsInvalidValues(t *testing.T) {
	var nilPipeline *Pipeline
	nilPipeline.ApplyGateConfig(DefaultGateConfig()) // must not panic

	p := NewPipeline(ResolvedBackend{}, "", nil, PipelineOptions{})
	p.EnableRelocation(NewStore(), 0.5)
	p.ApplyGateConfig(GateConfig{
		KeepThreshold:    1.5, // invalid → unset
		MinGateTokens:    -10, // invalid → 0 (gate disabled)
		MaxElideFraction: 42,  // invalid → reference default
	})
	got := p.resolveGate()
	if got.keep != 0.5 {
		t.Errorf("keep threshold = %v, want the armed 0.5 to stay in force", got.keep)
	}
	if got.cfg.MaxElideFraction != DefaultMaxElideFraction {
		t.Errorf("MaxElideFraction = %v, want the default %v", got.cfg.MaxElideFraction, DefaultMaxElideFraction)
	}
	if got.cfg.MinGateTokens != 0 {
		t.Errorf("MinGateTokens = %d, want 0", got.cfg.MinGateTokens)
	}
	if got.cfg.ProtectedKinds[KindStacktrace] != DefaultProtectedFloor {
		t.Errorf("nil ProtectedKinds must resolve to the reference defaults, got %v", got.cfg.ProtectedKinds)
	}

	// A valid keep threshold takes over the relocation threshold, exactly
	// as EnableRelocation would.
	p.ApplyGateConfig(GateConfig{KeepThreshold: 0.2})
	if _, threshold := p.relocationArmed(); threshold != 0.2 {
		t.Errorf("relocation threshold = %v, want the applied keep threshold 0.2", threshold)
	}

	// Per-kind floors clamp into [0, 1].
	p.ApplyGateConfig(GateConfig{ProtectedKinds: map[SegmentKind]float64{KindDiff: 7, KindStacktrace: -1}})
	got = p.resolveGate()
	if got.cfg.ProtectedKinds[KindDiff] != 1 || got.cfg.ProtectedKinds[KindStacktrace] != 0 {
		t.Errorf("ProtectedKinds = %v, want floors clamped into [0,1]", got.cfg.ProtectedKinds)
	}
}

// protectedKindOutput builds three separate paragraphs (each over the
// 80-char tiny-paragraph floor): prose, a Go panic trace (kind stacktrace),
// prose again. It fails the test if the middle segment is not classified as
// a stacktrace — the fixture the protected-floor behavior hangs on.
func protectedKindOutput(t *testing.T) (output, traceSegment string) {
	t.Helper()
	prose := strings.Repeat("ordinary prose paragraph. ", 5)
	trace := "goroutine 1 [running]:\nmain.main()\n\t/home/dev/app/main.go:42 +0x1a4\nexit status 2"
	output = prose + "\n\n" + trace + "\n\n" + prose
	segs := SegmentSegments(output, 0)
	if len(segs) != 3 {
		t.Fatalf("SegmentSegments() = %d segments, want 3", len(segs))
	}
	if segs[1].Kind != KindStacktrace {
		t.Fatalf("middle segment Kind = %q, want %q", segs[1].Kind, KindStacktrace)
	}
	return output, segs[1].Text
}

// TestPipeline_ProtectedKindFloor: a stacktrace segment scoring below the
// keep threshold is still kept unless it scores below its own, much lower,
// protected floor (0.05) — the reference's protected_kinds semantics.
func TestPipeline_ProtectedKindFloor(t *testing.T) {
	output, traceSegment := protectedKindOutput(t)

	newArmed := func(stackScore float64) *Pipeline {
		d := newDecisionsServer(t, fixedScoresHandler(map[string]float64{
			"seg-1": 0.9,
			"seg-2": stackScore,
			"seg-3": 0.9,
		}))
		store := NewStore()
		p := NewPipeline(ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}, "k", nil, PipelineOptions{})
		p.EnableRelocation(store, 0.35)
		p.ApplyGateConfig(GateConfig{KeepThreshold: 0.35, MinGateTokens: 0, MaxElideFraction: 0.7})
		return p
	}

	t.Run("between keep threshold and protected floor is kept", func(t *testing.T) {
		// 0.2 is below the 0.35 keep threshold — a plain segment would be
		// elided — but far above the 0.05 stacktrace floor.
		got, err := newArmed(0.2).CompactToolOutput(context.Background(), "Bash", output)
		if err != nil {
			t.Fatalf("CompactToolOutput() error = %v", err)
		}
		if len(got.Elided) != 0 {
			t.Errorf("Elided = %v, want nothing elided (stacktrace protected at 0.2)", got.Elided)
		}
		if got.CompactText != output {
			t.Error("CompactText must be the original byte-for-byte when nothing is elided")
		}
		if got.Tripwire != "" {
			t.Errorf("Tripwire = %q, want empty", got.Tripwire)
		}
	})

	t.Run("below the protected floor is elided", func(t *testing.T) {
		got, err := newArmed(0.04).CompactToolOutput(context.Background(), "Bash", output)
		if err != nil {
			t.Fatalf("CompactToolOutput() error = %v", err)
		}
		if len(got.Elided) != 1 {
			t.Fatalf("Elided = %d segments, want exactly the stacktrace one", len(got.Elided))
		}
		if got.Elided[0].Text != traceSegment {
			t.Errorf("elided text = %q, want the trace segment", got.Elided[0].Text)
		}
		// The trace's tail is past the 60-char pointer preview, so it must
		// not appear in the compact text at all (the preview's first line
		// legitimately does).
		if strings.Contains(got.CompactText, "exit status 2") {
			t.Error("CompactText kept the below-floor stacktrace segment")
		}
	})
}

// TestPipeline_TripwireMaxElideFraction: when the scorer wants to elide more
// than MaxElideFraction of the output's tokens, it is distrusted — nothing
// is elided, the result reports the tripwire, and the shadow log records the
// override alongside (not instead of) the scorer's own decisions.
func TestPipeline_TripwireMaxElideFraction(t *testing.T) {
	newPipeline := func(shadow *ShadowLog, store *Store, maxElide float64) *Pipeline {
		d := newDecisionsServer(t, fixedScoresHandler(map[string]float64{
			"seg-1": 0.01, "seg-2": 0.02, "seg-3": 0.03, // everything below 0.35
		}))
		p := NewPipeline(ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}, "k", shadow, PipelineOptions{})
		p.EnableRelocation(store, 0.35)
		p.ApplyGateConfig(GateConfig{KeepThreshold: 0.35, MinGateTokens: 0, MaxElideFraction: maxElide})
		return p
	}

	output, _, _, _ := relocationOutput()

	t.Run("all elided trips and keeps everything", func(t *testing.T) {
		shadow, err := NewShadowLogAt(filepath.Join(t.TempDir(), "shadow.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		store := NewStore()
		p := newPipeline(shadow, store, 0.7)

		got, err := p.CompactToolOutput(context.Background(), "Bash", output)
		if err != nil {
			t.Fatalf("CompactToolOutput() error = %v", err)
		}
		if got.Tripwire != TripwireMaxElideFraction {
			t.Errorf("Tripwire = %q, want %q", got.Tripwire, TripwireMaxElideFraction)
		}
		if got.CompactText != output {
			t.Error("the tripwire must keep the original output byte-for-byte")
		}
		if len(got.Elided) != 0 {
			t.Errorf("Elided = %v, want empty after the tripwire", got.Elided)
		}
		if store.Len() != 0 {
			t.Error("store must stay empty after the tripwire")
		}

		// The shadow log holds what the scorer wanted (three elide
		// decisions) plus one tripwire entry Replay skips.
		lines := readLines(t, shadow.Path())
		if len(lines) != 4 {
			t.Fatalf("got %d shadow lines, want 3 decisions + 1 tripwire", len(lines))
		}
		tripwires := 0
		for _, line := range lines {
			var e ShadowEntry
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Fatalf("shadow line invalid: %v", err)
			}
			if e.Type != EntryTypeTripwire {
				continue
			}
			tripwires++
			if e.Action != TripwireAction {
				t.Errorf("tripwire Action = %q, want %q", e.Action, TripwireAction)
			}
			if e.SegmentID != "" {
				t.Errorf("tripwire entry must carry no segment id, got %q", e.SegmentID)
			}
		}
		if tripwires != 1 {
			t.Errorf("got %d tripwire entries, want 1", tripwires)
		}

		report, err := shadow.Replay(0.35)
		if err != nil {
			t.Fatalf("Replay() error = %v", err)
		}
		if report.Entries != 3 || report.ElidedEntries != 3 {
			t.Errorf("Replay() = %+v, want the scorer's 3 elide decisions with the tripwire entry skipped", report)
		}
	})

	t.Run("fraction at the limit does not trip", func(t *testing.T) {
		// MaxElideFraction 1 means only an over-100% claim trips (impossible),
		// so the all-elide batch goes through untouched.
		p := newPipeline(nil, NewStore(), 1)
		got, err := p.CompactToolOutput(context.Background(), "Bash", output)
		if err != nil {
			t.Fatalf("CompactToolOutput() error = %v", err)
		}
		if got.Tripwire != "" {
			t.Errorf("Tripwire = %q, want empty", got.Tripwire)
		}
		// All three segments are consecutive: one run, one pointer, three
		// grouped segments.
		if len(got.Elided) != 1 || got.Elided[0].Segments != 3 {
			t.Errorf("Elided = %+v, want one run grouping all 3 segments", got.Elided)
		}
	})
}

// TestPipeline_PartialElisionDoesNotTrip: one of three segments elided is
// normal operation — no tripwire, normal pointer output.
func TestPipeline_PartialElisionDoesNotTrip(t *testing.T) {
	d := newDecisionsServer(t, fixedScoresHandler(map[string]float64{
		"seg-1": 0.9, "seg-2": 0.1, "seg-3": 0.9,
	}))
	store := NewStore()
	p := NewPipeline(ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}, "k", nil, PipelineOptions{})
	p.EnableRelocation(store, 0.35)
	p.ApplyGateConfig(GateConfig{KeepThreshold: 0.35, MinGateTokens: 0, MaxElideFraction: 0.7})

	output, _, _, _ := relocationOutput()
	got, err := p.CompactToolOutput(context.Background(), "Bash", output)
	if err != nil {
		t.Fatalf("CompactToolOutput() error = %v", err)
	}
	if got.Tripwire != "" {
		t.Errorf("Tripwire = %q, want empty", got.Tripwire)
	}
	if len(got.Elided) != 1 {
		t.Errorf("Elided = %d segments, want exactly the low scorer", len(got.Elided))
	}
}

// TestPipeline_MinGateTokensSkipsScoring: an output estimated below
// MinGateTokens is returned unchanged without any backend call and without
// shadow entries — the round trip would cost more than elision could save.
// Disabling the token gate lets the same output through.
func TestPipeline_MinGateTokensSkipsScoring(t *testing.T) {
	var requests int
	d := newDecisionsServer(t, func(_ int, req capturedRequest) (int, string) {
		requests++
		return echoHandler(1, req)
	})
	shadow, err := NewShadowLogAt(filepath.Join(t.TempDir(), "shadow.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	p := NewPipeline(ResolvedBackend{Backend: Backend{Name: "test", URL: d.srv.URL, Model: "jev-latest"}, APIKey: "k"}, "k", shadow, PipelineOptions{})
	p.EnableRelocation(store, 0.35)

	// ~150 chars: at most 150 tokens under any tokenizer, far below the
	// 400-token reference default that applies while no gate config was set.
	output := strings.Repeat("tiny output ", 12)

	got, err := p.CompactToolOutput(context.Background(), "Bash", output)
	if err != nil {
		t.Fatalf("CompactToolOutput() error = %v", err)
	}
	if got.CompactText != output || len(got.Elided) != 0 || got.Tripwire != "" {
		t.Errorf("below MinGateTokens the output must pass through unchanged, got %+v", got)
	}
	if requests != 0 {
		t.Errorf("got %d scoring requests, want 0 below MinGateTokens", requests)
	}
	if store.Len() != 0 {
		t.Error("store must stay empty below MinGateTokens")
	}
	report, err := shadow.Replay(0.35)
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if report.Entries != 0 {
		t.Errorf("shadow entries = %d, want 0 (scoring skipped entirely)", report.Entries)
	}

	// Disabling the token gate lets the very same output through: scored,
	// elided (echoHandler scores seg-1 at 0.01), stored.
	p.ApplyGateConfig(GateConfig{KeepThreshold: 0.35, MinGateTokens: 0, MaxElideFraction: 1})
	got, err = p.CompactToolOutput(context.Background(), "Bash", output)
	if err != nil {
		t.Fatalf("CompactToolOutput() error = %v", err)
	}
	if requests == 0 {
		t.Error("output must be scored once the token gate is disabled")
	}
	if len(got.Elided) != 1 {
		t.Errorf("Elided = %d segments, want the single segment elided", len(got.Elided))
	}
	if got.Tripwire != "" {
		t.Errorf("Tripwire = %q, want empty", got.Tripwire)
	}
	// The single-segment output is one run stored under its content id.
	if _, ok := store.Get(ContentID(output, "Bash", "r")); !ok {
		t.Error("store must hold the elided original once the token gate is disabled")
	}
}
