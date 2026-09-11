package plugin

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSplitURLSubpath(t *testing.T) {
	tests := []struct {
		input       string
		wantBase    string
		wantSubpath string
	}{
		{
			input:       "github:user/repo",
			wantBase:    "github:user/repo",
			wantSubpath: "",
		},
		{
			input:       "github:user/repo//plugins/my-plugin",
			wantBase:    "github:user/repo",
			wantSubpath: "plugins/my-plugin",
		},
		{
			input:       "https://github.com/user/repo.git",
			wantBase:    "https://github.com/user/repo.git",
			wantSubpath: "",
		},
		{
			input:       "https://github.com/user/repo.git//plugins/my-plugin/",
			wantBase:    "https://github.com/user/repo.git",
			wantSubpath: "plugins/my-plugin",
		},
		{
			input:       "git@github.com:user/repo.git//subdir/nested",
			wantBase:    "git@github.com:user/repo.git",
			wantSubpath: "subdir/nested",
		},
	}

	for _, tt := range tests {
		gotBase, gotSubpath := splitURLSubpath(tt.input)
		if gotBase != tt.wantBase || gotSubpath != tt.wantSubpath {
			t.Errorf("splitURLSubpath(%q) = (%q, %q), want (%q, %q)",
				tt.input, gotBase, gotSubpath, tt.wantBase, tt.wantSubpath)
		}
	}
}

func TestPluginNameFromURL_Monorepo(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			input: "github:user/repo",
			want:  "repo",
		},
		{
			input: "github:user/repo//plugins/notify-tool-approval",
			want:  "notify-tool-approval",
		},
		{
			input: "https://github.com/user/repo.git//my-plugin",
			want:  "my-plugin",
		},
	}

	for _, tt := range tests {
		got := pluginNameFromURL(tt.input)
		if got != tt.want {
			t.Errorf("pluginNameFromURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestLocatePluginDir(t *testing.T) {
	root := t.TempDir()

	// 1. Monorepo structure with plugins/foo and bar
	fooDir := filepath.Join(root, "plugins", "foo")
	barDir := filepath.Join(root, "bar")
	writeMinimalPluginManifest(t, fooDir, "foo", "1.0.0", "", "git")
	writeMinimalPluginManifest(t, barDir, "bar", "1.0.0", "", "git")

	// Locate by expectedName inside plugins/
	dir, sub, err := locatePluginDir(root, "foo", "")
	if err != nil {
		t.Fatalf("locatePluginDir foo err: %v", err)
	}
	if dir != fooDir || sub != filepath.Join("plugins", "foo") {
		t.Errorf("locatePluginDir foo = (%q, %q), want (%q, %q)", dir, sub, fooDir, "plugins/foo")
	}

	// Locate by expectedName at top level
	dir, sub, err = locatePluginDir(root, "bar", "")
	if err != nil {
		t.Fatalf("locatePluginDir bar err: %v", err)
	}
	if dir != barDir || sub != "bar" {
		t.Errorf("locatePluginDir bar = (%q, %q), want (%q, %q)", dir, sub, barDir, "bar")
	}

	// Locate by explicit subpath
	dir, sub, err = locatePluginDir(root, "", "plugins/foo")
	if err != nil {
		t.Fatalf("locatePluginDir explicit subpath err: %v", err)
	}
	if dir != fooDir || sub != "plugins/foo" {
		t.Errorf("locatePluginDir explicit subpath = (%q, %q), want (%q, %q)", dir, sub, fooDir, "plugins/foo")
	}

	// Missing plugin returns error
	_, _, err = locatePluginDir(root, "nonexistent", "")
	if err == nil {
		t.Fatal("expected error for nonexistent plugin, got nil")
	}
}

func TestMonorepoInstallAndMarketplace(t *testing.T) {
	// Create a real local git monorepo fixture
	repoDir := t.TempDir()
	cmd := exec.Command("git", "init", repoDir)
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	exec.Command("git", "-C", repoDir, "config", "user.email", "test@test.com").Run()
	exec.Command("git", "-C", repoDir, "config", "user.name", "Test").Run()

	// Add plugin 1: plugins/plug-a
	plugADir := filepath.Join(repoDir, "plugins", "plug-a")
	writeMinimalPluginManifest(t, plugADir, "plug-a", "1.0.0", "", "git")

	// Add plugin 2: plugins/plug-b
	plugBDir := filepath.Join(repoDir, "plugins", "plug-b")
	writeMinimalPluginManifest(t, plugBDir, "plug-b", "2.0.0", "", "git")

	exec.Command("git", "-C", repoDir, "add", ".").Run()
	exec.Command("git", "-C", repoDir, "commit", "-m", "init monorepo").Run()

	// Test 1: Install via direct git URL with subpath
	storeDir := t.TempDir()
	pm := NewPluginManager(storeDir)

	gitURLA := repoDir + "//plugins/plug-a"
	installedA, err := InstallFromGit(pm, gitURLA)
	if err != nil {
		t.Fatalf("InstallFromGit(plug-a): %v", err)
	}
	if installedA.Name != "plug-a" {
		t.Errorf("expected plug-a, got %s", installedA.Name)
	}
	if installedA.Subpath != "plugins/plug-a" {
		t.Errorf("expected subpath plugins/plug-a, got %s", installedA.Subpath)
	}

	// Test 2: Install via marketplace stub pointing to monorepo
	srv := stubMarketplace(t, map[string]MarketplaceEntry{
		"plug-b": {
			Git:         repoDir,
			Description: "Plugin B in monorepo",
			Version:     "2.0.0",
		},
	})

	mc := &MarketplaceClient{BaseURL: srv.URL, HTTPClient: srv.Client()}
	installedB, err := Install(pm, "plug-b", mc, false)
	if err != nil {
		t.Fatalf("Install(plug-b via marketplace): %v", err)
	}
	if installedB.Name != "plug-b" {
		t.Errorf("expected plug-b, got %s", installedB.Name)
	}
	if installedB.Subpath != "plugins/plug-b" {
		t.Errorf("expected subpath plugins/plug-b, got %s", installedB.Subpath)
	}
	if installedB.SourceType != "marketplace" {
		t.Errorf("expected source_type marketplace, got %s", installedB.SourceType)
	}

	// Both plugins must be installed and coexist in storeDir
	if pm.Plugin("plug-a") == nil || pm.Plugin("plug-b") == nil {
		t.Fatalf("expected both plug-a and plug-b to be registered")
	}

	// Verify on-disk directories
	if _, err := os.Stat(filepath.Join(storeDir, "plug-a", "package.json")); err != nil {
		t.Errorf("plug-a package.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(storeDir, "plug-b", "package.json")); err != nil {
		t.Errorf("plug-b package.json missing: %v", err)
	}

	// Test 3: Update plug-b
	writeMinimalPluginManifest(t, plugBDir, "plug-b", "2.1.0", "", "git")
	exec.Command("git", "-C", repoDir, "add", ".").Run()
	exec.Command("git", "-C", repoDir, "commit", "-m", "update plug-b to 2.1.0").Run()

	updatedB, err := Update(pm, "plug-b", mc)
	if err != nil {
		t.Fatalf("Update(plug-b): %v", err)
	}
	if updatedB.Version != "2.1.0" {
		t.Errorf("expected updated version 2.1.0, got %s", updatedB.Version)
	}
	if updatedB.Subpath != "plugins/plug-b" {
		t.Errorf("expected updated subpath plugins/plug-b, got %s", updatedB.Subpath)
	}
	if pm.Plugin("plug-a").Version != "1.0.0" {
		t.Errorf("plug-a should not be affected by plug-b update")
	}
}
