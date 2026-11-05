package tui

import (
	"testing"

	"late/internal/client"
)

func TestToolCallLabelPlaceholderWhileArgsStream(t *testing.T) {
	m := NewModel(&mockOrchestrator{}, nil, nil)
	call := func(args string) client.ToolCall {
		tc := client.ToolCall{}
		tc.Function.Name = "bash"
		tc.Function.Arguments = args
		return tc
	}

	tests := []struct {
		name      string
		args      string
		streaming bool
		want      string
		preparing bool
	}{
		{"no args yet", "", true, "bash", false},
		{"partial json while streaming", `{"command": "go te`, true, "Preparing bash…", true},
		{"complete json while streaming", `{"command": "ls"}`, true, "bash", false}, // nil registry in mock: falls back to name
		{"partial json once finished is not a placeholder", `{"command": "go te`, false, "bash", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, preparing := m.toolCallLabel(call(tt.args), tt.streaming)
			if got != tt.want || preparing != tt.preparing {
				t.Fatalf("toolCallLabel() = %q, %v; want %q, %v", got, preparing, tt.want, tt.preparing)
			}
		})
	}
}

func TestToolActivityTextOnlyRunningOnceArgsComplete(t *testing.T) {
	if got := toolActivityText("bash", "Preparing bash…", true); got != "Preparing bash…" {
		t.Fatalf("preparing = %q", got)
	}
	if got := toolActivityText("bash", "Executing: ls", false); got != "Executing: ls · running" {
		t.Fatalf("running = %q", got)
	}
}
