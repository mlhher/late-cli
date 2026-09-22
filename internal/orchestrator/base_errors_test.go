package orchestrator

import (
	"errors"
	"fmt"
	"late/internal/client"
	"testing"
)

// TestIsBadRequestStatusError verifies detection of terminal HTTP 400s from
// the LLM API, including through the executor's "stream error: ..." wrapping.
func TestIsBadRequestStatusError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "plain error", err: errors.New("connection refused"), want: false},
		{
			name: "wrapped 400 StatusError",
			err: fmt.Errorf("stream error: %w", &client.StatusError{
				StatusCode: 400,
				Status:     "400 Bad Request",
				Body:       "invalid request payload",
			}),
			want: true,
		},
		{
			name: "StatusError 500",
			err:  &client.StatusError{StatusCode: 500, Status: "500 Internal Server Error"},
			want: false,
		},
		{
			name: "StatusError 401",
			err:  &client.StatusError{StatusCode: 401, Status: "401 Unauthorized"},
			want: false,
		},
		{
			name: "double-wrapped 400 StatusError",
			err: fmt.Errorf("outer: %w", fmt.Errorf("stream error: %w", &client.StatusError{
				StatusCode: 400,
				Body:       "messages must alternate",
			})),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBadRequestStatusError(tt.err); got != tt.want {
				t.Errorf("isBadRequestStatusError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
