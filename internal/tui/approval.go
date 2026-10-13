package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"late/internal/client"
	"late/internal/tool"

	"charm.land/lipgloss/v2"
)

// maxApprovalPreviewBytes caps how much of a file (or of proposed content) is
// read and diffed for the approval notice.
const maxApprovalPreviewBytes = 512 * 1024

// denyReasonPrefix marks a ConfirmRequestMsg answer as a denial that carries
// the user's own words after the prefix.
const denyReasonPrefix = "n:"

// approvalPreview is what the tool-approval notice shows for one pending call.
// It is built once when the request arrives (it may read the target file), then
// rendered for whatever width the transcript currently has.
type approvalPreview struct {
	// heading is Markdown: a one-line description and, for commands and tools
	// without a dedicated view, a fenced block of what will run.
	heading string
	// diff is the full line diff for file edits; nil for everything else.
	diff []diffLine
	// note is a short plain remark shown above the diff ("new file · 12 lines",
	// "too large to preview").
	note string
	// remember says what s / p / g would trust beyond this one call.
	remember string
}

// approvalDisplayName is the name the approval notice shows for a tool.
func approvalDisplayName(toolName string) string {
	if runtime.GOOS == "windows" && toolName == "bash" {
		return "PowerShell"
	}
	return toolName
}

func buildApprovalPreview(tc client.ToolCall) *approvalPreview {
	name := approvalDisplayName(tc.Function.Name)
	args := []byte(tc.Function.Arguments)

	switch tc.Function.Name {
	case "bash":
		var p struct {
			Command string `json:"command"`
			Cwd     string `json:"cwd"`
		}
		if json.Unmarshal(args, &p) == nil && p.Command != "" {
			return shellApprovalPreview(name, p.Command, p.Cwd)
		}
	case "write_file":
		var p struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if json.Unmarshal(args, &p) == nil && p.Path != "" {
			return writeFileApprovalPreview(p.Path, p.Content)
		}
	case "target_edit":
		var p struct {
			File    string `json:"file"`
			Search  string `json:"search"`
			Replace string `json:"replace"`
		}
		if json.Unmarshal(args, &p) == nil && p.File != "" {
			return targetEditApprovalPreview(p.File, p.Search, p.Replace)
		}
	}
	return genericApprovalPreview(tc.Function.Name, name, tc.Function.Arguments)
}

// fencedBlock wraps s in a Markdown code fence long enough that backticks inside
// s cannot close it early.
func fencedBlock(lang, s string) string {
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", max(3, longest+1))
	return fence + lang + "\n" + strings.TrimRight(s, "\n") + "\n" + fence
}

func shellApprovalPreview(name, command, cwd string) *approvalPreview {
	lang := "bash"
	if name == "PowerShell" {
		lang = "powershell"
	}
	heading := fmt.Sprintf("The agent wants to run a **%s** command", name)
	if cwd != "" {
		heading += fmt.Sprintf(" in `%s`", cwd)
	}
	heading += "\n\n" + fencedBlock(lang, command)

	remember := "This command can't be remembered: s, p and g only allow it once."
	if scope := describeCommandScope(tool.ParseCommandsForAllowList(command)); scope != "" {
		remember = "s, p and g also trust, with any arguments: " + scope
	}
	return &approvalPreview{heading: heading, remember: remember}
}

// describeCommandScope renders the allow-list keys a command would add, with
// the flags each one is trusted with.
func describeCommandScope(keys map[string][]string) string {
	if len(keys) == 0 {
		return ""
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)

	const maxShown = 4
	parts := make([]string, 0, len(names))
	for i, k := range names {
		if i == maxShown {
			parts = append(parts, fmt.Sprintf("+%d more", len(names)-maxShown))
			break
		}
		seen := map[string]bool{}
		var flags []string
		for _, f := range keys[k] {
			if !seen[f] {
				seen[f] = true
				flags = append(flags, f)
			}
		}
		sort.Strings(flags)
		if len(flags) == 0 {
			parts = append(parts, k+" (no flags)")
		} else {
			parts = append(parts, k+" (flags: "+strings.Join(flags, ", ")+")")
		}
	}
	return strings.Join(parts, "; ")
}

func rememberTool(toolName, scope string) string {
	return fmt.Sprintf("s, p and g also trust every %s call, %s.", toolName, scope)
}

// readPreviewFile reads path for diffing. ok is false when the file is missing,
// not a regular file, or too large; tooLarge distinguishes the last case.
func readPreviewFile(path string) (content string, ok, tooLarge bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", false, false
	}
	if info.Size() > maxApprovalPreviewBytes {
		return "", false, true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, false
	}
	return string(data), true, false
}

func countLines(s string) int { return len(splitDiffLines(s)) }

func writeFileApprovalPreview(path, content string) *approvalPreview {
	p := &approvalPreview{remember: rememberTool("write_file", "on any path")}
	p.heading = fmt.Sprintf("The agent wants to write `%s`", path)

	old, exists, oldTooLarge := readPreviewFile(path)
	switch {
	case len(content) > maxApprovalPreviewBytes || oldTooLarge:
		p.note = fmt.Sprintf("too large to preview · writes %d lines, %d KB", countLines(content), len(content)/1024)
	case !exists:
		p.heading += " (new file)"
		p.diff = diffLines("", content, 1)
		p.note = fmt.Sprintf("new file · %d lines", countLines(content))
	default:
		p.diff = diffLines(old, content, 1)
		if added, deleted := diffStats(p.diff); added == 0 && deleted == 0 {
			p.diff = nil
			p.note = "no changes: the file already has this content"
		}
	}
	return p
}

func targetEditApprovalPreview(file, search, replace string) *approvalPreview {
	p := &approvalPreview{remember: rememberTool("target_edit", "on any path")}
	p.heading = fmt.Sprintf("The agent wants to edit `%s`", file)

	if len(search)+len(replace) > maxApprovalPreviewBytes {
		p.note = "too large to preview"
		return p
	}
	// Locate the block so the diff carries real line numbers; without them the
	// gutter is simply left out.
	first := 0
	if content, ok, _ := readPreviewFile(file); ok {
		content = strings.ReplaceAll(content, "\r\n", "\n")
		if idx := strings.Index(content, strings.ReplaceAll(search, "\r\n", "\n")); idx >= 0 {
			first = strings.Count(content[:idx], "\n") + 1
		}
	}
	p.diff = diffLines(search, replace, first)
	return p
}

func genericApprovalPreview(toolName, display, arguments string) *approvalPreview {
	body := arguments
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(arguments), "", "  "); err == nil {
		body = buf.String()
	}
	return &approvalPreview{
		heading:  fmt.Sprintf("The agent wants to use **%s**\n\n%s", display, fencedBlock("json", body)),
		remember: rememberTool(toolName, "with any arguments"),
	}
}

// shortDuration formats an approval lifetime compactly: 30m, 12h, 30d.
func shortDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}

// approvalActionRows lays the answer keys out as chips, wrapping onto further
// rows when width is tight.
func approvalActionRows(width int) []string {
	session, project, global := tool.ApprovalTTLs()
	chips := []struct {
		key, label string
		color      lipgloss.Style
	}{
		{"y", "allow once", lipgloss.NewStyle().Foreground(accentEmerald)},
		{"s", "session · " + shortDuration(session), lipgloss.NewStyle().Foreground(primaryColor)},
		{"p", "project · " + shortDuration(project), lipgloss.NewStyle().Foreground(primaryColor)},
		{"g", "global · " + shortDuration(global), lipgloss.NewStyle().Foreground(primaryColor)},
		{"n", "deny", lipgloss.NewStyle().Foreground(accentCoral)},
	}

	var rows []string
	var cur []string
	curW := 0
	flush := func() {
		if len(cur) > 0 {
			rows = append(rows, strings.Join(cur, baseStyle.Render("  ")))
			cur, curW = nil, 0
		}
	}
	for _, c := range chips {
		rendered := chipStyle.Render(
			c.color.Background(chipBgColor).Bold(true).Render(c.key) +
				lipgloss.NewStyle().Foreground(textColor).Background(chipBgColor).Render(" "+c.label))
		w := lipgloss.Width(rendered)
		if curW > 0 && curW+2+w > width {
			flush()
		}
		if curW > 0 {
			curW += 2
		}
		cur = append(cur, rendered)
		curW += w
	}
	flush()
	return rows
}

// wrapDim wraps plain text to width and styles each resulting row, so notes stay
// inside the box on narrow terminals. It breaks on spaces only: flags such as
// "-n" must not be split after their hyphen.
func wrapDim(text string, width int, style lipgloss.Style) []string {
	lines := wrapTodoText(text, width)
	rows := make([]string, len(lines))
	for i, line := range lines {
		rows[i] = style.Render(line)
	}
	return rows
}

// render draws the preview body that goes below the heading: change stats and
// diff for edits, then the answer chips and hints. Every row is at most width
// cells wide; the diff shows at most maxRows rows.
func (p *approvalPreview) render(width, maxRows int) string {
	width = max(width, 20)
	dim := lipgloss.NewStyle().Foreground(subtextColor).Background(appBgColor)
	var rows []string

	if len(p.diff) > 0 {
		added, deleted := diffStats(p.diff)
		rows = append(rows,
			lipgloss.NewStyle().Foreground(accentEmerald).Background(appBgColor).Bold(true).Render(fmt.Sprintf("+%d", added))+
				dim.Render(" ")+
				lipgloss.NewStyle().Foreground(accentCoral).Background(appBgColor).Bold(true).Render(fmt.Sprintf("-%d", deleted)))
		if p.note != "" {
			rows = append(rows, wrapDim(p.note, width, dim)...)
		}
		diffRows, hidden := renderDiffRows(collapseDiff(p.diff, 3), width, maxRows)
		rows = append(rows, diffRows...)
		if hidden > 0 {
			rows = append(rows, wrapDim(fmt.Sprintf("… %d more lines not shown", hidden), width, dim)...)
		}
		rows = append(rows, "")
	} else if p.note != "" {
		rows = append(rows, wrapDim(p.note, width, dim)...)
		rows = append(rows, "")
	}

	rows = append(rows, approvalActionRows(width)...)
	rows = append(rows, "")
	for _, hint := range []string{p.remember, "Or type a reply and press Enter to deny and tell the agent why."} {
		if hint != "" {
			rows = append(rows, wrapDim(hint, width, dim.Italic(true))...)
		}
	}
	return strings.Join(rows, "\n")
}
