package session

import (
	"strings"
	"testing"
	"time"
)

// TestFormatSessionDisplay_ShowsProjectDir verifies that the verbose session
// display shows the project directory when recorded, and omits the Project
// line entirely for legacy sessions with an empty WorkingDir.
func TestFormatSessionDisplay_ShowsProjectDir(t *testing.T) {
	base := SessionMeta{
		ID:          "session-20250101-120000",
		Title:       "Test",
		CreatedAt:   time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
		LastUpdated: time.Date(2025, 1, 1, 12, 5, 0, 0, time.UTC),
		HistoryPath: "/tmp/late-sessions/session-20250101-120000.json",
		WorkingDir:  "/tmp/proj-a",
	}

	t.Run("with project dir", func(t *testing.T) {
		out := FormatSessionDisplay(base, true)
		if !strings.Contains(out, "/tmp/proj-a") {
			t.Errorf("expected output to contain project dir %q, got:\n%s", base.WorkingDir, out)
		}
		if !strings.Contains(out, "    Project: /tmp/proj-a") {
			t.Errorf("expected aligned 'Project:' line in output, got:\n%s", out)
		}
	})

	t.Run("without project dir", func(t *testing.T) {
		legacy := base
		legacy.WorkingDir = ""
		out := FormatSessionDisplay(legacy, true)
		if strings.Contains(out, "Project:") {
			t.Errorf("expected no 'Project:' label for legacy session with empty WorkingDir, got:\n%s", out)
		}
		if !strings.Contains(out, "Test") {
			t.Errorf("expected output to still render the session title, got:\n%s", out)
		}
	})
}
