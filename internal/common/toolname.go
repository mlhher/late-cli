package common

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// MaxToolNameLen bounds the length of tool names sent to the model.
// OpenAI-compatible chat-completions endpoints reject tool names longer
// than 64 characters, so every tool name we construct is capped here.
const MaxToolNameLen = 64

// SanitizeToolName converts an arbitrary string into a tool-name-safe
// form: only [A-Za-z0-9_-] characters (everything else becomes '_'),
// capped at MaxToolNameLen. This is required because OpenAI-compatible
// endpoints reject tool names containing characters such as ':' — which
// appears in every plugin-namespaced name (plugin "my:tool").
func SanitizeToolName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= MaxToolNameLen {
			break
		}
	}
	return b.String()
}

// NamespaceToolName joins plugin/server and tool name parts into a single
// endpoint-safe tool name: each part is sanitized individually and the
// parts are joined with "__", capped at MaxToolNameLen. Sanitizing the
// parts before joining keeps the boundary unambiguous even when a part
// itself contains characters that sanitize to '_'.
func NamespaceToolName(parts ...string) string {
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteString("__")
		}
		b.WriteString(SanitizeToolName(p))
	}
	out := b.String()
	if len(out) > MaxToolNameLen {
		out = out[:MaxToolNameLen]
	}
	return out
}

// BareToolName returns the trailing component of a namespaced tool name:
// the part after the last "__" or ":" separator (whichever comes last),
// or the name itself when it has no separator. Used to match
// enabledTools-style configs that disable tools by bare name.
func BareToolName(name string) string {
	if idx := strings.LastIndex(name, "__"); idx >= 0 {
		return name[idx+2:]
	}
	if idx := strings.LastIndex(name, ":"); idx >= 0 {
		return name[idx+1:]
	}
	return name
}

// DeduplicateToolNames returns a copy of bases in which every name is
// unique. The first occurrence of a name keeps it unchanged; later
// duplicates get a deterministic "-<8 hex chars>" suffix derived from the
// full base name (so the mapping is stable across calls). The used map
// may pre-seed names that are already taken by other tools; assigned
// names are recorded into it. A name that survives sanitization/truncation
// unchanged is returned unchanged — existing installs never see their
// tool names rewritten unless a collision or a length violation forces it.
func DeduplicateToolNames(bases []string, used map[string]bool) []string {
	if used == nil {
		used = make(map[string]bool)
	}
	out := make([]string, len(bases))
	for i, base := range bases {
		name := base
		if used[name] {
			sum := sha256.Sum256([]byte(base))
			suffix := hex.EncodeToString(sum[:4]) // 8 hex chars, derived from the full base
			for n := 0; ; n++ {
				suf := "-" + suffix
				if n > 0 {
					suf += fmt.Sprintf(".%d", n)
				}
				// Truncate the BASE to leave room for the suffix, never the
				// candidate: cutting the tail could remove the suffix entirely
				// (a 64-char duplicate base would then stay equal to the taken
				// name forever — an infinite loop). The suffix is always
				// present and distinct per n, so termination is guaranteed.
				headLen := MaxToolNameLen - len(suf)
				if headLen < 0 {
					headLen = 0
				}
				head := base
				if len(head) > headLen {
					head = head[:headLen]
				}
				candidate := head + suf
				if !used[candidate] {
					name = candidate
					break
				}
			}
		}
		used[name] = true
		out[i] = name
	}
	return out
}
