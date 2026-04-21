package tui

import (
	"context"
	"fmt"
	"late/internal/client"
	"late/internal/common"
	"late/internal/tool"
	"runtime"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

type mockMessenger struct {
	confirmCalled bool
	autoConfirm   *bool
}

func (m *mockMessenger) Send(msg tea.Msg) {
	if req, ok := msg.(ConfirmRequestMsg); ok {
		m.confirmCalled = true
		if m.autoConfirm != nil {
			req.ResultCh <- *m.autoConfirm
		}
	}
}

func TestTUIConfirmMiddleware_SkipConfirmation(t *testing.T) {
	messenger := &mockMessenger{}
	reg := common.NewToolRegistry()
	bashTool := &tool.ShellTool{}
	reg.Register(bashTool)

	middleware := TUIConfirmMiddleware(messenger, reg)

	var approvedSeen bool

	// Next runner just returns success
	next := func(ctx context.Context, tc client.ToolCall) (string, error) {
		if approved, ok := ctx.Value(common.ToolApprovalKey).(bool); ok && approved {
			approvedSeen = true
		}
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
		approvedSeen = false
		ctx := context.WithValue(context.Background(), common.SkipConfirmationKey, true)

		if runtime.GOOS == "windows" {
			// Windows policy: shell commands must still go through confirmation.
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			_, _ = runner(ctx, tc)
			if !messenger.confirmCalled {
				t.Errorf("Expected confirmation to be requested on Windows")
			}
			if approvedSeen {
				t.Errorf("Did not expect approval marker when confirmation was not granted")
			}
			return
		}

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
		if !approvedSeen {
			t.Errorf("Expected middleware to mark tool execution as approved in context")
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

func TestTUIConfirmMiddleware_ConfirmedExecutionMarksApproval(t *testing.T) {
	autoConfirm := true
	messenger := &mockMessenger{autoConfirm: &autoConfirm}
	reg := common.NewToolRegistry()
	bashTool := &tool.ShellTool{}
	reg.Register(bashTool)

	middleware := TUIConfirmMiddleware(messenger, reg)

	next := func(ctx context.Context, tc client.ToolCall) (string, error) {
		approved, ok := ctx.Value(common.ToolApprovalKey).(bool)
		if !ok || !approved {
			return "", fmt.Errorf("missing approval marker")
		}
		return "success", nil
	}

	runner := middleware(next)

	tc := client.ToolCall{
		Function: client.FunctionCall{
			Name:      "bash",
			Arguments: `{"command": "wget https://example.com"}`,
		},
	}

	result, err := runner(context.Background(), tc)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "success" {
		t.Fatalf("Expected result 'success', got %q", result)
	}
	if !messenger.confirmCalled {
		t.Fatalf("Expected confirmation to be requested")
	}
}
