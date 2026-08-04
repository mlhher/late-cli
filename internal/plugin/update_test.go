package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// withExecSeams swaps runCommand / runCommandOutput with capturing stubs and
// returns a function that yields the recorded (name, args) invocations. The
// returned cleanup restores the production seams.
func withExecSeams(t *testing.T) (rec func() []recordedExec, cleanup func()) {
	t.Helper()
	origRun := runCommand
	origOut := runCommandOutput

	var (
		mu      sync.Mutex
		logRun  []recordedExec
		logOut  []recordedExec
		stubOut = []byte("ok")
		stubErr error
	)

	runCommand = func(ctx context.Context, name string, args ...string) error {
		mu.Lock()
		defer mu.Unlock()
		copyArgs := append([]string(nil), args...)
		logRun = append(logRun, recordedExec{name: name, args: copyArgs})
		return stubErr
	}
	runCommandOutput = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		copyArgs := append([]string(nil), args...)
		logOut = append(logOut, recordedExec{name: name, args: copyArgs, out: append([]byte(nil), stubOut...), err: stubErr})
		return stubOut, stubErr
	}

	cleanup = func() {
		runCommand = origRun
		runCommandOutput = origOut
	}
	rec = func() []recordedExec {
		mu.Lock()
		defer mu.Unlock()
		merged := append([]recordedExec(nil), logRun...)
		merged = append(merged, logOut...)
		return merged
	}
	return rec, cleanup
}

type recordedExec struct {
	name string
	args []string
	out  []byte
	err  error
}

// writeMinimalPluginManifest writes a real package.json + .late-plugin.json
// to <dir> so LoadPlugin succeeds, and returns what to use as the registered
// Source for the installer test.
func writeMinimalPluginManifest(t *testing.T, dir, name, version, source, sourceType string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	pkg := map[string]any{
		"name":    name,
		"version": version,
		"late": map[string]any{
			"skills": []string{"skills/welcome.md"},
		},
	}
	pkgBytes, _ := json.MarshalIndent(pkg, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "package.json"), pkgBytes, 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	meta := map[string]any{
		"name":        name,
		"version":     version,
		"path":        dir,
		"source":      source,
		"source_type": sourceType,
		"enabled":     true,
		"installed":   time.Now().UTC().Format(time.RFC3339),
	}
	metaBytes, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, ".late-plugin.json"), metaBytes, 0o644); err != nil {
		t.Fatalf("write .late-plugin.json: %v", err)
	}
}

// TestUpdateNpmHappyPath: Update("foo") records `npm install --prefix ... @latest`
// and returns the fresh plugin entry on the in-memory PluginManager map.
func TestUpdateNpmHappyPath(t *testing.T) {
	root := t.TempDir()
	pluginsDir := filepath.Join(root, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("mkdir plugins dir: %v", err)
	}
	rec, cleanup := withExecSeams(t)
	defer cleanup()

	// Pre-install npm-shaped plugin: plugins/<name> is a symlink to
	// plugins/node_modules/@late/<name>; both directories exist with a
	// real package.json + .late-plugin.json. Scoped source "@late/foo"
	// installs under node_modules/@late/foo, which is where updateNpm
	// re-resolves the package after `npm install`.
	nodeModules := filepath.Join(pluginsDir, "node_modules", "@late", "foo")
	linkTarget := filepath.Join(pluginsDir, "foo")
	if err := os.MkdirAll(nodeModules, 0o755); err != nil {
		t.Fatalf("mkdir node_modules/@late/foo: %v", err)
	}
	writeMinimalPluginManifest(t, nodeModules, "foo", "1.0.0", "@late/foo", "npm")
	if err := os.Symlink(nodeModules, linkTarget); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	pm := NewPluginManager(pluginsDir)
	if err := pm.Discover(); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if pm.Plugin("foo") == nil {
		t.Fatalf("expected foo to be discovered")
	}

	updated, err := Update(pm, "foo", nil)
	if err != nil {
		t.Fatalf("Update(foo) err: %v", err)
	}
	if updated == nil {
		t.Fatalf("Update returned nil plugin")
	}

	calls := rec()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 recorded exec, got %d (%+v)", len(calls), calls)
	}
	got := calls[0]
	if got.name != "npm" {
		t.Fatalf("expected npm invocation, got %q", got.name)
	}
	// Expect: npm install --prefix <root>/plugins --no-save --quiet @late/foo@latest
	wantFragments := []string{"install", "--prefix", pluginsDir, "--no-save", "--quiet", "@late/foo@latest"}
	for _, frag := range wantFragments {
		found := false
		for _, a := range got.args {
			if a == frag {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected npm args to contain %q, got %v", frag, got.args)
		}
	}
}

// TestUpdateLocalRefuses: Update on a local-source plugin returns a clear
// refusal rather than mutating it.
func TestUpdateLocalRefuses(t *testing.T) {
	root := t.TempDir()
	pluginsDir := filepath.Join(root, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rec, cleanup := withExecSeams(t)
	defer cleanup()

	linkTarget := t.TempDir()
	writeMinimalPluginManifest(t, linkTarget, "devlink", "0.1.0", linkTarget, "local")
	if err := os.Symlink(linkTarget, filepath.Join(pluginsDir, "devlink")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	pm := NewPluginManager(pluginsDir)
	if err := pm.Discover(); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if _, err := Update(pm, "devlink", nil); err == nil {
		t.Fatalf("expected Update on local-source plugin to error")
	} else if !strings.Contains(err.Error(), "local") {
		t.Fatalf("expected error to mention 'local', got %v", err)
	}

	if len(rec()) != 0 {
		t.Fatalf("expected no exec to run for refused local update, got %+v", rec())
	}
}

// TestUpdateUnknownName: Update on a name that was never installed errors
// without touching the filesystem (verified by no recorded exec).
func TestUpdateUnknownName(t *testing.T) {
	root := t.TempDir()
	pluginsDir := filepath.Join(root, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rec, cleanup := withExecSeams(t)
	defer cleanup()

	pm := NewPluginManager(pluginsDir)

	if _, err := Update(pm, "nope", nil); err == nil {
		t.Fatalf("expected Update on unknown plugin to error")
	}

	if len(rec()) != 0 {
		t.Fatalf("expected no exec for missing plugin, got %+v", rec())
	}
}

// TestUpdatePropagatesExecError: if the underlying npm install exits
// non-zero, Update surfaces that error wrapped and does NOT silently claim
// success.
func TestUpdatePropagatesExecError(t *testing.T) {
	root := t.TempDir()
	pluginsDir := filepath.Join(root, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	origRun := runCommand
	origOut := runCommandOutput
	defer func() {
		runCommand = origRun
		runCommandOutput = origOut
	}()
	runCommand = func(ctx context.Context, name string, args ...string) error {
		return errors.New("synthetic npm failure")
	}
	runCommandOutput = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("synthetic npm failure"), errors.New("synthetic npm failure")
	}

	nodeModules := filepath.Join(pluginsDir, "node_modules", "broken")
	linkTarget := filepath.Join(pluginsDir, "broken")
	if err := os.MkdirAll(nodeModules, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeMinimalPluginManifest(t, nodeModules, "broken", "0.0.1", "@late/broken", "npm")
	if err := os.Symlink(nodeModules, linkTarget); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	pm := NewPluginManager(pluginsDir)
	if err := pm.Discover(); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if _, err := Update(pm, "broken", nil); err == nil {
		t.Fatalf("expected Update to surface exec error")
	} else if !strings.Contains(err.Error(), "synthetic npm failure") {
		t.Fatalf("expected wrapped error, got %v", err)
	}
}

// TestUpdateAllIteratesAndSkipsLocal: UpdateAll loops over every non-local
// plugin and skips dev symlinks without erroring out.
func TestUpdateAllIteratesAndSkipsLocal(t *testing.T) {
	root := t.TempDir()
	pluginsDir := filepath.Join(root, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_, cleanup := withExecSeams(t)
	defer cleanup()

	// Two npm-shape plugins (scoped sources install under
	// node_modules/@late/<name>) and one local devlink.
	for _, name := range []string{"alpha", "beta"} {
		nm := filepath.Join(pluginsDir, "node_modules", "@late", name)
		link := filepath.Join(pluginsDir, name)
		if err := os.MkdirAll(nm, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		writeMinimalPluginManifest(t, nm, name, "1.0.0", "@late/"+name, "npm")
		if err := os.Symlink(nm, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
	}
	devDir := t.TempDir()
	writeMinimalPluginManifest(t, devDir, "devlink", "0.1.0", devDir, "local")
	if err := os.Symlink(devDir, filepath.Join(pluginsDir, "devlink")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	pm := NewPluginManager(pluginsDir)
	if err := pm.Discover(); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	updated, err := UpdateAll(pm, nil)
	if err != nil {
		t.Fatalf("UpdateAll err: %v", err)
	}
	if len(updated) != 2 {
		t.Fatalf("expected 2 updated plugins, got %d (%v)", len(updated), namesOf(updated))
	}
	gotNames := map[string]bool{}
	for _, p := range updated {
		gotNames[p.Name] = true
	}
	for _, want := range []string{"alpha", "beta"} {
		if !gotNames[want] {
			t.Fatalf("UpdateAll did not return %q", want)
		}
	}
	if gotNames["devlink"] {
		t.Fatalf("UpdateAll should not include the local devlink")
	}
}

func namesOf(plugins []*InstalledPlugin) []string {
	out := make([]string, len(plugins))
	for i, p := range plugins {
		out[i] = p.Name
	}
	return out
}

// TestInstallDispatcherMarketplaceFallback: Install dispatches a bare name
// to the marketplace; on a 404 it falls through to npm (default) and emits
// the user-visible notice on stderr.
func TestInstallDispatcherMarketplaceFallback(t *testing.T) {
	root := t.TempDir()
	pluginsDir := filepath.Join(root, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Marketplace always 404s.
	srv := stubMarketplace(t, nil) // empty store → every name misses
	mc := &MarketplaceClient{BaseURL: srv.URL, HTTPClient: srv.Client()}

	// Override npm exec: it would fail because the package doesn't exist.
	// We only need to assert Install attempted npm after the 404 fall-through,
	// and that it bubbled up the exec error. A bare name (no "/") is what
	// routes through the marketplace branch — scoped names go straight to npm.
	origRun := runCommandOutput
	defer func() { runCommandOutput = origRun }()
	runCommandOutput = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "npm" {
			t.Fatalf("expected fallback to npm exec, got %q", name)
		}
		return nil, errors.New("npm not configured in test")
	}

	pm := NewPluginManager(pluginsDir)

	// Capture stderr from Install's fmt.Fprintf "marketplace did not match..."
	// by redirecting os.Stderr.
	r, w, _ := os.Pipe()
	origStderr := os.Stderr
	os.Stderr = w
	defer func() {
		os.Stderr = origStderr
		_ = w.Close()
		_, _ = io.ReadAll(r)
	}()

	if _, err := Install(pm, "scoped-pkg", mc, false); err == nil {
		t.Fatalf("expected Install to surface the underlying exec error after marketplace miss")
	} else if !strings.Contains(err.Error(), "npm not configured") {
		t.Fatalf("expected underlying npm error to be wrapped, got %v", err)
	}

	// Restore stderr to read what was written.
	_ = w.Close()
	os.Stderr = origStderr
	buf, _ := io.ReadAll(r)
	if !strings.Contains(string(buf), "marketplace did not match") {
		t.Fatalf("expected stderr to announce marketplace miss, got %q", string(buf))
	}
}
