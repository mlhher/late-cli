package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// The models[] entry key walk: unknown keys inside a models[] entry are
// fatal, located, did-you-mean errors. encoding/json alone would silently
// drop a hand-edited typo there (the typed decode has no useful position for
// nested fields), and the top-level decode on this build ignores unknown
// keys — so the walk is enforced where it matters most: the per-model
// settings inside every models[] entry, including the per-model
// jev-autocompact-percent override. Value-RANGE problems (e.g. a
// jev-autocompact-percent of 400) stay in the warn-and-fall-back path
// (Config.AutocompactWarnings), consistent with the global key's warning
// pattern; strictness here covers key NAMES only.
//
// This file carries the minimal private versions of the strict-parser
// helpers the walk needs (known-key set, positioned error rendering,
// did-you-mean suggestion): the full positioned strict parser for every
// top-level key belongs to the config PR.

// knownModelEntryKeys is the set of valid keys inside one models[] entry,
// mirrored from ModelSetting's json tags (kept in sync by docs_test.go's
// reflection guard on the documented schema).
var knownModelEntryKeys = map[string]bool{
	"id":                      true,
	"url":                     true,
	"key":                     true,
	"model":                   true,
	"jev-autocompact-percent": true,
}

// maxSuggestionDistance is the largest Levenshtein distance still considered
// "reasonably similar" for a did-you-mean suggestion.
const maxSuggestionDistance = 3

// checkModelsEntryKeys walks the raw models[] array in content and validates
// every object's keys against the known ModelSetting key set: an unknown key
// inside an entry is a positioned did-you-mean error naming the entry. Each
// nested key's exact byte range within the document is recovered from the
// Decoder's InputOffset, so the reported line/column is the key's position in
// the user's editor — encoding/json does not report positions for nested
// fields itself. Syntax problems are NOT reported here (the typed decode
// already ran and owns them); the walk only adds the key-name check.
// agent_models needs no equivalent pass: it is a map[string]string whose keys
// are agent roles (data, never schema), so it has no unknown-key concept.
func checkModelsEntryKeys(path string, content []byte) error {
	dec := json.NewDecoder(bytes.NewReader(content))
	tok, err := dec.Token()
	if err != nil {
		// Syntax error: the typed decode already reported it.
		return nil
	}
	if delim, isDelim := tok.(json.Delim); !isDelim || delim != '{' {
		// Not an object: the typed decode owns that error.
		return nil
	}
	// Last "models" value wins (encoding/json's duplicate-key semantics: the
	// last occurrence is what the typed decode keeps, so validating it
	// matches what survives).
	var modelsRaw json.RawMessage
	modelsStart := -1
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil
		}
		key, _ := keyTok.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil
		}
		if key == "models" {
			modelsRaw = raw
			// The value's first byte lives len(raw) bytes before the
			// offset just past it.
			modelsStart = int(dec.InputOffset()) - len(raw)
		}
	}
	if modelsRaw == nil {
		return nil
	}
	return walkModelsArray(path, content, modelsRaw, modelsStart)
}

// walkModelsArray re-parses the raw models value with a Decoder so each
// entry's exact byte range within the document is known, then validates each
// entry object's keys. A non-array models value is the typed decode's error,
// not this walk's.
func walkModelsArray(path string, content, modelsRaw []byte, modelsStart int) error {
	dec := json.NewDecoder(bytes.NewReader(modelsRaw))
	tok, err := dec.Token()
	if err != nil {
		return nil
	}
	if delim, isDelim := tok.(json.Delim); !isDelim || delim != '[' {
		return nil
	}
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil
		}
		entryStart := modelsStart + int(dec.InputOffset()) - len(raw)
		if err := checkOneModelsEntry(path, content, raw, entryStart); err != nil {
			return err
		}
	}
	return nil
}

// modelsEntryLabel derives the human name of one raw models[] entry for
// error messages: its id when set, else its model name, else the generic
// "entry" — the same identifier AutocompactWarnings uses.
func modelsEntryLabel(raw json.RawMessage) string {
	var m ModelSetting
	if err := json.Unmarshal(raw, &m); err == nil {
		if m.ID != "" {
			return m.ID
		}
		if m.Model != "" {
			return m.Model
		}
	}
	return "entry"
}

// checkOneModelsEntry validates the keys of one raw models[] entry object
// whose first byte sits at entryStart in the document. A nested key's
// absolute offset is entryStart + (offset of the key within raw); the key
// token's InputOffset sits just past its closing quote, so the opening quote
// is len(key)+2 bytes back. Null entries (and non-object values, whose
// handling the typed decode owns) carry no keys to validate.
func checkOneModelsEntry(path string, content, raw []byte, entryStart int) error {
	if string(raw) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil
	}
	if delim, isDelim := tok.(json.Delim); !isDelim || delim != '{' {
		return nil
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil
		}
		key, isString := tok.(string)
		if !isString {
			// Unreachable in valid JSON: object keys are strings.
			return nil
		}
		keyStart := entryStart + int(dec.InputOffset()) - len(key) - 2
		if !knownModelEntryKeys[key] {
			return unknownModelEntryKeyError(path, content, key, keyStart, modelsEntryLabel(raw))
		}
		// Values need no key-level validation here: the typed decode's
		// errors cover wrong types, and an out-of-range
		// jev-autocompact-percent value warns (Config.AutocompactWarnings),
		// the same as the global key.
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil
		}
	}
	return nil
}

// unknownModelEntryKeyError renders the located error for an unknown key
// inside a models[] entry: a did-you-mean suggestion against the known entry
// keys when reasonably similar, otherwise the full sorted list — with the
// message naming the entry.
func unknownModelEntryKeyError(path string, content []byte, key string, keyStart int, label string) error {
	names := make([]string, 0, len(knownModelEntryKeys))
	for name := range knownModelEntryKeys {
		names = append(names, name)
	}
	before := string(content[:keyStart])
	line := 1 + strings.Count(before, "\n")
	lineStart := strings.LastIndex(before, "\n") + 1
	column := utf8.RuneCountInString(before[lineStart:]) + 1
	if suggestion := closestMatch(key, names); suggestion != "" {
		return fmt.Errorf("error in %s at line %d, column %d: models[%s] entry %q is not a valid entry key. Did you mean %q?",
			path, line, column, label, key, suggestion)
	}
	sort.Strings(names)
	return fmt.Errorf("error in %s at line %d, column %d: models[%s] entry %q is not a valid entry key. Valid entry keys are: %s",
		path, line, column, label, key, strings.Join(names, ", "))
}

// closestMatch returns the candidate with the smallest Levenshtein distance
// to target when that distance is at most maxSuggestionDistance; ties resolve
// to the alphabetically first candidate (the candidates are walked sorted).
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

// levenshtein is the standard edit distance over runes.
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
