package session

import "context"

// SetInFlightToolCancel registers the cancel function of the currently
// executing tool call, enabling the owning orchestrator (or the user) to
// cancel a hung tool without killing the whole agent. Nil clears it.
func (s *Session) SetInFlightToolCancel(cancel context.CancelFunc) {
	if cancel == nil {
		s.inFlightToolCancel.Store(nil)
		return
	}
	s.inFlightToolCancel.Store(&cancel)
}

// ClearInFlightToolCancel removes the registered in-flight tool cancel
// function. It must be called once a tool call has finished so a stale cancel
// is never fired against a later tool call.
func (s *Session) ClearInFlightToolCancel() {
	s.inFlightToolCancel.Store(nil)
}

// CancelInFlightTool cancels the in-flight tool call, if any. It returns
// false when no tool is in flight (no cancel function is registered); the
// caller can use that to distinguish "killed the tool" from "nothing to
// kill" when escalating a kill.
func (s *Session) CancelInFlightTool() bool {
	if cancel := s.inFlightToolCancel.Load(); cancel != nil && *cancel != nil {
		(*cancel)()
		return true
	}
	return false
}
