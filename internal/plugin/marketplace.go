package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultRegistryBaseURL is the canonical marketplace endpoint. It can be
// overridden by LATE_PLUGIN_REGISTRY (no trailing slash expected —
// "/plugins/<name>.json" is appended dynamically).
//
// Deliberately empty for now — no registry is published yet. With no
// BaseURL configured, Resolve() returns an error and Install falls
// through to npm interpretation for any unresolved bare name (see the
// fallback policy in Install). Revisit once a real registry exists.
const DefaultRegistryBaseURL = ""

// ErrMarketplaceMiss indicates the registry returned 404 for the
// requested plugin name. Callers should fall back to plain npm
// interpretation of the bare name.
var ErrMarketplaceMiss = errors.New("marketplace: plugin not found")

// MarketplaceEntry is the registry's JSON shape for a single plugin.
// `npm` and `git` are mutually exclusive — at most one is set. When both
// are empty the registry is signaling "decommissioned".
type MarketplaceEntry struct {
	Npm         string `json:"npm,omitempty"`
	Git         string `json:"git,omitempty"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`
}

// SourceType returns "npm" or "git" depending on which field is set.
// Discriminates an empty entry as "".
func (e *MarketplaceEntry) SourceType() string {
	switch {
	case e == nil:
		return ""
	case e.Npm != "":
		return "npm"
	case e.Git != "":
		return "git"
	default:
		return ""
	}
}

// MarketplaceClient resolves bare plugin names to installable targets by
// consulting a JSON registry. Production code uses DefaultRegistry().
// Tests can construct a client pointed at an httptest.Server so no real
// network I/O is needed.
type MarketplaceClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// DefaultRegistry returns the MarketplaceClient used by Install. Honors
// LATE_PLUGIN_REGISTRY from the environment if it is set, else falls back
// to DefaultRegistryBaseURL. The HTTP client has a sensible 5s timeout so
// a slow registry never stalls `late plugin install`.
func DefaultRegistry() MarketplaceClient {
	base := os.Getenv("LATE_PLUGIN_REGISTRY")
	if base == "" {
		base = DefaultRegistryBaseURL
	}
	base = strings.TrimRight(base, "/")
	return MarketplaceClient{
		BaseURL:    base,
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
	}
}

// Resolve looks up a plugin by its short name on the configured registry.
// On 404 it returns ErrMarketplaceMiss so the caller can fall back to
// npm; on any other error or non-200 status it returns the underlying
// failure (no fallback is attempted at this layer — callers decide).
func (c *MarketplaceClient) Resolve(ctx context.Context, name string) (*MarketplaceEntry, error) {
	if c == nil || c.BaseURL == "" {
		return nil, fmt.Errorf("marketplace: client not configured")
	}
	if name == "" {
		return nil, fmt.Errorf("marketplace: empty plugin name")
	}
	url := c.BaseURL + "/plugins/" + name + ".json"
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("marketplace: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("marketplace: get %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrMarketplaceMiss
	}
	if resp.StatusCode != http.StatusOK {
		// Limit how much we read so a hostile server can't blow up memory.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("marketplace: %s returned %d: %s", url, resp.StatusCode, string(body))
	}
	var entry MarketplaceEntry
	if err := json.NewDecoder(resp.Body).Decode(&entry); err != nil {
		return nil, fmt.Errorf("marketplace: decode %s: %w", url, err)
	}
	if entry.Npm == "" && entry.Git == "" {
		return nil, fmt.Errorf("marketplace: %s is decommissioned", name)
	}
	return &entry, nil
}
