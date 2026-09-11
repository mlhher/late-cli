package tui

import (
	"charm.land/bubbles/v2/textarea"
	"testing"
)

func setupTestModel() Model {
	ti := textarea.New()
	ti.SetValue("")

	m := Model{
		Input:        ti,
		Mode:         ViewChat,
		HistoryIndex: -1,
		InputHistory: []string{"msg1", "msg2", "msg3"},
	}
	return m
}

func TestIsAtExactInputStart(t *testing.T) {
	m := setupTestModel()
	m.Input.SetValue("hello world")

	// Default cursor is at end (col=11)
	m.Input.CursorEnd()
	if m.isAtExactInputStart() {
		t.Error("Expected false at end of input")
	}

	// Move to start (col=0)
	m.Input.SetCursorColumn(0)
	if !m.isAtExactInputStart() {
		t.Error("Expected true at col=0")
	}
}

func TestIsAtExactInputEnd(t *testing.T) {
	m := setupTestModel()
	m.Input.SetValue("hello world")

	// Default cursor is at end (col=11)
	m.Input.CursorEnd()
	if !m.isAtExactInputEnd() {
		t.Error("Expected true at end of input")
	}

	// Move to start (col=0)
	m.Input.SetCursorColumn(0)
	if m.isAtExactInputEnd() {
		t.Error("Expected false at col=0")
	}
}

func TestIsAtExactInputEnd_MultiLine(t *testing.T) {
	m := setupTestModel()
	m.Input.SetValue("line 1\nline 2")

	// By default SetValue moves cursor to the end
	m.Input.CursorEnd() // ensure we are at the end of line 2
	if !m.isAtExactInputEnd() {
		t.Error("Expected true at end of multi-line input")
	}

	// Move to line 0
	m.Input.MoveToBegin()
	m.Input.CursorEnd() // end of line 0
	if m.isAtExactInputEnd() {
		t.Error("Expected false at end of first line of multi-line input")
	}
}
