package tui

import (
	"reflect"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func hasQuitMsg(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	if reflect.ValueOf(cmd).Pointer() == reflect.ValueOf(tea.Quit).Pointer() {
		return true
	}
	done := make(chan tea.Msg, 1)
	go func() {
		done <- cmd()
	}()
	select {
	case msg := <-done:
		if _, ok := msg.(tea.QuitMsg); ok {
			return true
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				if c != nil && reflect.ValueOf(c).Pointer() == reflect.ValueOf(tea.Quit).Pointer() {
					return true
				}
			}
		}
		return false
	case <-time.After(20 * time.Millisecond):
		return false
	}
}

func ctrlDKeyMsg() tea.Msg {
	return tea.KeyPressMsg(tea.Key{Code: 'd', Mod: tea.ModCtrl})
}

func ctrlCKeyMsg() tea.Msg {
	return tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl})
}

func TestCtrlD_NonEmptyInput_DeletesCharacterForward(t *testing.T) {
	m, _ := newViewportBenchmarkModel(nil)
	m.Input.SetValue("hello")
	m.Input.CursorStart()

	// Ctrl+D with cursor at start: should delete 'h' -> "ello"
	updated, cmd := m.Update(ctrlDKeyMsg())
	model := updated.(Model)

	if hasQuitMsg(cmd) {
		t.Fatal("expected Ctrl+D on non-empty input not to quit")
	}
	if model.Input.Value() != "ello" {
		t.Fatalf("Input.Value() = %q, want %q", model.Input.Value(), "ello")
	}
	if model.QuitArmedUntil != 0 {
		t.Fatalf("QuitArmedUntil = %d, want 0", model.QuitArmedUntil)
	}

	// Move cursor after 'e', delete forward: should delete first 'l' -> "elo"
	model.Input.SetCursorColumn(1)
	updated, cmd = model.Update(ctrlDKeyMsg())
	model = updated.(Model)

	if hasQuitMsg(cmd) {
		t.Fatal("expected Ctrl+D on non-empty input not to quit")
	}
	if model.Input.Value() != "elo" {
		t.Fatalf("Input.Value() = %q, want %q", model.Input.Value(), "elo")
	}
}

func TestCtrlD_MultilineInput_DeletesCharacterForward(t *testing.T) {
	m, _ := newViewportBenchmarkModel(nil)
	m.Input.SetValue("line1\nline2")
	// SetValue leaves cursor on line 2; move up to line 1 column 0
	m.Input.CursorUp()
	m.Input.CursorStart()

	updated, cmd := m.Update(ctrlDKeyMsg())
	model := updated.(Model)

	if hasQuitMsg(cmd) {
		t.Fatal("expected Ctrl+D on multiline input not to quit")
	}
	if model.Input.Value() != "ine1\nline2" {
		t.Fatalf("Input.Value() = %q, want %q", model.Input.Value(), "ine1\nline2")
	}
}

func TestCtrlD_EmptyInput_DoublePressQuits(t *testing.T) {
	m, _ := newViewportBenchmarkModel(nil)
	m.Input.SetValue("")

	// First press: arm exit and show toast
	before := time.Now().UnixMilli()
	updated, cmd := m.Update(ctrlDKeyMsg())
	model := updated.(Model)

	if hasQuitMsg(cmd) {
		t.Fatal("expected first Ctrl+D on empty input not to quit")
	}
	if model.ToastMessage != "Press Ctrl+D again to exit" {
		t.Fatalf("ToastMessage = %q, want %q", model.ToastMessage, "Press Ctrl+D again to exit")
	}
	if model.QuitArmedUntil < before+1900 || model.QuitArmedUntil > before+2500 {
		t.Fatalf("QuitArmedUntil = %d, want ~2000ms after %d", model.QuitArmedUntil, before)
	}

	// Second press within window: quit
	updated, cmd = model.Update(ctrlDKeyMsg())
	_ = updated.(Model)

	if !hasQuitMsg(cmd) {
		t.Fatal("expected second Ctrl+D within window to return tea.Quit")
	}
}

func TestCtrlD_EmptyInput_ExpiredWindowDoesNotQuit(t *testing.T) {
	m, _ := newViewportBenchmarkModel(nil)
	m.Input.SetValue("")

	// First press
	updated, cmd := m.Update(ctrlDKeyMsg())
	model := updated.(Model)
	if hasQuitMsg(cmd) {
		t.Fatal("expected first Ctrl+D not to quit")
	}

	// Simulate expired window by placing QuitArmedUntil in the past
	model.QuitArmedUntil = time.Now().UnixMilli() - 100

	// Second press after expiry: re-arm, do not quit
	updated, cmd = model.Update(ctrlDKeyMsg())
	model = updated.(Model)

	if hasQuitMsg(cmd) {
		t.Fatal("expected Ctrl+D after window expiry not to quit")
	}
	if model.ToastMessage != "Press Ctrl+D again to exit" {
		t.Fatalf("ToastMessage = %q, want %q", model.ToastMessage, "Press Ctrl+D again to exit")
	}
	if model.QuitArmedUntil <= time.Now().UnixMilli() {
		t.Fatalf("QuitArmedUntil = %d, want future timestamp", model.QuitArmedUntil)
	}
}

func TestCtrlD_EmptyInput_StreamingAgent(t *testing.T) {
	m, state := newViewportBenchmarkModel(nil)
	state.State = StateStreaming
	m.Input.SetValue("")

	// First press while agent is streaming: arm exit and show toast
	updated, cmd := m.Update(ctrlDKeyMsg())
	model := updated.(Model)

	if hasQuitMsg(cmd) {
		t.Fatal("expected first Ctrl+D during streaming not to quit")
	}
	if model.ToastMessage != "Press Ctrl+D again to exit" {
		t.Fatalf("ToastMessage = %q, want %q", model.ToastMessage, "Press Ctrl+D again to exit")
	}

	// Second press within window: quit
	updated, cmd = model.Update(ctrlDKeyMsg())
	_ = updated.(Model)

	if !hasQuitMsg(cmd) {
		t.Fatal("expected second Ctrl+D during streaming to quit")
	}
}

func TestCtrlC_QuitsImmediately(t *testing.T) {
	m, _ := newViewportBenchmarkModel(nil)
	m.Input.SetValue("not empty draft")

	updated, cmd := m.Update(ctrlCKeyMsg())
	_ = updated.(Model)

	if !hasQuitMsg(cmd) {
		t.Fatal("expected Ctrl+C to quit immediately")
	}
}

func TestCtrlD_NonChatMode(t *testing.T) {
	m, _ := newViewportBenchmarkModel(nil)
	m.Mode = ViewHelp
	// Even if input had text prior to entering help mode, non-chat mode should arm double-press
	m.Input.SetValue("some text")

	// First press: arm exit
	updated, cmd := m.Update(ctrlDKeyMsg())
	model := updated.(Model)

	if hasQuitMsg(cmd) {
		t.Fatal("expected first Ctrl+D in ViewHelp not to quit")
	}
	if model.ToastMessage != "Press Ctrl+D again to exit" {
		t.Fatalf("ToastMessage = %q, want %q", model.ToastMessage, "Press Ctrl+D again to exit")
	}

	// Second press within window: quit
	updated, cmd = model.Update(ctrlDKeyMsg())
	_ = updated.(Model)

	if !hasQuitMsg(cmd) {
		t.Fatal("expected second Ctrl+D in ViewHelp to quit")
	}
}

func TestCtrlD_DisarmedByDifferentKey(t *testing.T) {
	m, _ := newViewportBenchmarkModel(nil)
	m.Input.SetValue("")

	// First press: arm
	updated, cmd := m.Update(ctrlDKeyMsg())
	model := updated.(Model)

	if hasQuitMsg(cmd) {
		t.Fatal("expected first Ctrl+D not to quit")
	}
	if model.QuitArmedUntil == 0 {
		t.Fatal("expected QuitArmedUntil to be armed")
	}

	// Type another key (e.g. 'a')
	updated, _ = model.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	model = updated.(Model)

	if model.QuitArmedUntil != 0 {
		t.Fatalf("QuitArmedUntil = %d, want 0 after another key press", model.QuitArmedUntil)
	}

	// Clear input back to empty to verify second Ctrl+D does not quit
	model.Input.SetValue("")
	updated, cmd = model.Update(ctrlDKeyMsg())
	model = updated.(Model)

	if hasQuitMsg(cmd) {
		t.Fatal("expected Ctrl+D after disarming not to quit")
	}
	if model.QuitArmedUntil == 0 {
		t.Fatal("expected QuitArmedUntil to be re-armed")
	}
}
