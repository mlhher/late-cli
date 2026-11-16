package tool

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalBinaryTool_Description(t *testing.T) {
	described := NewExternalBinaryTool("lint", "lint", "Runs the project linter.")
	if got := described.Description(); got != "Runs the project linter." {
		t.Fatalf("Description() = %q, want the configured description", got)
	}

	fallback := NewExternalBinaryTool("lint", "lint", "")
	if want := fmt.Sprintf("Execute the external binary %q without a shell.", "lint"); fallback.Description() != want {
		t.Fatalf("Description() = %q, want %q", fallback.Description(), want)
	}
}

func TestExternalBinaryTool_CallStringIncludesArgs(t *testing.T) {
	tool := NewExternalBinaryTool("lint", "lint", "")

	if got, want := tool.CallString([]byte(`{"args":["-v","main"]}`)), "Running external tool \"lint\" with args: -v main..."; got != want {
		t.Fatalf("CallString() = %q, want %q", got, want)
	}
	if got, want := tool.CallString([]byte(`{}`)), "Running external tool \"lint\"..."; got != want {
		t.Fatalf("CallString() without args = %q, want %q", got, want)
	}
	if got, want := tool.CallString([]byte(`not json`)), "Running external tool \"lint\"..."; got != want {
		t.Fatalf("CallString() with invalid args = %q, want %q", got, want)
	}
}

func TestExternalBinaryTool_ParametersHaveNoEnv(t *testing.T) {
	tool := NewExternalBinaryTool("lint", "lint", "")
	if params := string(tool.Parameters()); strings.Contains(params, `"env"`) {
		t.Fatalf("Parameters() still advertises env: %s", params)
	}
}

func TestExternalBinaryTool_LimitsLargeOutput(t *testing.T) {
	tool := NewExternalBinaryTool("cat", "cat", "")

	singleLine := filepath.Join(t.TempDir(), "single-line.txt")
	if err := os.WriteFile(singleLine, bytes.Repeat([]byte("a"), maxBashOutputChars+1000), 0o644); err != nil {
		t.Fatalf("write single-line file: %v", err)
	}
	out, err := tool.Execute(context.Background(), []byte(fmt.Sprintf(`{"args":[%q]}`, singleLine)))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(out) > maxBashOutputChars+len("\n... (output truncated)") {
		t.Fatalf("output length %d exceeds the character cap", len(out))
	}
	if !strings.Contains(out, "... (output truncated)") {
		t.Fatal("expected the truncation marker in capped output")
	}

	manyLines := filepath.Join(t.TempDir(), "many-lines.txt")
	if err := os.WriteFile(manyLines, bytes.Repeat([]byte("x\n"), maxBashOutputLines+1000), 0o644); err != nil {
		t.Fatalf("write many-lines file: %v", err)
	}
	out, err = tool.Execute(context.Background(), []byte(fmt.Sprintf(`{"args":[%q]}`, manyLines)))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if newlines := strings.Count(out, "\n"); newlines > maxBashOutputLines+1 {
		t.Fatalf("output has %d newlines, exceeds the line cap", newlines)
	}
}

func TestExternalBinaryTool_LimitsBinaryOutput(t *testing.T) {
	tool := NewExternalBinaryTool("cat", "cat", "")

	binaryFile := filepath.Join(t.TempDir(), "binary.bin")
	if err := os.WriteFile(binaryFile, []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatalf("write binary file: %v", err)
	}
	out, err := tool.Execute(context.Background(), []byte(fmt.Sprintf(`{"args":[%q]}`, binaryFile)))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if out != "(binary output detected)" {
		t.Fatalf("output = %q, want the binary marker", out)
	}
}

func TestExternalBinaryTool_ExecuteMissingBinary(t *testing.T) {
	tool := NewExternalBinaryTool("missing", "definitely-not-a-binary", "")
	if _, err := tool.Execute(context.Background(), []byte(`{}`)); err == nil {
		t.Fatal("Execute() succeeded for a missing binary")
	}
}
