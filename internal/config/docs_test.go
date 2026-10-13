package config

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The config reference doc (docs/config-reference.md) claims to document
// every accepted config.json key. These tests keep that claim honest against
// the Config struct itself:
//
//   - every documented key (a table row whose first cell is exactly one
//     backticked word) must be a Config json tag found via reflection;
//   - every valid Config json tag must be documented, so a new struct field
//     without a doc row fails the test instead of silently shipping
//     undocumented;
//   - every valid ModelSetting json tag (the keys inside one models[] entry)
//     must be documented in the "models entries" nested schema, and every
//     entry key documented in that section must be a ModelSetting tag;
//   - the doc's required sections (precedence, strict parsing) must stay
//     present.
//
// Other nested entries (enabled_tools."bash", …) are intentionally
// documented outside that row shape — only top-level config.json keys are
// backticked single tokens in a first cell.
//
// Note: this build carries no strict config parser (that lives on the fork's
// config hardening line), so "valid" here means "accepted by the Config
// struct's json tags" rather than "the strict parser accepts it".

const configReferenceDocPath = "../../docs/config-reference.md"

// docKeyRowRe matches a reference-table row whose first cell is exactly one
// backticked key: `| `key` | ... |`.
var docKeyRowRe = regexp.MustCompile(`^\|\s*` + "`" + `([^` + "`" + `]+)` + "`" + `\s*\|`)

// requiredDocSections are the headings the reference promises at the top.
var requiredDocSections = []string{
	"## Precedence",
	"## Strict parsing",
}

// modelsSchemaHeading is the nested-schema section that documents the keys
// of one models[] entry (ModelSetting). Its bulleted `key` (type …) items
// are guarded against the struct's tags.
const modelsSchemaHeading = "### `models` entries"

// modelsSchemaItemRe matches one bulleted models-entry key description:
// `* `key` (…) — description`.
var modelsSchemaItemRe = regexp.MustCompile(`^\*\s*` + "`" + `([^` + "`" + `]+)` + "`" + `\s*\(`)

// structJSONTags reflects over Config's json tags and returns the set of
// top-level config.json keys the struct accepts. Runtime-only fields
// (json:"-") are excluded.
func structJSONTags(t *testing.T) map[string]bool {
	t.Helper()
	tags := make(map[string]bool)
	typ := reflect.TypeOf(Config{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		switch tag {
		case "-":
			// Runtime-only field (e.g. Degraded): not a config.json key.
		case "":
			t.Fatalf("Config field %s has no json tag; every field must declare one", field.Name)
		default:
			tags[tag] = true
		}
	}
	return tags
}

// modelSettingJSONTags reflects over ModelSetting's json tags and returns the
// set of keys one models[] entry accepts.
func modelSettingJSONTags(t *testing.T) map[string]bool {
	t.Helper()
	tags := make(map[string]bool)
	typ := reflect.TypeOf(ModelSetting{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		switch tag {
		case "-":
			// Runtime-only field: not a config.json key.
		case "":
			t.Fatalf("ModelSetting field %s has no json tag; every field must declare one", field.Name)
		default:
			tags[tag] = true
		}
	}
	return tags
}

// modelSettingDocSection extracts the lines of the "### `models` entries"
// nested-schema section (up to the next heading).
func modelSettingDocSection(t *testing.T, lines []string) []string {
	t.Helper()
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == modelsSchemaHeading {
			start = i + 1
			break
		}
	}
	if start == -1 {
		t.Fatalf("%s is missing the %q section — the models[] entry schema must be documented", configReferenceDocPath, modelsSchemaHeading)
	}
	for end := start; end < len(lines); end++ {
		if strings.HasPrefix(lines[end], "## ") || strings.HasPrefix(lines[end], "### ") {
			return lines[start:end]
		}
	}
	return lines[start:]
}

func TestConfigReferenceDocKeysAreValid(t *testing.T) {
	lines := readConfigReferenceDocLines(t)

	fromStruct := structJSONTags(t)

	documented := map[string]bool{}
	for lineNo, line := range lines {
		m := docKeyRowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key := m[1]
		if !fromStruct[key] {
			t.Errorf("%s:%d: table row documents %q, which is NOT a Config json tag", configReferenceDocPath, lineNo, key)
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

// TestConfigReferenceDocModelsEntryKeysAreValid keeps the "### `models`
// entries" nested schema honest against ModelSetting's json tags: every
// documented entry key must be a ModelSetting tag, and every tag must be
// documented in that section, so a new per-model key without a doc bullet
// fails here.
func TestConfigReferenceDocModelsEntryKeysAreValid(t *testing.T) {
	lines := readConfigReferenceDocLines(t)
	section := modelSettingDocSection(t, lines)

	fromStruct := modelSettingJSONTags(t)

	documented := map[string]bool{}
	for lineNo, line := range section {
		m := modelsSchemaItemRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key := m[1]
		if !fromStruct[key] {
			t.Errorf("%s:%d: models schema documents entry key %q, which is NOT a ModelSetting json tag", configReferenceDocPath, lineNo, key)
			continue
		}
		if documented[key] {
			t.Errorf("%s:%d: models entry key %q is documented more than once", configReferenceDocPath, lineNo, key)
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
		t.Errorf("ModelSetting json tag %q is valid but has no `* `%s` (` bullet in the %q section of %s — the reference must be complete", tag, tag, modelsSchemaHeading, configReferenceDocPath)
	}

	t.Logf("%s documents %d/%d models entry keys", configReferenceDocPath, len(documented), len(fromStruct))
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
