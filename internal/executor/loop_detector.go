package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"late/internal/client"
	"late/internal/common"
)

// DefaultLoopThreshold is the number of consecutive identical tool-call turns
// (same calls AND same results) after which Late considers the model stuck in a
// loop. It is deliberately conservative so legitimate polling (e.g. re-checking
// a still-running build) is not flagged. Overridable via -loop-threshold /
// LATE_LOOP_THRESHOLD; 0 or a negative value disables detection.
const DefaultLoopThreshold = 5

// ErrRepeatedToolCalls is returned by RunLoop when the model keeps repeating
// the same tool calls even after being warned.
var ErrRepeatedToolCalls = errors.New("the model kept repeating the same tool calls with identical results, so Late stopped the run. Send a message to continue, or check your sampling settings (temperature, context shifting)")

const loopNudgeMessage = common.LoopNoticePrefix + ": you have repeated the same tool calls with identical results several times in a row. " +
	"Repeating them again will not change the outcome. Try a different approach, or stop and ask the user for help.]"

type loopVerdict int

const (
	loopNone loopVerdict = iota
	loopNudge
	loopStop
)

// loopDetector tracks one signature per assistant turn that issued tool calls.
// A turn is a loop iteration when the last N signatures repeat with period 1
// (A,A,A) or period 2 (A,B,A,B,A,B). Any deviation resets the detector.
type loopDetector struct {
	threshold int
	sigs      []string
	nudged    bool
}

func newLoopDetector(threshold int) *loopDetector {
	return &loopDetector{threshold: threshold}
}

func (d *loopDetector) enabled() bool { return d != nil && d.threshold > 0 }

// record adds a turn signature and reports what to do about it. The first
// detection yields loopNudge; if the repetition continues, loopStop.
func (d *loopDetector) record(sig string) loopVerdict {
	if !d.enabled() {
		return loopNone
	}
	d.sigs = append(d.sigs, sig)
	if max := 2 * d.threshold; len(d.sigs) > max {
		d.sigs = d.sigs[len(d.sigs)-max:]
	}
	if !d.repeating() {
		d.nudged = false
		return loopNone
	}
	if d.nudged {
		return loopStop
	}
	d.nudged = true
	return loopNudge
}

func (d *loopDetector) repeating() bool {
	for period := 1; period <= 2; period++ {
		n := period * d.threshold
		if len(d.sigs) < n {
			continue
		}
		w := d.sigs[len(d.sigs)-n:]
		if period == 2 && w[0] == w[1] {
			continue // all-equal windows are the period-1 case
		}
		ok := true
		for i := period; i < len(w); i++ {
			if w[i] != w[i-period] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// turnSignature hashes a turn's tool calls (name + canonicalized arguments) and
// their results, in order. Identical signature means no observable progress.
func turnSignature(calls []client.ToolCall, results []string) string {
	h := sha256.New()
	for i, tc := range calls {
		h.Write([]byte(tc.Function.Name))
		h.Write([]byte{0})
		h.Write([]byte(canonicalArgs(tc.Function.Arguments)))
		h.Write([]byte{0})
		if i < len(results) {
			h.Write([]byte(results[i]))
		}
		h.Write([]byte{1})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// canonicalArgs normalizes JSON (key order, whitespace) so equivalent
// arguments compare equal. Invalid JSON falls back to the trimmed raw string.
func canonicalArgs(raw string) string {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return strings.TrimSpace(raw)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return string(out)
}

// loopThresholdFromContext resolves the detection threshold from ctx, falling
// back to DefaultLoopThreshold. Zero or negative values disable detection.
func loopThresholdFromContext(ctx context.Context) int {
	if v, ok := ctx.Value(common.LoopThresholdKey).(int); ok {
		if v < 0 {
			return 0
		}
		return v
	}
	return DefaultLoopThreshold
}
