package tool

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestListProjectFilesHonorsIgnoreRules(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(".gitignore", "build/\n*.log\n")
	write(".llmignore", ".env\n")
	write("main.go", "package main")
	write("internal/app/app.go", "package app")
	write(".github/workflows/ci.yml", "on: push")
	write("build/out.bin", "x")
	write("debug.log", "x")
	write(".env", "SECRET=1")
	write("node_modules/dep/index.js", "x")
	write(".git/config", "x")

	ResetGitIgnoreCache()
	t.Chdir(root)
	ResetGitIgnoreCache()

	got, err := ListProjectFiles(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".github/workflows/ci.yml", ".gitignore", ".llmignore", "internal/app/app.go", "main.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListProjectFiles = %v, want %v", got, want)
	}
}

func TestListProjectFilesLimit(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a", "b", "c", "d"} {
		if err := os.WriteFile(filepath.Join(root, n+".txt"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ResetGitIgnoreCache()
	t.Chdir(root)
	ResetGitIgnoreCache()

	got, err := ListProjectFiles(".", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("got %d files, want the limit of 2: %v", len(got), got)
	}
}
