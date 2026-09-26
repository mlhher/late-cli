package compaction

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envOf(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// TestBackendRegistry_PinnedValues asserts the exact endpoints, model ids,
// and key variables the port pins for the three backends — including the
// user-required OpenRouter alpha decisions surface.
func TestBackendRegistry_PinnedValues(t *testing.T) {
	specs := backendSpecs()
	want := map[string]Backend{
		BackendTypesafe: {
			Name:    "typesafe",
			URL:     "https://api.typesafe.ai/v1/systemone",
			Model:   "jev-latest",
			KeyEnv:  "TYPESAFE_API_KEY",
			KeyFile: "compaction-typesafe.key",
		},
		BackendOpenRouter: {
			Name:    "openrouter",
			URL:     "https://openrouter.ai/api/alpha/decisions",
			Model:   "~typesafe/jev-latest",
			KeyEnv:  "OPENROUTER_API_KEY",
			KeyFile: "compaction-openrouter.key",
		},
		BackendGateway: {
			Name:    "gateway",
			URL:     "", // from JEV_GATEWAY_URL
			Model:   "jev-latest",
			KeyEnv:  "JEV_GATEWAY_API_KEY",
			KeyFile: "compaction-gateway.key",
		},
	}
	for name, wb := range want {
		got, ok := specs[name]
		if !ok {
			t.Fatalf("backend %q missing from registry", name)
		}
		if got.backend != wb {
			t.Errorf("backend %q = %+v, want %+v", name, got.backend, wb)
		}
		if name == BackendGateway && got.urlEnv != EnvJevGatewayURL {
			t.Errorf("gateway urlEnv = %q, want %q", got.urlEnv, EnvJevGatewayURL)
		}
	}
	if len(specs) != 3 {
		t.Errorf("registry has %d backends, want 3", len(specs))
	}
}

// TestResolveBackend_Precedence walks the selection matrix: --api param >
// JEV_API > first backend with a key (typesafe first); explicit selections
// never fall through.
func TestResolveBackend_Precedence(t *testing.T) {
	dir := t.TempDir() // no key files: env-only resolution

	t.Run("explicit name beats JEV_API and other keys", func(t *testing.T) {
		env := envOf(map[string]string{
			"JEV_API":             "openrouter",
			"OPENROUTER_API_KEY":  "or-key",
			"TYPESAFE_API_KEY":    "ts-key",
			"JEV_GATEWAY_URL":     "https://gw.example/decisions",
			"JEV_GATEWAY_API_KEY": "gw-key",
		})
		got, err := ResolveBackendIn(dir, "typesafe", env)
		if err != nil {
			t.Fatalf("ResolveBackendIn() error = %v", err)
		}
		if got.Backend.Name != BackendTypesafe {
			t.Errorf("resolved %q, want typesafe", got.Backend.Name)
		}
	})

	t.Run("JEV_API beats auto-detection", func(t *testing.T) {
		env := envOf(map[string]string{
			"JEV_API":            "openrouter",
			"OPENROUTER_API_KEY": "or-key",
			"TYPESAFE_API_KEY":   "ts-key",
		})
		got, err := ResolveBackendIn(dir, "", env)
		if err != nil {
			t.Fatalf("ResolveBackendIn() error = %v", err)
		}
		if got.Backend.Name != BackendOpenRouter {
			t.Errorf("resolved %q, want openrouter", got.Backend.Name)
		}
	})

	t.Run("auto-detection prefers typesafe", func(t *testing.T) {
		env := envOf(map[string]string{
			"OPENROUTER_API_KEY": "or-key",
			"TYPESAFE_API_KEY":   "ts-key",
		})
		got, err := ResolveBackendIn(dir, "", env)
		if err != nil {
			t.Fatalf("ResolveBackendIn() error = %v", err)
		}
		if got.Backend.Name != BackendTypesafe {
			t.Errorf("resolved %q, want typesafe", got.Backend.Name)
		}
	})

	t.Run("auto-detection skips keyless typesafe", func(t *testing.T) {
		env := envOf(map[string]string{"OPENROUTER_API_KEY": "or-key"})
		got, err := ResolveBackendIn(dir, "", env)
		if err != nil {
			t.Fatalf("ResolveBackendIn() error = %v", err)
		}
		if got.Backend.Name != BackendOpenRouter {
			t.Errorf("resolved %q, want openrouter", got.Backend.Name)
		}
	})

	t.Run("auto-detection with nothing usable enumerates the gaps", func(t *testing.T) {
		_, err := ResolveBackendIn(dir, "", envOf(nil))
		var noBackend *NoBackendError
		if !errors.As(err, &noBackend) {
			t.Fatalf("error = %v, want *NoBackendError", err)
		}
		for _, name := range backendOrder {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("NoBackendError detail %q does not mention backend %q", err.Error(), name)
			}
		}
	})

	t.Run("explicit unknown name is a typed error", func(t *testing.T) {
		_, err := ResolveBackendIn(dir, "acme", envOf(nil))
		var unknown *UnknownBackendError
		if !errors.As(err, &unknown) {
			t.Fatalf("error = %v, want *UnknownBackendError", err)
		}
		if !strings.Contains(err.Error(), "typesafe") {
			t.Errorf("error %q should enumerate known backends", err.Error())
		}
	})

	t.Run("JEV_API unknown name is a typed error", func(t *testing.T) {
		_, err := ResolveBackendIn(dir, "", envOf(map[string]string{"JEV_API": "acme"}))
		var unknown *UnknownBackendError
		if !errors.As(err, &unknown) {
			t.Fatalf("error = %v, want *UnknownBackendError", err)
		}
	})

	t.Run("named backend with missing key does not fall through", func(t *testing.T) {
		env := envOf(map[string]string{"OPENROUTER_API_KEY": "or-key"})
		_, err := ResolveBackendIn(dir, "typesafe", env)
		var missing *MissingKeyError
		if !errors.As(err, &missing) {
			t.Fatalf("error = %v, want *MissingKeyError", err)
		}
		if missing.Backend != BackendTypesafe || missing.KeyEnv != "TYPESAFE_API_KEY" {
			t.Errorf("MissingKeyError = %+v, want typesafe/TYPESAFE_API_KEY", missing)
		}
	})

	t.Run("backend names are case-insensitive", func(t *testing.T) {
		env := envOf(map[string]string{"OPENROUTER_API_KEY": "or-key"})
		got, err := ResolveBackendIn(dir, "OpenRouter", env)
		if err != nil {
			t.Fatalf("ResolveBackendIn() error = %v", err)
		}
		if got.Backend.Name != BackendOpenRouter {
			t.Errorf("resolved %q, want openrouter", got.Backend.Name)
		}
	})
}

// TestResolveBackend_GatewayURL covers the env-provided gateway endpoint.
func TestResolveBackend_GatewayURL(t *testing.T) {
	dir := t.TempDir()

	t.Run("named gateway without URL is a typed error", func(t *testing.T) {
		env := envOf(map[string]string{"JEV_GATEWAY_API_KEY": "gw-key"})
		_, err := ResolveBackendIn(dir, "gateway", env)
		var urlErr *GatewayURLError
		if !errors.As(err, &urlErr) {
			t.Fatalf("error = %v, want *GatewayURLError", err)
		}
		if !strings.Contains(err.Error(), EnvJevGatewayURL) {
			t.Errorf("error %q should mention %s", err.Error(), EnvJevGatewayURL)
		}
	})

	t.Run("gateway URL comes from the env and is trimmed", func(t *testing.T) {
		env := envOf(map[string]string{
			"JEV_GATEWAY_URL":     "https://gw.example/decisions/",
			"JEV_GATEWAY_API_KEY": "gw-key",
		})
		got, err := ResolveBackendIn(dir, "gateway", env)
		if err != nil {
			t.Fatalf("ResolveBackendIn() error = %v", err)
		}
		if got.Backend.URL != "https://gw.example/decisions" {
			t.Errorf("gateway URL = %q, want trailing slash trimmed", got.Backend.URL)
		}
		if got.Backend.Model != "jev-latest" {
			t.Errorf("gateway model = %q, want jev-latest", got.Backend.Model)
		}
	})

	t.Run("auto-detection picks a fully configured gateway", func(t *testing.T) {
		env := envOf(map[string]string{
			"JEV_GATEWAY_URL":     "https://gw.example/decisions",
			"JEV_GATEWAY_API_KEY": "gw-key",
		})
		got, err := ResolveBackendIn(dir, "", env)
		if err != nil {
			t.Fatalf("ResolveBackendIn() error = %v", err)
		}
		if got.Backend.Name != BackendGateway {
			t.Errorf("resolved %q, want gateway", got.Backend.Name)
		}
	})

	t.Run("auto-detection skips a keyless gateway", func(t *testing.T) {
		// The gateway has its URL but no key anywhere: auto-detection takes
		// the first backend with a resolvable key, so the gateway is skipped
		// and, with nothing else configured, the gaps are enumerated.
		env := envOf(map[string]string{"JEV_GATEWAY_URL": "https://gw.example/decisions"})
		_, err := ResolveBackendIn(dir, "", env)
		var noBackend *NoBackendError
		if !errors.As(err, &noBackend) {
			t.Fatalf("error = %v, want *NoBackendError (the keyless gateway is not selected)", err)
		}
		if !strings.Contains(err.Error(), BackendGateway) || !strings.Contains(err.Error(), EnvJevGatewayKey) {
			t.Errorf("detail %q should report the skipped gateway's missing %s", err.Error(), EnvJevGatewayKey)
		}
	})

	t.Run("auto-detection falls through a URL'd keyless gateway to a keyed backend", func(t *testing.T) {
		env := envOf(map[string]string{
			"JEV_GATEWAY_URL":    "https://gw.example/decisions",
			"OPENROUTER_API_KEY": "or-key",
		})
		got, err := ResolveBackendIn(dir, "", env)
		if err != nil {
			t.Fatalf("ResolveBackendIn() error = %v", err)
		}
		if got.Backend.Name != BackendOpenRouter {
			t.Errorf("resolved %q, want openrouter (the URL'd but keyless gateway is skipped)", got.Backend.Name)
		}
	})
}

// TestResolveBackend_KeyFileLookup covers the env → key file → error chain.
func TestResolveBackend_KeyFileLookup(t *testing.T) {
	dir := t.TempDir()

	t.Run("key file is used when the env is empty", func(t *testing.T) {
		path := filepath.Join(dir, "compaction-typesafe.key")
		if err := os.WriteFile(path, []byte("file-key\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := ResolveBackendIn(dir, "typesafe", envOf(nil))
		if err != nil {
			t.Fatalf("ResolveBackendIn() error = %v", err)
		}
		if got.APIKey != "file-key" {
			t.Errorf("APIKey = %q, want file-key (trailing newline trimmed)", got.APIKey)
		}
		if got.KeySource != KeySourceFile {
			t.Errorf("KeySource = %q, want file", got.KeySource)
		}
		if got.KeyPath != path {
			t.Errorf("KeyPath = %q, want %q", got.KeyPath, path)
		}
	})

	t.Run("env beats key file", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(dir, "compaction-typesafe.key"), []byte("file-key"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := ResolveBackendIn(dir, "typesafe", envOf(map[string]string{"TYPESAFE_API_KEY": "env-key"}))
		if err != nil {
			t.Fatalf("ResolveBackendIn() error = %v", err)
		}
		if got.APIKey != "env-key" || got.KeySource != KeySourceEnv {
			t.Errorf("got %q/%q, want env-key/env", got.APIKey, got.KeySource)
		}
	})

	t.Run("whitespace-only key file counts as missing", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(dir, "compaction-openrouter.key"), []byte("  \n\t\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := ResolveBackendIn(dir, "openrouter", envOf(nil))
		var missing *MissingKeyError
		if !errors.As(err, &missing) {
			t.Fatalf("error = %v, want *MissingKeyError", err)
		}
	})
}

// TestResolveBackend_MissingKeyErrorEnumeratesOptions checks that the typed
// error tells the user every place a key can live.
func TestResolveBackend_MissingKeyErrorEnumeratesOptions(t *testing.T) {
	dir := t.TempDir()
	_, err := ResolveBackendIn(dir, "typesafe", envOf(nil))
	var missing *MissingKeyError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %v, want *MissingKeyError", err)
	}
	msg := err.Error()
	for _, want := range []string{"TYPESAFE_API_KEY", filepath.Join(dir, "compaction-typesafe.key")} {
		if !strings.Contains(msg, want) {
			t.Errorf("MissingKeyError message %q does not mention %q", msg, want)
		}
	}
}
