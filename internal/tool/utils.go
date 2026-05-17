package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

var (
	sqzEnabled       = true
	sqzAvailable     bool
	sqzAvailableOnce sync.Once

	// isSqzAvailable is a variable holding the function to check for sqz, allowing it to be mocked in tests.
	isSqzAvailable = defaultIsSqzAvailable
)

// SetSqzEnabled enables or disables sqz compression globally.
func SetSqzEnabled(enabled bool) {
	sqzEnabled = enabled
}

// IsSqzAvailable checks if the 'sqz' binary is available in the PATH and enabled.
func IsSqzAvailable() bool {
	if !sqzEnabled {
		return false
	}
	return isSqzAvailable()
}

func defaultIsSqzAvailable() bool {
	sqzAvailableOnce.Do(func() {
		_, err := exec.LookPath("sqz")
		sqzAvailable = (err == nil)
	})
	return sqzAvailable
}

// CompressWithSqz takes raw output and compresses it using the 'sqz' binary if available.
func CompressWithSqz(ctx context.Context, input []byte, command string) ([]byte, error) {
	if !IsSqzAvailable() {
		return input, nil
	}

	// Use --cmd to label the compression for stats
	cmd := exec.CommandContext(ctx, "sqz", "compress", "--cmd", command)
	cmd.Stdin = strings.NewReader(string(input))
	
	// We only want the stdout (the compressed content). 
	// Stderr contains sqz stats which we don't want to pollute the LLM context with.
	return cmd.Output()
}

// getToolParam extracts a string parameter from tool arguments
func getToolParam(args json.RawMessage, key string) string {
	var params map[string]any
	if err := json.Unmarshal(args, &params); err != nil {
		// Fallback for partial JSON during streaming where the unmarshal fails
		re := regexp.MustCompile(fmt.Sprintf(`"%s"\s*:\s*"([^"]*)`, regexp.QuoteMeta(key)))
		matches := re.FindStringSubmatch(string(args))
		if len(matches) > 1 {
			return matches[1]
		}
		return ""
	}
	val, ok := params[key].(string)
	if !ok {
		return ""
	}
	return val
}

// truncate shortens a string to maxLen characters, adding "..." if truncated
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// detectLineEnding returns the dominant line ending of the content.
// If it's consistently CRLF, it returns "\r\n".
// Otherwise (if consistent LF or mixed), it returns "\n".
func detectLineEnding(content string) string {
	crlfCount := strings.Count(content, "\r\n")
	lfCount := strings.Count(content, "\n")

	if crlfCount > 0 && crlfCount == lfCount {
		return "\r\n"
	}
	// Defaults to Unix for LF-only, mixed, or empty content.
	return "\n"
}

// normalizeToUnix converts all CRLF to LF.
func normalizeToUnix(content string) string {
	return strings.ReplaceAll(content, "\r\n", "\n")
}

// restoreLineEnding converts all LF to the specified lineEnding (e.g., "\r\n").
// If lineEnding is "\n", it does nothing.
func restoreLineEnding(content, lineEnding string) string {
	if lineEnding == "\r\n" {
		return strings.ReplaceAll(content, "\n", "\r\n")
	}
	return content
}

// IsBinary detects if the given data is likely binary.
// It checks for the presence of null bytes in the first 8KB of data.
func IsBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	// Check first 8KB for null bytes
	limit := len(data)
	if limit > 8192 {
		limit = 8192
	}
	for i := 0; i < limit; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}
