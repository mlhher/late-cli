package plugin

import (
	"path/filepath"
	"slices"
	"testing"
)

// TestResolveArgs verifies the conservative resolver policy:
//   - args starting with `./` or `../` are joined to pluginDir
//   - args with a leading `/` (absolute) pass through verbatim
//   - bare names pass through verbatim — even if a same-named file
//     happens to live under pluginDir (the transport's cmd.Dir makes
//     cwd resolution work without help)
//
// The "bare name" guarantee is the bug-prevention half of this test:
// before the tightening, ["node", "src/index.js"] would silently
// rewrite to ["node", "/abs/.../src/index.js"] when src/index.js
// existed in the plugin, surprising plugin authors.
func TestResolveArgs(t *testing.T) {
	pluginDir := t.TempDir()
	resolved := filepath.Join(pluginDir, "scripts", "server.sh")
	parent := filepath.Join(pluginDir, "..", "shared", "server.sh")

	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "explicit ./ resolves under pluginDir",
			in:   []string{"./scripts/server.sh"},
			want: []string{resolved},
		},
		{
			name: "explicit ../ resolves upward from pluginDir",
			in:   []string{"../shared/server.sh"},
			want: []string{parent},
		},
		{
			name: "absolute path passes through",
			in:   []string{"/usr/local/bin/server.sh"},
			want: []string{"/usr/local/bin/server.sh"},
		},
		{
			name: "bare names with the same basename in pluginDir are NOT rebound",
			in:   []string{"node", "src/index.js"},
			want: []string{"node", "src/index.js"},
		},
		{
			name: "mixed: ./ resolves, / passes through, bare names preserved",
			in:   []string{"./a.sh", "/b.sh", "c.sh", "node"},
			want: []string{filepath.Join(pluginDir, "a.sh"), "/b.sh", "c.sh", "node"},
		},
		{
			name: "empty arg preserved",
			in:   []string{""},
			want: []string{""},
		},
		{
			name: "bare '.' alone preserved (no ./ prefix)",
			in:   []string{"."},
			want: []string{"."},
		},
		{
			name: "nil input yields empty (same-length) slice",
			in:   nil,
			want: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveArgs(pluginDir, tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("resolveArgs(%q, %v) length %d, want %d (got %v)",
					pluginDir, tt.in, len(got), len(tt.want), got)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("resolveArgs(%q, %v) = %v, want %v", pluginDir, tt.in, got, tt.want)
			}
		})
	}
}
