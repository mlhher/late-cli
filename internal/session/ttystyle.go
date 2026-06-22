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

// FormatSessionDisplay formats a session for display with appropriate styling.
func FormatSessionDisplay(meta SessionMeta, verbose bool) string {
	if verbose {
		return formatSessionDisplayVerbose(meta)
	}
	return FormatSessionDisplayCompact(meta)
}

// formatSessionDisplayVerbose returns a detailed multi-line session display.
func formatSessionDisplayVerbose(meta SessionMeta) string {
	lines := []string{
		colorID(fmt.Sprintf("ID: %s", strings.TrimSuffix(meta.ID, ".json"))),
		fmt.Sprintf("    Title:   %s", meta.Title),
		fmt.Sprintf("    Created: %s", meta.CreatedAt.Format("2006-01-02 15:04:05")),
		fmt.Sprintf("    Updated: %s", meta.LastUpdated.Format("2006-01-02 15:04:05")),
		fmt.Sprintf("    Msg #:   %d", meta.MessageCount),
	}
	if meta.LastUserPrompt != "" {
		last := truncateUTF8(meta.LastUserPrompt, 50)
		lines = append(lines, fmt.Sprintf("    Last:    %s", last))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// FormatCompactID formats just the session ID without the .json suffix
func FormatCompactID(id string) string {
	return colorID(fmt.Sprintf("%s", strings.TrimSuffix(id, ".json")))
}

// FormatResumePrompt formats the resume prompt with appropriate styling
func FormatResumePrompt() string {
	return colorBold("To resume, use: late session load <id>")
}

// FormatSessionDisplayCompact formats a session in a single-line compact format
// Shows: ID, Title (truncated), Updated timestamp, and Message count
func FormatSessionDisplayCompact(meta SessionMeta) string {
	// Colorize ID without .json suffix
	id := colorID(strings.TrimSuffix(meta.ID, ".json"))

	// Handle empty title
	title := meta.Title
	if title == "" {
		title = "Untitled Session"
	}
	// Truncate title to 40 characters max
	title = truncateUTF8(title, 40)

	// Format timestamp without seconds
	updated := meta.LastUpdated.Format("2006-01-02 15:04")

	// Use tab-separated values for alignment
	result := fmt.Sprintf("%s\t%s\t%s\t%d", id, title, updated, meta.MessageCount)
	return strings.TrimSpace(result)
}
