package tui

import (
	"context"
	"late/internal/client"
	"late/internal/common"
	"late/internal/tool"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

type mockMessenger struct {
	confirmCalled bool
}

func (m *mockMessenger) Send(msg tea.Msg) {
	if _, ok := msg.(ConfirmRequestMsg); ok {
		m.confirmCalled = true
	}
}

func TestTUIConfirmMiddleware_SkipConfirmation(t *testing.T) {
	messenger := &mockMessenger{}
	reg := common.NewToolRegistry()
	bashTool := &tool.BashTool{}
	reg.Register(bashTool)

	middleware := TUIConfirmMiddleware(messenger, reg)

	// Next runner just returns success
	next := func(ctx context.Context, tc client.ToolCall) (string, error) {
		return "success", nil
	}

	runner := middleware(next)

	// Tool call that REQUIRES confirmation (e.g. rm)
	tc := client.ToolCall{
		Function: client.FunctionCall{
			Name:      "bash",
			Arguments: `{"command": "wget https://mlgpt.io"}`,
		},
	}

	t.Run("Unsupervised execution skips confirmation", func(t *testing.T) {
		messenger.confirmCalled = false
		ctx := context.WithValue(context.Background(), common.SkipConfirmationKey, true)

		result, err := runner(ctx, tc)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result != "success" {
			t.Errorf("Expected result 'success', got %q", result)
		}
		if messenger.confirmCalled {
			t.Errorf("Expected confirmation to be skipped, but it was requested")
		}
	})

	t.Run("Normal execution still requests confirmation", func(t *testing.T) {
		messenger.confirmCalled = false
		// We use a canceled context to avoid hanging in the select loop of TUIConfirmMiddleware
		// while still verifying that Send() was called before hitting the select.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, _ = runner(ctx, tc)
		if !messenger.confirmCalled {
			t.Errorf("Expected confirmation to be requested")
		}
	})
}
