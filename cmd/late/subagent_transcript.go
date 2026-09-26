package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"late/internal/client"
	"late/internal/session"
)

// subagentTranscriptSource is the slice of the subagent orchestrator the
// transcript writer needs. It is satisfied directly by common.Orchestrator —
// the interface type agent.NewSubagentOrchestrator returns — and by the stubs
// used in the tests.
type subagentTranscriptSource interface {
	ID() string
	History() []client.ChatMessage
}

// sessionPathSource is implemented by orchestrators that expose their session
// (*orchestrator.BaseOrchestrator, the concrete type behind the interface,
// does). The transcript writer uses it opportunistically to place the file
// next to the child's own history.
type sessionPathSource interface {
	Session() *session.Session
}

const (
	// transcriptFileMode and transcriptDirMode keep subagent transcripts as
	// private as the rest of the session artifacts.
	transcriptFileMode os.FileMode = 0o600
	transcriptDirMode  os.FileMode = 0o700

	// maxTranscriptBytes caps the rendered transcript. When the full body
	// overflows, only the head and the tail of the conversation are kept.
	maxTranscriptBytes = 64 * 1024

	// transcriptKeepHead and transcriptKeepTail are the message windows kept
	// when the cap overflows: the head shows how the subagent was briefed,
	// the tail is what resuming needs.
	transcriptKeepHead = 2
	transcriptKeepTail = 6

	// truncationMarker is appended by clip whenever content was cut.
	truncationMarker = " […truncated…]"
)

// writeSubagentTranscript renders the child's pruned conversation and writes
// it next to the child's own history file (or in the user cache directory
// when the child session has no history path, i.e. subagent history
// persistence is off). It returns the absolute path of the written file.
func writeSubagentTranscript(child subagentTranscriptSource, agentType, goal, cause string) (string, error) {
	sessionPath := ""
	if sp, ok := child.(sessionPathSource); ok {
		if sess := sp.Session(); sess != nil {
			sessionPath = sess.HistoryPath
		}
	}
	dir := transcriptDir(sessionPath)
	if err := os.MkdirAll(dir, transcriptDirMode); err != nil {
		return "", fmt.Errorf("create transcript dir: %w", err)
	}
	path := filepath.Join(dir, sanitizeFilename(child.ID())+"-transcript.md")
	content := renderTranscript(child.History(), agentType, goal, cause, child.ID())
	if err := os.WriteFile(path, []byte(content), transcriptFileMode); err != nil {
		return "", fmt.Errorf("write transcript: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path, nil
	}
	return abs, nil
}

// transcriptDir resolves where subagent transcripts live. It prefers the
// directory holding the given history file so transcripts sit with the other
// session artifacts: a plain history path yields <dir>/subagents, and a path
// already inside a "subagents" folder (how persisted subagent histories are
// laid out) is used as-is instead of nesting a second subagents directory.
// An empty path falls back to the user cache directory, then to the system
// temp directory when even the cache dir cannot be resolved.
func transcriptDir(historyPath string) string {
	if historyPath != "" {
		dir := filepath.Dir(historyPath)
		if filepath.Base(dir) == "subagents" {
			return dir
		}
		return filepath.Join(dir, "subagents")
	}
	if cache, err := os.UserCacheDir(); err == nil && cache != "" {
		return filepath.Join(cache, "late", "subagent-transcripts")
	}
	return filepath.Join(os.TempDir(), "late-subagent-transcripts")
}

// sanitizeFilename keeps only [A-Za-z0-9._-] from the child ID and replaces
// everything else with '_' so the identifier can never escape the transcripts
// directory. An empty result falls back to a fixed name.
func sanitizeFilename(id string) string {
	var sb strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			sb.WriteRune(r)
		default:
			sb.WriteByte('_')
		}
	}
	if name := sb.String(); name != "" {
		return name
	}
	return "subagent"
}

// renderTranscript renders the child conversation as a pruned Markdown
// transcript. It is pure (no filesystem access): the only non-deterministic
// part is the header timestamp. When the rendered body exceeds
// maxTranscriptBytes it is re-rendered keeping only the first
// transcriptKeepHead and last transcriptKeepTail messages — never sliced
// mid-message — so the parent still sees how the run was briefed and what it
// was doing when it stopped.
func renderTranscript(msgs []client.ChatMessage, agentType, goal, cause, childID string) string {
	var header strings.Builder
	fmt.Fprintf(&header, "# Subagent transcript: %s\n\n", childID)
	fmt.Fprintf(&header, "- Agent type: %s\n", agentType)
	fmt.Fprintf(&header, "- Goal: %s\n", singleLine(goal))
	fmt.Fprintf(&header, "- Cause: %s\n", cause)
	fmt.Fprintf(&header, "- Recorded (UTC): %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&header, "- Messages: %d\n", len(msgs))
	header.WriteString("Pruned transcript: images, long tool outputs and reasoning are omitted.\n")

	body := renderMessages(msgs, 0, len(msgs))
	if len(header.String())+len(body) <= maxTranscriptBytes || len(msgs) <= transcriptKeepHead+transcriptKeepTail {
		// Either it fits, or there is nothing left to drop: the per-message
		// clips already bound each entry, and pruning below the kept window
		// would mean dropping the entire head or tail.
		return header.String() + body
	}

	omitted := len(msgs) - transcriptKeepHead - transcriptKeepTail
	var pruned strings.Builder
	pruned.WriteString(renderMessages(msgs, 0, transcriptKeepHead))
	fmt.Fprintf(&pruned, "\n[… %d earlier messages omitted …]\n", omitted)
	pruned.WriteString(renderMessages(msgs, len(msgs)-transcriptKeepTail, len(msgs)))
	return header.String() + pruned.String()
}

// renderMessages renders msgs[start:stop] with their original 1-based
// numbering so pruned transcripts keep stable message numbers.
func renderMessages(msgs []client.ChatMessage, start, stop int) string {
	var sb strings.Builder
	for i := start; i < stop; i++ {
		fmt.Fprintf(&sb, "\n## [%d] %s\n", i+1, msgs[i].Role)
		sb.WriteString(renderMessage(msgs[i]))
	}
	return sb.String()
}

// renderMessage renders one message body (without its "## [n] role"
// heading), applying the per-role clipping rules. Reasoning content is
// deliberately dropped everywhere: it is internal model scratch space and
// often enormous.
func renderMessage(m client.ChatMessage) string {
	var sb strings.Builder
	switch m.Role {
	case "system":
		sb.WriteString(clip(renderMessageContent(m.Content), 500))
	case "user":
		sb.WriteString(clip(renderMessageContent(m.Content), 2000))
	case "assistant":
		sb.WriteString(clip(renderMessageContent(m.Content), 4000))
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&sb, "\n→ tool %s(%s)", tc.Function.Name, clip(tc.Function.Arguments, 500))
		}
	case "tool":
		fmt.Fprintf(&sb, "tool result for %s:\n", m.ToolCallID)
		sb.WriteString(clip(renderMessageContent(m.Content), 2000))
	default:
		sb.WriteString(clip(renderMessageContent(m.Content), 2000))
	}
	sb.WriteString("\n")
	return sb.String()
}

// renderMessageContent flattens a message's content into text. Text parts
// are kept verbatim; image parts and any other attachment kinds collapse to
// placeholders so no base64 payloads can leak into the transcript.
func renderMessageContent(c client.MessageContent) string {
	if len(c.Parts) == 0 {
		return c.Text
	}
	var sb strings.Builder
	for _, part := range c.Parts {
		switch part.Type {
		case client.ContentPartText:
			sb.WriteString(part.Text)
			sb.WriteString("\n")
		case client.ContentPartImageURL:
			sb.WriteString("[image omitted]\n")
		default:
			sb.WriteString("[attachment omitted]\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// lastActionPreview summarizes what the subagent was doing when it stopped:
// the content of the last assistant message, or — when the assistant only
// announced work — its last tool call. Messages without any assistant signal
// yield a fixed hint.
func lastActionPreview(msgs []client.ChatMessage, limit int) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != "assistant" {
			continue
		}
		if text := strings.TrimSpace(renderMessageContent(m.Content)); text != "" {
			return clip(text, limit)
		}
		if len(m.ToolCalls) > 0 {
			tc := m.ToolCalls[len(m.ToolCalls)-1]
			return clip(fmt.Sprintf("→ tool %s(%s)", tc.Function.Name, tc.Function.Arguments), limit)
		}
	}
	return "no actions recorded"
}

// clip limits s to max bytes without splitting a multi-byte rune, appending
// a truncation marker whenever anything was cut.
func clip(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := s[:max]
	// cut can end mid-rune: trim the incomplete trailing rune byte by byte
	// (a dangling start byte is as invalid an ending as a lone continuation
	// byte) so the cut never ends inside a multi-byte rune.
	for len(cut) > 0 {
		if r, size := utf8.DecodeLastRuneInString(cut); r == utf8.RuneError && size <= 1 {
			cut = cut[:len(cut)-1]
			continue
		}
		break
	}
	return cut + truncationMarker
}

// singleLine collapses a multi-line string into one line so header entries
// stay compact and parseable.
func singleLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
