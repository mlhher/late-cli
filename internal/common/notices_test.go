package common

import (
	"strings"
	"testing"
)

func TestIsSystemNotice(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{LoopNoticePrefix + ": you have repeated the same tool calls]", true},
		{"  " + TruncatedNoticePrefix + " due to length limit. Please continue.]", true},
		{"please fix the loop detected earlier", false},
		{"[Something else]", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsSystemNotice(tt.text); got != tt.want {
			t.Errorf("IsSystemNotice(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func TestNoticeTextIsUserFacing(t *testing.T) {
	for _, msg := range []string{
		LoopNoticePrefix + ": you have repeated the same tool calls]",
		TruncatedNoticePrefix + " due to length limit. Please continue.]",
	} {
		got, ok := NoticeText(msg)
		if !ok || got == "" {
			t.Fatalf("NoticeText(%q) = %q, %v", msg, got, ok)
		}
		if got == msg || strings.Contains(strings.ToLower(got), "you have") {
			t.Errorf("user-facing text must not echo the model-addressed message: %q", got)
		}
	}
	if _, ok := NoticeText("hello"); ok {
		t.Error("ordinary text is not a notice")
	}
}
