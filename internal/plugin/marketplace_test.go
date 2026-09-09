package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubMarketplace builds an httptest.Server that maps "name" -> JSON entries.
// Names not in the store return 404.
func stubMarketplace(t *testing.T, store map[string]MarketplaceEntry) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for name, entry := range store {
		entry := entry // capture
		name := name
		mux.HandleFunc("/plugins/"+name+".json", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(entry)
		})
	}
	// Catch-all miss handler -> 404.
	mux.HandleFunc("/plugins/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestMarketplaceClient_Resolve_NpmEntry: 200 + npm field → returns the entry verbatim.
func TestMarketplaceClient_Resolve_NpmEntry(t *testing.T) {
	srv := stubMarketplace(t, map[string]MarketplaceEntry{
		"cool-thing": {Npm: "@late/cool-thing", Description: "Cool thing"},
	})
	c := &MarketplaceClient{BaseURL: srv.URL, HTTPClient: srv.Client()}
	got, err := c.Resolve(context.Background(), "cool-thing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Npm != "@late/cool-thing" {
		t.Fatalf("expected npm target, got %+v", got)
	}
	if got.SourceType() != "npm" {
		t.Fatalf("expected source type \"npm\", got %q", got.SourceType())
	}
}

// TestMarketplaceClient_Resolve_GitEntry: 200 + git field → returns the entry verbatim.
func TestMarketplaceClient_Resolve_GitEntry(t *testing.T) {
	srv := stubMarketplace(t, map[string]MarketplaceEntry{
		"git-only": {Git: "github:user/repo"},
	})
	c := &MarketplaceClient{BaseURL: srv.URL, HTTPClient: srv.Client()}
	got, err := c.Resolve(context.Background(), "git-only")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.SourceType() != "git" {
		t.Fatalf("expected source type \"git\", got %q", got.SourceType())
	}
}

// TestMarketplaceClient_Resolve_NotFound_ReturnsMiss: 404 → ErrMarketplaceMiss
// so callers can fall back to npm-as-package.
func TestMarketplaceClient_Resolve_NotFound_ReturnsMiss(t *testing.T) {
	srv := stubMarketplace(t, nil)
	c := &MarketplaceClient{BaseURL: srv.URL, HTTPClient: srv.Client()}
	_, err := c.Resolve(context.Background(), "missing-thing")
	if !errors.Is(err, ErrMarketplaceMiss) {
		t.Fatalf("expected ErrMarketplaceMiss, got %v", err)
	}
}

// TestMarketplaceClient_Resolve_BothEmptyIsDecommissioned: 200 + empty payload
// is rejected (decommissioned).
func TestMarketplaceClient_Resolve_BothEmptyIsDecommissioned(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/plugins/empty.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := &MarketplaceClient{BaseURL: srv.URL, HTTPClient: srv.Client()}
	_, err := c.Resolve(context.Background(), "empty")
	if err == nil {
		t.Fatal("expected error for decommissioned entry, got nil")
	}
	if !contains(err.Error(), "decommissioned") {
		t.Fatalf("expected decommissioned error, got %v", err)
	}
}

// TestMarketplaceClient_Resolve_ServerError_NotMissed: HTTP 500 is NOT a
// cacheable miss — callers should fall back to npm but the error itself
// stays surfaced so the user can debug.
func TestMarketplaceClient_Resolve_ServerError_NotMissed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	t.Cleanup(srv.Close)
	c := &MarketplaceClient{BaseURL: srv.URL, HTTPClient: srv.Client()}
	_, err := c.Resolve(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, ErrMarketplaceMiss) {
		t.Fatalf("500 should NOT be ErrMarketplaceMiss, got %v", err)
	}
	if !contains(err.Error(), "500") {
		t.Fatalf("expected status code in error, got %v", err)
	}
}

// TestMarketplaceClient_Resolve_NilOrEmpty: defensive checks reject
// misconfiguration out — never crash on a nil receiver.
func TestMarketplaceClient_Resolve_NilOrEmpty(t *testing.T) {
	if _, err := ((*MarketplaceClient)(nil)).Resolve(context.Background(), "x"); err == nil {
		t.Fatal("expected error on nil receiver")
	}
	c := &MarketplaceClient{BaseURL: ""}
	if _, err := c.Resolve(context.Background(), "x"); err == nil {
		t.Fatal("expected error on empty BaseURL")
	}
	if _, err := c.Resolve(context.Background(), ""); err == nil {
		t.Fatal("expected error on empty name")
	}
}

// TestDefaultRegistry_NoDefaultURL is a regression test: no registry has
// been published yet, so DefaultRegistryBaseURL must stay empty rather
// than pointing at a placeholder host. DefaultRegistry() honors
// LATE_PLUGIN_REGISTRY when set, and otherwise resolves to an empty
// BaseURL — which Resolve() rejects, so a bare-name install falls through
// to npm (see Install's fallback policy) instead of hitting a
// placeholder endpoint.
func TestDefaultRegistry_NoDefaultURL(t *testing.T) {
	if DefaultRegistryBaseURL != "" {
		t.Fatalf("expected DefaultRegistryBaseURL to be empty until a registry is published, got %q", DefaultRegistryBaseURL)
	}

	t.Setenv("LATE_PLUGIN_REGISTRY", "")
	if c := DefaultRegistry(); c.BaseURL != "" {
		t.Fatalf("expected empty BaseURL with no env override, got %q", c.BaseURL)
	}

	t.Setenv("LATE_PLUGIN_REGISTRY", "https://example.com/registry/")
	if c := DefaultRegistry(); c.BaseURL != "https://example.com/registry" {
		t.Fatalf("expected env override (trailing slash trimmed), got %q", c.BaseURL)
	}
}

// TestMarketplaceEntry_SourceType: explicit table check for the discriminator.
func TestMarketplaceEntry_SourceType(t *testing.T) {
	cases := []struct {
		name  string
		entry *MarketplaceEntry
		want  string
	}{
		{"nil receiver", nil, ""},
		{"npm only", &MarketplaceEntry{Npm: "x"}, "npm"},
		{"git only", &MarketplaceEntry{Git: "y"}, "git"},
		{"both — npm wins precedence", &MarketplaceEntry{Npm: "x", Git: "y"}, "npm"},
		{"neither — decommissioned marker", &MarketplaceEntry{Description: "z"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.entry.SourceType(); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

// contains is a small helper so we don't import strings in just one test
// when fmt.Errorf already formats the same way elsewhere. Returns true
// if needle appears in haystack.
func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestLooksLikeGitSource / TestLooksLikeLocalPath / TestLooksLikeNpmPackage
// pin the dispatcher classifier so future regressions break the build.
func TestLooksLikeSources(t *testing.T) {
	git := []string{"https://github.com/x/y", "http://x/y.git", "git@github.com:x/y", "github:u/r", "gitlab:u/r", "bitbucket:u/r"}
	for _, s := range git {
		if !looksLikeGitSource(s) {
			t.Errorf("expected git: %q", s)
		}
	}
	local := []string{"./mine", "../sibling/mine", "~/d/mine", "/abs/path"}
	for _, s := range local {
		if !looksLikeLocalPath(s) {
			t.Errorf("expected local: %q", s)
		}
	}
	npm := []string{"@scope/pkg", "user/repo"}
	for _, s := range npm {
		if !looksLikeNpmPackage(s) {
			t.Errorf("expected npm: %q", s)
		}
	}
	// Bare names — neither local nor git nor npm.
	bare := []string{"cool-thing", "fmt", "a"}
	for _, s := range bare {
		if looksLikeGitSource(s) || looksLikeLocalPath(s) || looksLikeNpmPackage(s) {
			t.Errorf("bare name should hit marketplace only: %q", s)
		}
	}
	// Avoid a fmt import: this just ensures contains behaves without
	// panicking and shows up in the trace for TUI smoke.
	if contains("abc", "") != true {
		t.Fatal("contains empty substring must be true")
	}
	if contains("abc", "z") != false {
		t.Fatal("contains missing must be false")
	}
	// Keep fmt referenced so it doesn't drift from future use.
	_ = fmt.Sprintf("noop")
}
