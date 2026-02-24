package session

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// colorize wraps a string with ANSI color codes if output is a TTY
func colorize(s string, code string) string {
	if term.IsTerminal(int(os.Stdout.Fd())) {
		return code + s + "\033[0m"
	}
	return s
}

// colorID returns the ID string with blue color if TTY
func colorID(id string) string {
	return colorize(id, "\033[36m")
}

// colorBold returns the string in bold if TTY
func colorBold(s string) string {
	return colorize(s, "\033[1m")
}

// truncateUTF8 safely truncates a string to maxLen runes, handling UTF-8 characters
func truncateUTF8(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	// Use rune count for safe truncation
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// FormatSessionDisplay formats a session for display with appropriate styling
func FormatSessionDisplay(meta SessionMeta) string {
	var lines []string
	lines = append(lines, colorID(fmt.Sprintf("ID: %s", strings.TrimSuffix(meta.ID, ".json"))))
	lines = append(lines, fmt.Sprintf("    Title:   %s", meta.Title))
	lines = append(lines, fmt.Sprintf("    Created: %s", meta.CreatedAt.Format("2006-01-02 15:04:05")))
	lines = append(lines, fmt.Sprintf("    Updated: %s", meta.LastUpdated.Format("2006-01-02 15:04:05")))
	lines = append(lines, fmt.Sprintf("    Msg #:   %d", meta.MessageCount))
	if meta.LastUserPrompt != "" {
		last := meta.LastUserPrompt
		if len([]rune(last)) > 50 {
			last = truncateUTF8(last, 50)
		}
		lines = append(lines, fmt.Sprintf("    Last:    %s", last))
	}
	return strings.Join(lines, "\n")
}

// FormatResumePrompt formats the resume prompt with appropriate styling
func FormatResumePrompt() string {
	return colorBold("To resume, use: late session load <id>")
}
