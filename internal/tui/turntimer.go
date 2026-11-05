package tui

import (
	"fmt"
	"time"
)

// turnTimer times one user turn across all agents: it starts when work
// begins while everything was idle (the user sent a message), keeps running
// while any agent is busy, and freezes the moment the last one goes idle. The
// frozen value stays until the next turn begins, so the info bar can still
// say how long the last turn took.
//
// It is a pointer on the Model so copies of the (value-typed) model share it.
// All access happens on the bubbletea goroutine.
type turnTimer struct {
	busy  bool
	start time.Time
	end   time.Time // zero while running
}

// observe records the busy state after an update. A message queued while an
// agent is already running never reaches a new idle→busy edge, so it does not
// reset the turn in progress.
func (t *turnTimer) observe(busy bool, now time.Time) {
	if t == nil || busy == t.busy {
		return
	}
	t.busy = busy
	if busy {
		t.start, t.end = now, time.Time{}
		return
	}
	t.end = now
}

// elapsed returns the running or final duration of the current turn and
// whether it is still running. ok is false before the first turn.
func (t *turnTimer) elapsed(now time.Time) (d time.Duration, running, ok bool) {
	if t == nil || t.start.IsZero() {
		return 0, false, false
	}
	if t.busy {
		return max(0, now.Sub(t.start)), true, true
	}
	return max(0, t.end.Sub(t.start)), false, true
}

// turnDuration renders a turn time at second resolution: 7s, 42s, 1m42s,
// 1h02m.
func turnDuration(d time.Duration) string {
	secs := int(d.Round(time.Second) / time.Second)
	switch {
	case secs < 60:
		return fmt.Sprintf("%ds", secs)
	case secs < 3600:
		return fmt.Sprintf("%dm%02ds", secs/60, secs%60)
	default:
		return fmt.Sprintf("%dh%02dm", secs/3600, secs%3600/60)
	}
}
