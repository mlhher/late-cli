// Package compaction is late's Go port of jev-compaction
// (github.com/Waxmell114514/jev-compaction, MIT): a System One "decision
// client" that scores how essential each segment of a tool output is for the
// ongoing task, so a later stage can elide the low-value tail of the context
// window instead of cramming everything in.
//
// Stage 1 (ScoreToolOutput) segments tool outputs, scores the segments
// through the System One decisions protocol, and appends one JSONL line per
// decision to a shadow log; Replay() turns the accumulated log into the
// numbers a threshold decision needs. Stage 2 (EnableRelocation +
// CompactToolOutput) additionally elides low-scoring segments into the
// persistent record Store and replaces each run with an [[elided …]]
// pointer line that the expand tool (internal/tool) resolves back — the
// executor consults the pipeline for oversized tool results before they
// enter history, and session.CompactContext reuses the same scorer, gate
// vocabulary, and store for full-history compaction. Shadow mode
// (compaction-mode "shadow") is still report-only: stage 2 only runs when
// relocation is armed.
//
// Fail-open contract: scoring is best-effort. Any item that cannot be scored
// (provider outage, bad answer, oversized item) comes back with a score of
// 1.0 ("keep") plus a recorded error, so a compaction decision can never be
// wrong because the scorer was down — at worst it degenerates to "keep
// everything".
package compaction

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"late/internal/pathutil"
)

// Backend names accepted by --api / JEV_API and stored on Backend.Name.
const (
	BackendTypesafe   = "typesafe"
	BackendOpenRouter = "openrouter"
	BackendGateway    = "gateway"
)

// Environment variables consulted by ResolveBackend.
const (
	// EnvJevAPI overrides backend auto-detection with an explicit backend
	// name (the env form of the --api flag).
	EnvJevAPI = "JEV_API"
	// EnvJevGatewayURL holds the gateway backend's decisions endpoint; the
	// gateway is the one backend whose URL is user-provided rather than
	// compiled in.
	EnvJevGatewayURL = "JEV_GATEWAY_URL"
	// EnvJevGatewayKey is the gateway backend's API key variable.
	EnvJevGatewayKey = "JEV_GATEWAY_API_KEY"
)

// Backend is one System One decisions provider. The three known backends
// speak the same protocol; they differ only in endpoint, model id, and where
// the API key lives.
type Backend struct {
	// Name is the stable identifier used by --api / JEV_API.
	Name string
	// URL is the absolute decisions endpoint. Empty for the gateway backend
	// until ResolveBackend fills it in from EnvJevGatewayURL.
	URL string
	// Model is the decisions model id sent as the request's "model".
	Model string
	// KeyEnv is the environment variable checked first for the API key.
	KeyEnv string
	// KeyFile is the file name (under late's config dir) checked second:
	// ~/.config/late/compaction-<name>.key.
	KeyFile string
}

// KeySource says where a resolved API key came from.
type KeySource string

const (
	KeySourceEnv  KeySource = "env"
	KeySourceFile KeySource = "file"
)

// ResolvedBackend is a Backend ready to call: URL resolved (the gateway's
// comes from JEV_GATEWAY_URL) and API key found.
type ResolvedBackend struct {
	Backend   Backend
	APIKey    string
	KeySource KeySource
	// KeyPath is the key file consulted, "" when the key came from the env.
	KeyPath string
}

// backendSpec is the internal registry entry: a Backend plus the env variable
// that supplies a non-static URL (the gateway's endpoint is user-provided).
type backendSpec struct {
	backend Backend
	urlEnv  string
}

// backendOrder is the auto-detection precedence: typesafe first.
var backendOrder = []string{BackendTypesafe, BackendOpenRouter, BackendGateway}

// backendSpecs returns the registry of known System One backends. The URLs,
// model ids, and key variables are pinned by the port and asserted in
// providers_test.go — change them there and here together.
func backendSpecs() map[string]backendSpec {
	return map[string]backendSpec{
		BackendTypesafe: {
			backend: Backend{
				Name:    BackendTypesafe,
				URL:     "https://api.typesafe.ai/v1/systemone",
				Model:   "jev-latest",
				KeyEnv:  "TYPESAFE_API_KEY",
				KeyFile: "compaction-typesafe.key",
			},
		},
		BackendOpenRouter: {
			backend: Backend{
				Name:    BackendOpenRouter,
				URL:     "https://openrouter.ai/api/alpha/decisions",
				Model:   "~typesafe/jev-latest",
				KeyEnv:  "OPENROUTER_API_KEY",
				KeyFile: "compaction-openrouter.key",
			},
		},
		BackendGateway: {
			backend: Backend{
				Name:    BackendGateway,
				Model:   "jev-latest",
				KeyEnv:  EnvJevGatewayKey,
				KeyFile: "compaction-gateway.key",
			},
			urlEnv: EnvJevGatewayURL,
		},
	}
}

// MissingKeyError reports that the selected backend has no API key in any of
// the places late looks. It is typed so callers can errors.As it and print
// tailored guidance.
type MissingKeyError struct {
	Backend string
	KeyEnv  string
	// KeyFile is the absolute key-file path that was consulted ("" when the
	// config dir itself was unavailable).
	KeyFile string
}

func (e *MissingKeyError) Error() string {
	if e.KeyFile == "" {
		return fmt.Sprintf(
			"compaction: no API key for backend %q: set %s=<key> in the environment (the user config dir is unavailable, so the key file cannot be used)",
			e.Backend, e.KeyEnv)
	}
	return fmt.Sprintf(
		"compaction: no API key for backend %q: set %s=<key> in the environment, or write the key to %s",
		e.Backend, e.KeyEnv, e.KeyFile)
}

// UnknownBackendError reports an unrecognized --api / JEV_API name.
type UnknownBackendError struct {
	Name string
}

func (e *UnknownBackendError) Error() string {
	return fmt.Sprintf(
		"compaction: unknown System One backend %q (known backends: %s; or unset to auto-detect)",
		e.Name, strings.Join(backendOrder, ", "))
}

// GatewayURLError reports a gateway selection without JEV_GATEWAY_URL: the
// gateway's endpoint is user-provided and there is nothing to call without it.
type GatewayURLError struct {
	Backend string
}

func (e *GatewayURLError) Error() string {
	return fmt.Sprintf(
		"compaction: backend %q needs an endpoint URL: set %s=https://your-gateway/decisions",
		e.Backend, EnvJevGatewayURL)
}

// NoBackendError reports that auto-detection found no fully usable backend.
type NoBackendError struct {
	Detail string
}

func (e *NoBackendError) Error() string {
	return "compaction: no System One backend available (tried " + strings.Join(backendOrder, ", ") + "): " + e.Detail
}

// ResolveBackend picks the System One backend for this run.
//
// Precedence: an explicit backend name (the --api param, name != "") beats
// the JEV_API env var, which beats auto-detection — the first backend in
// [typesafe, openrouter, gateway] that is fully usable (has a key and, for
// the gateway, a URL). env is the environment lookup (os.Getenv); it is a
// parameter so callers and tests can pin the environment.
//
// An explicitly selected backend never falls through to another one: a
// missing key or URL for the named backend is a typed error enumerating the
// options, not a silent switch.
func ResolveBackend(name string, env func(string) string) (ResolvedBackend, error) {
	return resolveBackend(defaultKeyDir(), name, env)
}

// ResolveBackendEnv is ResolveBackend against the process environment.
func ResolveBackendEnv(name string) (ResolvedBackend, error) {
	return ResolveBackend(name, os.Getenv)
}

// ResolveBackendIn is ResolveBackend with an explicit config dir holding the
// compaction-<name>.key files (tests inject a temp dir).
func ResolveBackendIn(keyDir, name string, env func(string) string) (ResolvedBackend, error) {
	return resolveBackend(keyDir, name, env)
}

func resolveBackend(keyDir, name string, env func(string) string) (ResolvedBackend, error) {
	if env == nil {
		env = os.Getenv
	}
	specs := backendSpecs()

	pick := strings.TrimSpace(name)
	if pick == "" {
		pick = strings.TrimSpace(env(EnvJevAPI))
	}
	if pick != "" {
		spec, ok := specs[strings.ToLower(pick)]
		if !ok {
			return ResolvedBackend{}, &UnknownBackendError{Name: pick}
		}
		return resolveKey(spec, env, keyDir)
	}

	// Auto-detect: the first fully usable backend in precedence order. A
	// backend that is merely unconfigured (no key yet, or the gateway with
	// no URL) is skipped and reported if nothing else pans out.
	var attempts []string
	for _, n := range backendOrder {
		res, err := resolveKey(specs[n], env, keyDir)
		if err == nil {
			return res, nil
		}
		attempts = append(attempts, fmt.Sprintf("%s: %v", n, err))
	}
	return ResolvedBackend{}, &NoBackendError{Detail: strings.Join(attempts, "; ")}
}

// resolveKey resolves a backend spec into a callable ResolvedBackend: URL
// first (gateway only), then the API key from KeyEnv, then the key file.
func resolveKey(spec backendSpec, env func(string) string, keyDir string) (ResolvedBackend, error) {
	b := spec.backend
	if b.URL == "" && spec.urlEnv != "" {
		u := strings.TrimSpace(env(spec.urlEnv))
		if u == "" {
			return ResolvedBackend{}, &GatewayURLError{Backend: b.Name}
		}
		b.URL = strings.TrimSuffix(u, "/")
	}

	keyPath := ""
	if keyDir != "" && b.KeyFile != "" {
		keyPath = filepath.Join(keyDir, b.KeyFile)
	}
	if k := strings.TrimSpace(env(b.KeyEnv)); k != "" {
		return ResolvedBackend{Backend: b, APIKey: k, KeySource: KeySourceEnv}, nil
	}
	if keyPath != "" {
		// The key file holds just the key; surrounding whitespace (a
		// trailing newline from an editor) is tolerated.
		if data, err := os.ReadFile(keyPath); err == nil {
			if k := strings.TrimSpace(string(data)); k != "" {
				return ResolvedBackend{Backend: b, APIKey: k, KeySource: KeySourceFile, KeyPath: keyPath}, nil
			}
		}
	}
	return ResolvedBackend{}, &MissingKeyError{Backend: b.Name, KeyEnv: b.KeyEnv, KeyFile: keyPath}
}

// defaultKeyDir returns late's config dir (os.UserConfigDir()/late), where
// the compaction-<name>.key files live; "" when it cannot be determined.
func defaultKeyDir() string {
	dir, err := pathutil.LateConfigDir()
	if err != nil {
		return ""
	}
	return dir
}
