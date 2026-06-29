package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Benchmark suite for search_tool's .gitignore efficiency.

// TestGitIgnoreOutputSavings demonstrates concrete byte/token savings from
// .gitignore filtering. Search for a pattern that matches EVERY file to show
// the full impact of filtering noise directories.
func TestGitIgnoreOutputSavings(t *testing.T) {
	dir := t.TempDir()

	os.MkdirAll(filepath.Join(dir, ".git"), 0755)
	os.MkdirAll(filepath.Join(dir, "dist"), 0755)
	os.MkdirAll(filepath.Join(dir, "vendor/pkg/lib"), 0755)

	// 20 source files — each matches "package"
	for i := 0; i < 20; i++ {
		content := fmt.Sprintf("package p%d\nfunc F%d() {}\n", i, i)
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%d.go", i)), []byte(content), 0644)
	}
	// 100 noise files in dist/ — each also matches "package"
	for i := 0; i < 100; i++ {
		content := fmt.Sprintf("(function() { var package$%d = {}; })();\n", i)
		os.WriteFile(filepath.Join(dir, "dist", fmt.Sprintf("bundle%d.js", i)), []byte(content), 0644)
	}
	// 100 noise files in vendor/ — each also matches "package"
	for i := 0; i < 100; i++ {
		content := fmt.Sprintf("package vendor_%d\nfunc vendorFunc() {}\n", i)
		os.WriteFile(filepath.Join(dir, "vendor/pkg/lib", fmt.Sprintf("lib%d.go", i)), []byte(content), 0644)
	}

	tool := &SearchTool{}
	searchArgs := `{"pattern":"package","path":"` + dir + `","output_mode":"files_with_matches","max_results":500}`

	// With gitignore
	ResetGitIgnoreCache()
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("dist/\nvendor/\n"), 0644)
	ResetGitIgnoreCache()
	r1, _ := tool.Execute(context.Background(), json.RawMessage(searchArgs))
	lines1 := countResultLines(r1)

	// Without gitignore
	os.Remove(filepath.Join(dir, ".gitignore"))
	ResetGitIgnoreCache()
	r2, _ := tool.Execute(context.Background(), json.RawMessage(searchArgs))
	lines2 := countResultLines(r2)

	pct := 0
	if len(r2) > 0 {
		pct = 100 - (len(r1) * 100 / len(r2))
	}

	t.Logf("WITH gitignore:    %d files, %d bytes (~%d tokens)", lines1, len(r1), len(r1)/4)
	t.Logf("WITHOUT gitignore: %d files, %d bytes (~%d tokens)", lines2, len(r2), len(r2)/4)
	t.Logf("Output reduction:  %d%% (%d of %d files filtered as noise)", pct, lines2-lines1, lines2)
}

func countResultLines(result string) int {
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(result), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "...") {
			count++
		}
	}
	return count
}

// BenchmarkSearchOutputSize measures search time and output allocation with
// and without .gitignore filtering on a project with 20 source files and
// 200 noise files (all matching the search pattern).
func BenchmarkSearchOutputSize(b *testing.B) {
	dir := b.TempDir()

	os.MkdirAll(filepath.Join(dir, ".git"), 0755)
	os.MkdirAll(filepath.Join(dir, "dist"), 0755)
	os.MkdirAll(filepath.Join(dir, "vendor", "pkg", "lib"), 0755)

	for i := 0; i < 20; i++ {
		content := fmt.Sprintf("package p%d\nfunc F%d() {}\n", i, i)
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%d.go", i)), []byte(content), 0644)
	}
	for i := 0; i < 100; i++ {
		content := fmt.Sprintf("(function() { var package$%d = {}; })();\n", i)
		os.WriteFile(filepath.Join(dir, "dist", fmt.Sprintf("bundle%d.js", i)), []byte(content), 0644)
	}
	for i := 0; i < 100; i++ {
		content := fmt.Sprintf("package vendor_%d\nfunc vendorFunc() {}\n", i)
		os.WriteFile(filepath.Join(dir, "vendor", "pkg", "lib", fmt.Sprintf("lib%d.go", i)), []byte(content), 0644)
	}

	b.Run("with_gitignore", func(b *testing.B) {
		ResetGitIgnoreCache()
		os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("dist/\nvendor/\n"), 0644)

		tool := &SearchTool{}
		args := json.RawMessage(`{"pattern": "package", "path": "` + dir + `", "output_mode": "files_with_matches", "max_results": 500}`)

		ResetGitIgnoreCache()
		result, err := tool.Execute(context.Background(), args)
		if err != nil {
			b.Fatal(err)
		}
		resultSize := int64(len(result))

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			tool.Execute(context.Background(), args)
		}
		b.ReportMetric(float64(resultSize), "bytes_per_result")
		b.ReportMetric(float64(resultSize)/4, "tokens_per_result")
	})

	b.Run("no_gitignore", func(b *testing.B) {
		ResetGitIgnoreCache()
		os.Remove(filepath.Join(dir, ".gitignore"))

		tool := &SearchTool{}
		args := json.RawMessage(`{"pattern": "package", "path": "` + dir + `", "output_mode": "files_with_matches", "max_results": 500}`)

		ResetGitIgnoreCache()
		result, err := tool.Execute(context.Background(), args)
		if err != nil {
			b.Fatal(err)
		}
		resultSize := int64(len(result))

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			tool.Execute(context.Background(), args)
		}
		b.ReportMetric(float64(resultSize), "bytes_per_result")
		b.ReportMetric(float64(resultSize)/4, "tokens_per_result")
	})
}

// BenchmarkFindRepoRoot measures cold (filesystem walk) vs warm (cached) repo
// root resolution.
func BenchmarkFindRepoRoot(b *testing.B) {
	dir := b.TempDir()

	os.MkdirAll(filepath.Join(dir, ".git"), 0755)
	deepDir := filepath.Join(dir, "a", "b", "c", "d", "e")
	os.MkdirAll(deepDir, 0755)

	b.Run("cold", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			FindRepoRoot(deepDir)
		}
	})

	b.Run("cached_getRepoRoot", func(b *testing.B) {
		ResetGitIgnoreCache()
		getRepoRoot() // prime cache

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			getRepoRoot()
		}
	})
}

// BenchmarkSchemaSize measures the LLM context cost of the tool's JSON schema.
func BenchmarkSchemaSize(b *testing.B) {
	tool := &SearchTool{}

	b.Run("description", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			tool.Description()
		}
	})

	b.Run("parameters_schema", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			tool.Parameters()
		}
	})
}
