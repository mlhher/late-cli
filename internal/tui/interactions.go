package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"late/internal/client"
	"late/internal/common"
	"late/internal/tool"
	"runtime"
)

// TUIInputProvider implements common.InputProvider by sending messages to the TUI.
type TUIInputProvider struct {
	Messenger Messenger
}

func NewTUIInputProvider(messenger Messenger) *TUIInputProvider {
	return &TUIInputProvider{Messenger: messenger}
}

func (p *TUIInputProvider) Prompt(ctx context.Context, req common.PromptRequest) (json.RawMessage, error) {
	if p.Messenger == nil {
		return nil, fmt.Errorf("tui input provider: no messenger available")
	}

	resultCh := make(chan json.RawMessage, 1)
	errCh := make(chan error, 1)

	p.Messenger.Send(PromptRequestMsg{
		OrchestratorID: common.GetOrchestratorID(ctx),
		Request:        req,
		ResultCh:       resultCh,
		ErrCh:          errCh,
	})

	select {
	case result := <-resultCh:
		return result, nil
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// PromptRequestMsg is sent to the TUI to request user input.
type PromptRequestMsg struct {
	OrchestratorID string
	Request        common.PromptRequest
	ResultCh       chan json.RawMessage
	ErrCh          chan error
}

// TUIConfirmMiddleware implements tool confirmation using the TUI.
func TUIConfirmMiddleware(messenger Messenger, reg *common.ToolRegistry) common.ToolMiddleware {
	return func(next common.ToolRunner) common.ToolRunner {
		return func(ctx context.Context, tc client.ToolCall) (string, error) {
			if messenger == nil {
				return next(ctx, tc)
			}

			// Check for unsupervised execution flag in context
			if skip, ok := ctx.Value(common.SkipConfirmationKey).(bool); ok && skip {
				// On Windows, never bypass shell command confirmation.
				if !(runtime.GOOS == "windows" && tc.Function.Name == "bash") {
					approvedCtx := context.WithValue(ctx, common.ToolApprovalKey, true)
					return next(approvedCtx, tc)
				}
			}

			// Check if the tool requires confirmation
			if reg != nil {
				if t := reg.Get(tc.Function.Name); t != nil {
					// Check project-allowed tools (local or global)
					if allowed, _ := tool.LoadAllAllowedTools(); allowed[tc.Function.Name] {
						approvedCtx := context.WithValue(ctx, common.ToolApprovalKey, true)
						return next(approvedCtx, tc)
					}

					// Skip confirmation if the tool doesn't require it based on its own logic
					if !t.RequiresConfirmation(json.RawMessage(tc.Function.Arguments)) {
						return next(ctx, tc)
					}

					// For ShellTool, check if the command is blocked (e.g., cd commands)
					if bashTool, ok := t.(*tool.ShellTool); ok {
						var params struct {
							Command string `json:"command"`
							Cwd     string `json:"cwd"`
						}
						if err := json.Unmarshal([]byte(tc.Function.Arguments), &params); err == nil {
							if blocked, err := bashTool.IsCommandBlocked(params.Command, params.Cwd); blocked {
								return "", bashTool.WrapError(ctx, err)
							}
						}
					}
				}
			}

			// Ask for confirmation for tools that require it
			resultCh := make(chan string, 1)
			errCh := make(chan error, 1)

			messenger.Send(ConfirmRequestMsg{
				OrchestratorID: common.GetOrchestratorID(ctx),
				ToolCall:       tc,
				ResultCh:       resultCh,
				ErrCh:          errCh,
			})

			select {
			case choice := <-resultCh:
				switch choice {
				case "y", "s", "S", "p", "P", "g", "G", "a", "A":
					if t := reg.Get(tc.Function.Name); t != nil {
						if _, ok := t.(*tool.ShellTool); ok {
							var params struct {
								Command string `json:"command"`
							}
							if err := json.Unmarshal([]byte(tc.Function.Arguments), &params); err == nil {
								switch choice {
								case "s", "S":
									tool.SaveSessionAllowedCommand(params.Command)
								case "p", "P", "a":
									_ = tool.SaveAllowedCommand(params.Command, false)
								case "g", "G", "A":
									_ = tool.SaveAllowedCommand(params.Command, true)
								}
							}
						} else {
							switch choice {
							case "s", "S":
								tool.SaveSessionAllowedTool(tc.Function.Name)
							case "p", "P", "a":
								_ = tool.SaveAllowedTool(tc.Function.Name, false)
							case "g", "G", "A":
								_ = tool.SaveAllowedTool(tc.Function.Name, true)
							}
						}
					}
					approvedCtx := context.WithValue(ctx, common.ToolApprovalKey, true)
					return next(approvedCtx, tc)
				case "n":
					return "Tool execution cancelled by user", nil
				default:
					return "Tool execution cancelled by user", nil
				}
			case err := <-errCh:
				return "", err
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}
}

// ConfirmRequestMsg is sent to the TUI to request tool execution confirmation.
type ConfirmRequestMsg struct {
	OrchestratorID string
	ToolCall       client.ToolCall
	ResultCh       chan string
	ErrCh          chan error
}
