package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"
)

// Strict config.json handling.
//
// config.json is user-authored by hand, so every problem must be reported
// with the exact location instead of failing somewhere inside encoding/json:
//
//   - R2: any JSON syntax error renders the exact line and column and aborts
//     startup.
//   - R3: unknown top-level entries and wrong VALUE choices of multi-value
//     (enum) entries render line/column plus the closest known alternative
//     ("Did you mean ...?"); wrong-typed values render line/column too.
//
// All of these produce a *ConfigParseError whose Error() renders
// `error in <path> at line L, column C: <detail>`, and LoadConfig returns
// them with a nil config so main can exit instead of silently starting on
// fallback defaults.

// maxSuggestionDistance is the largest Levenshtein distance still considered
// "reasonably similar" for a Did-you-mean suggestion. Beyond it the error
// lists the valid alternatives instead of guessing.
const maxSuggestionDistance = 3

// ConfigParseError is a strict config.json error that knows where it
// happened: Offset is the byte offset into the file content the error was
// built from and Error() renders the exact 1-based line and column (columns
// count runes, so UTF-8 content reports the position an editor shows).
type ConfigParseError struct {
	// Path is the config.json file the error refers to. Empty for errors
	// built without file context (e.g. a standalone FlexBool decode).
	Path string
	// Offset is the byte offset of the offending position in content.
	Offset int
	// Msg is the detail rendered after the line/column prefix.
	Msg string

	content []byte
}

// newConfigParseError builds a ConfigParseError for an offset within content.
func newConfigParseError(path string, content []byte, offset int, format string, args ...any) *ConfigParseError {
	return &ConfigParseError{
		Path:    path,
		Offset:  offset,
		Msg:     fmt.Sprintf(format, args...),
		content: content,
	}
}

func (e *ConfigParseError) Error() string {
	line, col := lineCol(e.content, e.Offset)
	if e.Path == "" {
		return fmt.Sprintf("error at line %d, column %d: %s", line, col, e.Msg)
	}
	return fmt.Sprintf("error in %s at line %d, column %d: %s", e.Path, line, col, e.Msg)
}

// lineCol converts a byte offset within content into a 1-based line and a
// 1-based column: the line counts '\n' bytes before the offset, the column
// counts runes since the line start plus one. An offset past the end clamps
// to the end of the content.
func lineCol(content []byte, offset int) (line, col int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(content) {
		offset = len(content)
	}
	line, lineStart := 1, 0
	for i := 0; i < offset; i++ {
		if content[i] == '\n' {
			line++
			lineStart = i + 1
		}
	}
	return line, utf8.RuneCount(content[lineStart:offset]) + 1
}

// parseConfigContent strictly parses config.json content: full-document
// syntax validation (R2), an unknown-key walk that also validates boolean
// synonym values (R3), the typed decode with exact type-error positions
// (R3), and post-decode enum validation (R3). Any problem returns a
// *ConfigParseError and no config.
func parseConfigContent(path string, content []byte) (*Config, error) {
	// A leading UTF-8 byte-order mark carries no information but is common
	// in files saved by Windows editors (Notepad "UTF-8 with BOM", PowerShell
	// Out-File). encoding/json rejects it with a cryptic "invalid character
	// ï" error on a character the user cannot even see, so strip it before
	// parsing; every reported offset below is then relative to the stripped
	// content, which is what an editor shows.
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))

	if err := checkJSONSyntax(content); err != nil {
		return nil, wrapSyntaxError(path, content, err)
	}

	entries, err := walkTopLevelEntries(path, content)
	if err != nil {
		return nil, err
	}
	known := knownConfigKeys()
	for _, entry := range entries {
		field, ok := known[entry.key]
		if !ok {
			return nil, unknownKeyError(path, content, entry, known)
		}
		if isFlexBoolField(field) {
			if err := checkFlexBoolValue(path, content, entry); err != nil {
				return nil, err
			}
		}
	}

	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(content))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, wrapDecodeError(path, content, entries, err)
	}

	if err := validateEnumValues(path, content, entries); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// checkJSONSyntax token-streams the whole document so every syntax error —
// including trailing garbage after the top-level value and an empty file —
// surfaces before any semantic checks. R2: the returned json.SyntaxError
// carries the byte offset of the exact offending character.
//
// encoding/json's Token() stream also happily consumes CONCATENATED
// top-level values (NDJSON-style), which json.Unmarshal-based passes reject.
// Left alone, a second document appended after the config object would be
// silently ignored by the typed decode, which reads only the first value —
// the config would load with part of its entries dropped. So this pass pins
// down where the first top-level value ends (delim depth) and rejects every
// non-whitespace byte after a top-level OBJECT as trailing data. After a
// non-object root the remaining bytes are left alone: the walk's
// "must contain a JSON object, found X" error names the real problem.
func checkJSONSyntax(content []byte) error {
	dec := json.NewDecoder(bytes.NewReader(content))
	sawToken := false
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				if !sawToken {
					return errors.New("unexpected end of JSON input")
				}
				if depth > 0 {
					// Truncated before the top-level value closed.
					return io.ErrUnexpectedEOF
				}
				return nil
			}
			return err
		}
		sawToken = true
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{', '[':
				depth++
				continue
			case ',', ':':
				continue
			case '}', ']':
				depth--
				if depth > 0 {
					// A nested container closed; the first top-level value
					// is still open.
					continue
				}
			}
		default:
			if depth > 0 {
				// A scalar inside the first top-level value.
				continue
			}
		}
		// depth == 0 here: tok closed the first top-level value, or tok is a
		// top-level scalar. Only after an OBJECT (the only valid config root)
		// is the rest of the document checked for trailing data.
		if d, ok := tok.(json.Delim); ok && d == '}' {
			for i := int(dec.InputOffset()); i < len(content); i++ {
				switch content[i] {
				case ' ', '\t', '\n', '\r':
				default:
					return &trailingDataError{offset: i}
				}
			}
		}
		return nil
	}
}

// trailingDataError marks non-whitespace content found after the top-level
// object. wrapSyntaxError renders it as a positioned ConfigParseError.
type trailingDataError struct {
	offset int
}

func (e *trailingDataError) Error() string {
	return "unexpected trailing data after the top-level object"
}

// wrapSyntaxError converts a syntax-check failure into a positioned
// ConfigParseError.
func wrapSyntaxError(path string, content []byte, err error) error {
	var tde *trailingDataError
	if errors.As(err, &tde) {
		return newConfigParseError(path, content, tde.offset,
			"unexpected trailing data after the top-level object; remove everything after the final '}'")
	}
	var parseErr *ConfigParseError
	if errors.As(err, &parseErr) {
		return parseErr
	}
	var synErr *json.SyntaxError
	if errors.As(err, &synErr) {
		return newConfigParseError(path, content, int(synErr.Offset), "%s", synErr.Error())
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		// Truncated input: the most useful position is the end of the file.
		return newConfigParseError(path, content, len(content), "unexpected end of JSON input")
	}
	return newConfigParseError(path, content, 0, "%s", err.Error())
}

// configEntry is one top-level config.json entry recorded by the key walk:
// its name, the byte offset of the key, and the byte offset and raw bytes of
// its value.
type configEntry struct {
	key        string
	keyStart   int
	valueStart int
	raw        json.RawMessage
}

// walkTopLevelEntries walks the top-level object with json.Decoder tokens,
// recording each key's offset and each value's offset and raw bytes. The
// value offsets anchor enum and boolean-synonym errors at the exact value
// (R3). Values are captured as json.RawMessage via Decode, which preserves
// the exact source bytes, so valueStart = end offset - len(raw).
func walkTopLevelEntries(path string, content []byte) ([]configEntry, error) {
	dec := json.NewDecoder(bytes.NewReader(content))
	tok, err := dec.Token()
	if err != nil {
		// Unreachable in practice: the syntax pass runs first.
		return nil, wrapSyntaxError(path, content, err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, newConfigParseError(path, content, 0,
			"config.json must contain a JSON object, found %s", describeJSONToken(tok))
	}

	var entries []configEntry
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, wrapSyntaxError(path, content, err)
		}
		key, ok := tok.(string)
		if !ok {
			return nil, newConfigParseError(path, content, int(dec.InputOffset()),
				"expected an object key, found %s", describeJSONToken(tok))
		}
		keyStart := int(dec.InputOffset()) - len(key) - 2 // minus the two quotes

		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, wrapSyntaxError(path, content, err)
		}
		entries = append(entries, configEntry{
			key:        key,
			keyStart:   keyStart,
			valueStart: int(dec.InputOffset()) - len(raw),
			raw:        raw,
		})
	}
	return entries, nil
}

// describeJSONToken names a decoded json.Token the way type-error messages
// do ("a string", "a number", ...).
func describeJSONToken(tok json.Token) string {
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			return "an object"
		case '[':
			return "an array"
		}
		return string(v)
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case nil:
		return "null"
	default:
		return "a number"
	}
}

// knownConfigKeys reflects over Config's json tags: the set of valid
// top-level config.json keys mapped to their struct fields. Runtime-only
// fields (json:"-") are excluded.
func knownConfigKeys() map[string]reflect.StructField {
	known := make(map[string]reflect.StructField, 48)
	t := reflect.TypeOf(Config{})
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if tag == "-" {
			continue
		}
		if tag == "" {
			tag = field.Name
		}
		known[tag] = field
	}
	return known
}

var flexBoolType = reflect.TypeOf(FlexBool(false))

// isFlexBoolField reports whether the struct field is a FlexBool or
// *FlexBool — the boolean-synonym fields whose values the key walk validates
// with the exact value offset.
func isFlexBoolField(field reflect.StructField) bool {
	t := field.Type
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t == flexBoolType
}

// checkFlexBoolValue validates one boolean-typed entry's raw value against
// the FlexBool synonym table, anchoring any error at the value's exact
// offset (R3).
func checkFlexBoolValue(path string, content []byte, entry configEntry) error {
	var value FlexBool
	if err := json.Unmarshal(entry.raw, &value); err != nil {
		var parseErr *ConfigParseError
		if errors.As(err, &parseErr) {
			// FlexBool errors carry an offset relative to the value they
			// were given; re-anchor it into the full document and name the
			// entry, which FlexBool itself cannot know.
			return newConfigParseError(path, content, entry.valueStart+parseErr.Offset,
				"%s is not a valid boolean value for %q. Accepted values are: %s; case-insensitive, surrounding whitespace allowed",
				quoteJSONToken(entry.raw), entry.key, flexBoolAcceptedValues())
		}
		return newConfigParseError(path, content, entry.valueStart, "%s", err.Error())
	}
	return nil
}

// unknownKeyError renders the R3 error for an unknown top-level entry: the
// closest known key as a Did-you-mean suggestion when reasonably similar
// (Levenshtein distance ≤ 3), otherwise the full sorted valid-entries list.
func unknownKeyError(path string, content []byte, entry configEntry, known map[string]reflect.StructField) error {
	names := make([]string, 0, len(known))
	for name := range known {
		names = append(names, name)
	}
	if suggestion := closestMatch(entry.key, names); suggestion != "" {
		return newConfigParseError(path, content, entry.keyStart,
			"%q is not a valid config.json entry. Did you mean %q?", entry.key, suggestion)
	}
	sort.Strings(names)
	return newConfigParseError(path, content, entry.keyStart,
		"%q is not a valid config.json entry. Valid entries are: %s", entry.key, strings.Join(names, ", "))
}

// validateEnumValues rejects wrong VALUE choices for the multi-value (enum)
// entries, anchored at the recorded value offset (R3). It runs after the
// typed decode, so a surviving value is guaranteed to have the right type.
func validateEnumValues(path string, content []byte, entries []configEntry) error {
	byKey := make(map[string]configEntry, len(entries))
	for _, entry := range entries {
		byKey[entry.key] = entry // duplicate keys: last occurrence wins, matching decode semantics
	}

	if entry, ok := byKey["permission-mode"]; ok {
		var mode string
		_ = json.Unmarshal(entry.raw, &mode)
		switch mode {
		case "", PermissionModeAskForUserApproval, PermissionModeUnsupervised:
		default:
			return invalidEnumValueError(path, content, entry, "permission-mode", mode,
				[]string{PermissionModeAskForUserApproval, PermissionModeUnsupervised})
		}
	}
	return nil
}

// invalidEnumValueError renders the R3 enum error: a Did-you-mean suggestion
// when the value is close to an allowed one, otherwise the sorted
// valid-values list.
func invalidEnumValueError(path string, content []byte, entry configEntry, key, value string, allowed []string) error {
	if suggestion := closestMatch(value, allowed); suggestion != "" {
		return newConfigParseError(path, content, entry.valueStart,
			"%q is not a valid %s value. Did you mean %q?", value, key, suggestion)
	}
	sorted := append([]string(nil), allowed...)
	sort.Strings(sorted)
	return newConfigParseError(path, content, entry.valueStart,
		"%q is not a valid %s value. Valid values are: %s", value, key, strings.Join(sorted, ", "))
}

// wrapDecodeError converts a typed-decode failure into a positioned
// ConfigParseError. UnmarshalTypeError carries the END offset of the
// offending value; for a top-level entry the key walk's recorded valueStart
// points exactly at the value's first byte, which is the friendlier anchor
// (R3 wrong-typed values).
func wrapDecodeError(path string, content []byte, entries []configEntry, err error) error {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		offset := int(typeErr.Offset)
		if typeErr.Field != "" {
			if topKey, _, _ := strings.Cut(typeErr.Field, "."); topKey != "" {
				for _, entry := range entries {
					if entry.key == topKey {
						offset = entry.valueStart
						break
					}
				}
			}
			return newConfigParseError(path, content, offset,
				"%q must be %s, found %s", typeErr.Field, describeJSONType(typeErr.Type), describeJSONValueKind(typeErr.Value))
		}
		return newConfigParseError(path, content, offset, "%s", typeErr.Error())
	}
	var synErr *json.SyntaxError
	if errors.As(err, &synErr) {
		return newConfigParseError(path, content, int(synErr.Offset), "%s", synErr.Error())
	}
	if field, ok := unknownFieldName(err); ok {
		if idx := bytes.Index(content, []byte(`"`+field+`"`)); idx >= 0 {
			return newConfigParseError(path, content, idx,
				"%q is not a valid config.json entry (unknown nested entry)", field)
		}
	}
	return newConfigParseError(path, content, 0, "%s", err.Error())
}

// unknownFieldName extracts the key from encoding/json's
// `json: unknown field "x"` error (DisallowUnknownFields reports without an
// offset, so the caller locates it in the content).
func unknownFieldName(err error) (string, bool) {
	rest, ok := strings.CutPrefix(err.Error(), `json: unknown field "`)
	if !ok {
		return "", false
	}
	name, _, ok := strings.Cut(rest, `"`)
	return name, ok
}

// describeJSONType names a Go type the way JSON type-error messages do.
func describeJSONType(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Map, reflect.Struct:
		return "an object"
	default:
		return "a " + t.Kind().String()
	}
}

// describeJSONValueKind names the JSON value kind reported by
// UnmarshalTypeError.Value ("string", "number", ...) with the same article
// style as describeJSONType.
func describeJSONValueKind(value string) string {
	switch value {
	case "string":
		return "a string"
	case "number":
		return "a number"
	case "bool":
		return "a boolean"
	case "array":
		return "an array"
	case "object":
		return "an object"
	case "null":
		return "null"
	default:
		return value
	}
}

// closestMatch returns the candidate closest to target by Levenshtein
// distance, or "" when nothing is reasonably similar (distance above
// maxSuggestionDistance). Ties resolve to the alphabetically first candidate
// so suggestions are deterministic.
func closestMatch(target string, candidates []string) string {
	sorted := append([]string(nil), candidates...)
	sort.Strings(sorted)
	best, bestDistance := "", maxSuggestionDistance+1
	for _, candidate := range sorted {
		if distance := levenshtein(target, candidate); distance < bestDistance {
			best, bestDistance = candidate, distance
		}
	}
	if bestDistance > maxSuggestionDistance {
		return ""
	}
	return best
}

// levenshtein computes the edit distance between two strings (insertions,
// deletions, substitutions; rune-based).
func levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min(min(curr[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}
