package config

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The config reference doc (docs/config-reference.md) claims to be the
// COMPLETE config.json reference. These tests keep that claim honest against
// the Config struct itself:
//
//   - every documented key (a table row whose first cell is exactly one
//     backticked word) must be a Config json tag found via reflection — the
//     same set knownConfigKeys feeds the strict parser's unknown-key walk, so
//     "valid" here means "the parser accepts it instead of aborting startup";
//   - every valid Config json tag must be documented, so a new struct field
//     without a doc row fails the test instead of silently shipping
//     undocumented;
//   - the doc's required sections (precedence, boolean synonyms, strict
//     parsing) must stay present.
//
// Nested entries (models[].id, enabled_tools."bash", …) are intentionally
// documented outside that row shape — only top-level config.json keys are
// backticked single tokens in a first cell.

const configReferenceDocPath = "../../docs/config-reference.md"

// docKeyRowRe matches a reference-table row whose first cell is exactly one
// backticked key: `| `key` | ... |`.
var docKeyRowRe = regexp.MustCompile(`^\|\s*` + "`" + `([^` + "`" + `]+)` + "`" + `\s*\|`)

// requiredDocSections are the headings the reference promises at the top.
var requiredDocSections = []string{
	"## Precedence",
	"## Boolean values (on/off synonyms)",
	"## Strict parsing",
}

// structJSONTags reflects over Config's json tags and returns the set of
// top-level config.json keys the struct accepts. Runtime-only fields
// (json:"-") are excluded, mirroring knownConfigKeys.
func structJSONTags(t *testing.T) map[string]bool {
	t.Helper()
	tags := make(map[string]bool)
	typ := reflect.TypeOf(Config{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		switch tag {
		case "-":
			// Runtime-only field: not a config.json key.
		case "":
			t.Fatalf("Config field %s has no json tag; every field must declare one", field.Name)
		default:
			tags[tag] = true
		}
	}
	return tags
}

func TestConfigReferenceDocKeysAreValid(t *testing.T) {
	lines := readConfigReferenceDocLines(t)

	fromStruct := structJSONTags(t)
	fromParser := knownConfigKeys() // the strict parser's reflection-based key set

	// The doc's source of truth must not drift from the parser's: both walks
	// reflect over the same struct, so any difference is a bug in one of them.
	for tag := range fromStruct {
		if _, ok := fromParser[tag]; !ok {
			t.Errorf("json tag %q is visible via direct reflection but missing from knownConfigKeys; the strict parser would reject configs using it", tag)
		}
	}
	for tag := range fromParser {
		if !fromStruct[tag] {
			t.Errorf("knownConfigKeys accepts %q but the struct reflection walk does not see it", tag)
		}
	}

	documented := map[string]bool{}
	for lineNo, line := range lines {
		m := docKeyRowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key := m[1]
		if !fromStruct[key] {
			t.Errorf("%s:%d: table row documents %q, which is NOT a Config json tag — the strict parser would reject it as an unknown key", configReferenceDocPath, lineNo, key)
			continue
		}
		if documented[key] {
			t.Errorf("%s:%d: key %q is documented in more than one table row", configReferenceDocPath, lineNo, key)
		}
		documented[key] = true
	}

	var missing []string
	for tag := range fromStruct {
		if !documented[tag] {
			missing = append(missing, tag)
		}
	}
	sort.Strings(missing)
	for _, tag := range missing {
		t.Errorf("Config json tag %q is valid but has no `| `%s` |` table row in %s — the reference must be complete", tag, tag, configReferenceDocPath)
	}

	t.Logf("%s documents %d/%d Config json tags", configReferenceDocPath, len(documented), len(fromStruct))
}

func TestConfigReferenceDocRequiredSections(t *testing.T) {
	lines := readConfigReferenceDocLines(t)
	seen := map[string]bool{}
	for _, line := range lines {
		if strings.HasPrefix(line, "#") {
			seen[strings.TrimRight(line, " \t")] = true
		}
	}
	for _, heading := range requiredDocSections {
		if !seen[heading] {
			t.Errorf("%s is missing the required %q section", configReferenceDocPath, heading)
		}
	}
}

// readConfigReferenceDocLines reads the reference doc relative to this
// package's directory (Go tests run with the package dir as the working
// directory) and returns its lines.
func readConfigReferenceDocLines(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(configReferenceDocPath)
	if err != nil {
		t.Fatalf("read %s: %v", configReferenceDocPath, err)
	}
	return strings.Split(string(data), "\n")
}
