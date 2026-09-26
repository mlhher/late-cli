package main

import (
	"flag"
	"strings"
	"testing"

	"late/internal/compaction"
)

// pad mirrors tabwriter's column padding (max cell width + 2 spaces) so the
// expected table lines below stay readable instead of hand-counted.
func pad(s string, w int) string {
	return s + strings.Repeat(" ", w-len(s))
}

// TestFormatReplayTable pins the exact rendering of the -replay-shadow
// output: five aligned columns plus the false-negative rate line.
func TestFormatReplayTable(t *testing.T) {
	rows := []compaction.ReplayRow{
		{Threshold: 0.35, Kept: 2, Relocated: 1, TokensSaved: 120, StillMissed: 1},
		{Threshold: 0.10, Kept: 4, Relocated: 0, TokensSaved: 0, StillMissed: 0},
	}
	got := formatReplayTable(rows, 0.5)
	wantLines := []string{
		// Column widths: max cell width per column + 2 padding; the last
		// column is never padded.
		pad("threshold", 11) + pad("kept", 6) + pad("relocated", 11) + pad("tokens saved", 14) + "still missed",
		pad("0.35", 11) + pad("2", 6) + pad("1", 11) + pad("120", 14) + "1",
		pad("0.10", 11) + pad("4", 6) + pad("0", 11) + pad("0", 14) + "0",
		"",
		"false-negative rate: 50.0%",
	}
	want := strings.Join(wantLines, "\n") + "\n"
	if got != want {
		t.Errorf("formatReplayTable() =\n%q\nwant\n%q", got, want)
	}
}

// TestFormatReplayTableEmpty: no rows still renders the header and the rate
// line — an empty log must not print a bare number with no context.
func TestFormatReplayTableEmpty(t *testing.T) {
	got := formatReplayTable(nil, 0)
	wantLines := []string{
		pad("threshold", 11) + pad("kept", 6) + pad("relocated", 11) + pad("tokens saved", 14) + "still missed",
		"",
		"false-negative rate: 0.0%",
	}
	want := strings.Join(wantLines, "\n") + "\n"
	if got != want {
		t.Errorf("formatReplayTable(nil) =\n%q\nwant\n%q", got, want)
	}
}

// TestParseReplayThresholds: comma-separated thresholds in (0, 1], tolerant
// of surrounding whitespace, strict about everything else.
func TestParseReplayThresholds(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []float64
		wantErr bool
	}{
		{name: "reference example", in: "0.10,0.35,0.50", want: []float64{0.1, 0.35, 0.5}},
		{name: "single", in: "0.5", want: []float64{0.5}},
		{name: "whitespace tolerated", in: " 0.1 , 0.5 ", want: []float64{0.1, 0.5}},
		{name: "upper boundary", in: "1", want: []float64{1}},
		{name: "empty string", in: "", wantErr: true},
		{name: "empty entry", in: "0.1,,0.2", wantErr: true},
		{name: "non-numeric", in: "0.1,two", wantErr: true},
		{name: "zero", in: "0", wantErr: true},
		{name: "negative", in: "-0.2", wantErr: true},
		{name: "above one", in: "1.5", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseReplayThresholds(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseReplayThresholds(%q) error = nil, want an error", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseReplayThresholds(%q) error = %v", tc.in, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("parseReplayThresholds(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("parseReplayThresholds(%q)[%d] = %v, want %v", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestReplayShadowFlagParses: cheap smoke test that the flag accepts the
// documented =value form and its value flows into the threshold parser.
func TestReplayShadowFlagParses(t *testing.T) {
	fs := flag.NewFlagSet("smoke", flag.ContinueOnError)
	replay := fs.String("replay-shadow", "", "smoke")
	if err := fs.Parse([]string{"-replay-shadow=0.10,0.35"}); err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	thresholds, err := parseReplayThresholds(*replay)
	if err != nil {
		t.Fatalf("parseReplayThresholds() error = %v", err)
	}
	if len(thresholds) != 2 || thresholds[0] != 0.1 || thresholds[1] != 0.35 {
		t.Errorf("thresholds = %v, want [0.1 0.35]", thresholds)
	}
}
