package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"late/internal/common"
)

// Colours belong to the palette. A hex literal anywhere else is a colour that
// was picked in isolation (that is how a purple todo title ended up in an amber
// and ice interface) and that no theme can reach.
func TestNoFreeStandingColourLiterals(t *testing.T) {
	hex := regexp.MustCompile(`"#[0-9A-Fa-f]{6}"`)
	allowed := map[string]bool{
		"styles.go":  true, // the palette itself
		"palette.go": true, // palettes, where they exist as data
		"theme.go":   true, // the Markdown style derived from the palette
	}
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no sources found: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || allowed[f] {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if m := hex.FindString(line); m != "" {
				t.Errorf("%s:%d: free-standing colour %s: take it from the palette (styles.go)", f, i+1, m)
			}
		}
	}
}

// strays are the colours the todo pane and the animations used to hard-code.
var strays = map[string][3]string{
	"todo done green":  {"84", "155", "104"},
	"todo empty grey":  {"74", "75", "80"},
	"todo help grey":   {"103", "104", "110"},
	"todo border grey": {"35", "35", "41"},
}

func TestTodoPaneUsesThePalette(t *testing.T) {
	m, _ := newViewportBenchmarkModel(nil)
	m.SetSize(120, 40)
	out := m.todoPaneView(20)
	got := strayScanColors(out)

	for name, c := range strays {
		if got[c] {
			t.Errorf("the todo pane still draws its %s", name)
		}
	}
	for name, want := range map[string][3]string{
		"title in the todo violet":         {"125", "86", "244"},
		"empty note in muted":              {"72", "79", "88"},
		"border in the active line colour": {"45", "50", "62"},
	} {
		if !got[want] {
			t.Errorf("expected the %s (rgb %v) in the todo pane; found %v", name, want, got)
		}
	}
}

func TestFinishedTodosUseTheSuccessColour(t *testing.T) {
	rows := todoContentLines([]common.TodoItem{{Text: "done thing", Done: true}, {Text: "open thing"}}, 40)
	done, open := strayScanColors(rows[0]), strayScanColors(rows[1])
	if !done[[3]string{"78", "204", "163"}] {
		t.Errorf("a finished todo should use the success colour, got %v", done)
	}
	if done[strays["todo done green"]] {
		t.Error("a finished todo still uses its own green")
	}
	if open[[3]string{"78", "204", "163"}] {
		t.Error("an open todo must not look finished")
	}
}

// strayScanColors extracts the truecolor values from SGR sequences, wherever they sit
// in a (possibly compound) sequence.
var strayScanSGR = regexp.MustCompile(`(?:\x1b\[|;)[34]8;2;(\d+);(\d+);(\d+)`)

func strayScanColors(s string) map[[3]string]bool {
	found := map[[3]string]bool{}
	for _, m := range strayScanSGR.FindAllStringSubmatch(s, -1) {
		found[[3]string{m[1], m[2], m[3]}] = true
	}
	return found
}

func TestDerivedAccentsStayWithTheirParents(t *testing.T) {
	sum := func(c interface{ RGBA() (r, g, b, a uint32) }) uint32 {
		r, g, b, _ := c.RGBA()
		return (r + g + b) >> 8
	}
	if sum(peakColor) <= sum(secondaryColor) {
		t.Error("the equalizer's crest must be lighter than the ice accent it derives from")
	}
	if sum(auraColor) >= sum(primaryColor) {
		t.Error("the scanner's glow must be dimmer than the amber it derives from")
	}
}
