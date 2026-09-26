package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// DiagnosticMsg carries mid-session diagnostics (hook timeouts, hook stderr,
// dropped-progress-event notices) that used to be fmt.Fprintf(os.Stderr, ...)
// writes painting raw text over the alt-screen. They must surface as a
// WARNING toast with a 6s expiry and the standard clear tick.

func TestDiagnosticMsgShowsWarningToast(t *testing.T) {
	m := NewModel(&mockOrchestrator{}, nil, nil)
	m.SetSize(120, 30)

	before := time.Now().UnixMilli()
	updated, _ := m.Update(DiagnosticMsg{Text: "late: 44 events dropped (consumer stalled)"})
	m = updated.(Model)

	if !m.ToastWarning {
		t.Fatal("DiagnosticMsg must render as a warning toast")
	}
	if m.ToastMessage != "late: 44 events dropped (consumer stalled)" {
		t.Fatalf("ToastMessage = %q", m.ToastMessage)
	}
	// 6s expiry (± scheduling slack).
	if m.ToastExpireTime < before+5500 || m.ToastExpireTime > before+7000 {
		t.Fatalf("ToastExpireTime = %d, want ~6s after %d", m.ToastExpireTime, before)
	}
	// cmd is always non-nil after Update (present() batches a frame tick),
	// so the clear tick itself is exercised by the expiry above.
	if !strings.Contains(ansi.Strip(m.statusBarView()), "events dropped") {
		t.Fatal("toast text not rendered in the status bar")
	}
}

func TestDiagnosticMsgTruncatesLongText(t *testing.T) {
	m := NewModel(&mockOrchestrator{}, nil, nil)
	m.SetSize(40, 20)

	long := strings.Repeat("x", 200)
	updated, _ := m.Update(DiagnosticMsg{Text: long})
	m = updated.(Model)

	if got := ansi.StringWidth(m.ToastMessage); got > 40 {
		t.Fatalf("toast width = %d, want <= terminal width 40", got)
	}
	if !strings.HasSuffix(m.ToastMessage, "...") {
		t.Fatalf("truncated toast %q must end with an ellipsis", m.ToastMessage)
	}
}

func TestDiagnosticMsgEmptyTextIgnored(t *testing.T) {
	m := NewModel(&mockOrchestrator{}, nil, nil)
	m.SetSize(120, 30)

	updated, _ := m.Update(DiagnosticMsg{Text: ""})
	m = updated.(Model)

	if m.ToastMessage != "" || m.ToastWarning {
		t.Fatalf("empty diagnostic produced toast %q (warning=%v), want nothing", m.ToastMessage, m.ToastWarning)
	}
}

// TestStaleTickDoesNotClearNewerDiagnosticToast pins the overlap fix: hook
// diagnostics fire in bursts, and each toast schedules its own 6s clear tick.
// When a second toast replaces the first, the FIRST toast's tick still fires
// later — it must not clear the second toast early. The clear handler now
// ignores a clear while the current toast has not yet expired; only a tick
// arriving at/after the live toast's own expiry (or a direct clear with no
// live toast) clears it.
func TestStaleTickDoesNotClearNewerDiagnosticToast(t *testing.T) {
	m := NewModel(&mockOrchestrator{}, nil, nil)
	m.SetSize(120, 30)

	// First toast schedules its tick (cmd1 is not run — in production it
	// fires 6s later, while newer toasts have replaced this one).
	updated, cmd1 := m.Update(DiagnosticMsg{Text: "first diagnostic"})
	m = updated.(Model)
	if cmd1 == nil {
		t.Fatal("the first toast must schedule a clear tick")
	}

	// A second diagnostic lands 1ms later (two hooks timing out together).
	updated, cmd2 := m.Update(DiagnosticMsg{Text: "second diagnostic"})
	m = updated.(Model)
	if m.ToastMessage != "second diagnostic" {
		t.Fatalf("ToastMessage = %q, want the second toast", m.ToastMessage)
	}
	if cmd2 == nil {
		t.Fatal("the second toast must schedule its own clear tick")
	}

	// The stale tick from the FIRST toast fires now: the second toast must
	// survive it (previously the unconditional clear killed it ~6s early).
	updated, _ = m.Update(clearToastMsg{})
	m = updated.(Model)
	if m.ToastMessage != "second diagnostic" {
		t.Fatalf("stale tick cleared the newer toast: got %q", m.ToastMessage)
	}
	if !m.ToastWarning {
		t.Fatal("the surviving toast must keep its warning styling")
	}

	// Once the live toast's own expiry has passed (simulated by rewinding
	// ToastExpireTime — the same wall-clock comparison the handler uses), a
	// clear tick does clear it.
	m.ToastExpireTime = time.Now().UnixMilli() - 1
	updated, _ = m.Update(clearToastMsg{})
	m = updated.(Model)
	if m.ToastMessage != "" || m.ToastWarning {
		t.Fatalf("expired toast not cleared: %q (warning=%v)", m.ToastMessage, m.ToastWarning)
	}
}

// TestToastMsgReplacesDiagnosticToastWithoutStaleClear pins the same
// guarantee across toast KINDS: a long-lived ToastMsg can land while a 6s
// diagnostic toast is alive, and the diagnostic's stale tick must not
// truncate it.
func TestToastMsgReplacesDiagnosticToastWithoutStaleClear(t *testing.T) {
	m := NewModel(&mockOrchestrator{}, nil, nil)
	m.SetSize(120, 30)

	updated, _ := m.Update(DiagnosticMsg{Text: "hook timed out"})
	m = updated.(Model)

	// A long-lived guidance toast arrives while the diagnostic toast is
	// alive (e.g. a long guidance message that stays up for 8s).
	guidance := ToastMsg{Text: "long-lived guidance toast", Warning: true, Duration: 8 * time.Second}
	updated, _ = m.Update(guidance)
	m = updated.(Model)
	if m.ToastMessage != "long-lived guidance toast" {
		t.Fatalf("ToastMessage = %q, want the guidance toast", m.ToastMessage)
	}

	// The diagnostic toast's stale 6s tick must not clear the guidance.
	updated, _ = m.Update(clearToastMsg{})
	m = updated.(Model)
	if m.ToastMessage != "long-lived guidance toast" {
		t.Fatalf("stale diagnostic tick truncated the guidance toast: got %q", m.ToastMessage)
	}
}
