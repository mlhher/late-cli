package common

import "strings"

// Late injects a few messages into the conversation as user-role messages so
// the model sees them, but they are not typed by the user. The TUI recognises
// them by these prefixes and draws them as notices instead of prompt cards.
const (
	// LoopNoticePrefix starts the warning injected when the model repeats
	// identical tool calls.
	LoopNoticePrefix = "[Loop detected"
	// TruncatedNoticePrefix starts the message injected when the model's
	// output was cut off at the provider's length limit.
	TruncatedNoticePrefix = "[Output truncated"
)

// Text shown to the user in place of the injected message. The injected
// messages are addressed to the model ("you ..."), which would read as if
// aimed at the user, so the TUI shows these instead.
const (
	loopNoticeUserText      = "The model is repeating the same tool calls with the same results. Asked it to try something different; Late will stop the run if it keeps going."
	truncatedNoticeUserText = "The model's output hit the length limit. Asking it to continue."
)

// NoticeText returns the user-facing text for a message injected by Late, and
// whether the message is one. The model still receives the original message.
func NoticeText(text string) (string, bool) {
	text = strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(text, LoopNoticePrefix):
		return loopNoticeUserText, true
	case strings.HasPrefix(text, TruncatedNoticePrefix):
		return truncatedNoticeUserText, true
	}
	return "", false
}

// IsSystemNotice reports whether a user-role message was injected by Late
// rather than typed by the user.
func IsSystemNotice(text string) bool {
	_, ok := NoticeText(text)
	return ok
}
