package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

type Config struct {
	BaseURL      string
	APIKey       string
	Model        string
	Timeout      time.Duration
	EnableImages bool
	LogitBias    map[string]int
	AppVersion   string
	UserAgent    string
}

type BackendType string

const (
	BackendUnknown       BackendType = "unknown"
	BackendLlamaCPP      BackendType = "llama.cpp"
	BackendGenericOpenAI BackendType = "openai"
)

type Client struct {
	mu             sync.RWMutex
	cfg            Config
	httpClient     *http.Client
	backend        BackendType
	ctxSize        int
	supportsVision bool
	userAgent      string
}

func NewClient(cfg Config) *Client {
	ua := cfg.UserAgent
	if ua == "" {
		ver := cfg.AppVersion
		if ver == "" {
			ver = "dev"
		}
		ua = fmt.Sprintf("late-cli/%s (+https://github.com/mlhher/late-cli)", ver)
	}

	return &Client{
		cfg:       cfg,
		backend:   BackendUnknown,
		ctxSize:   -1, // -1 means unknown or not applicable
		userAgent: ua,
		httpClient: &http.Client{
			Transport: &http.Transport{
				DisableKeepAlives: true,
			},
			Timeout: 0, // Streaming needs no timeout here
		},
	}
}

func (c *Client) applyHeaders(req *http.Request) {
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if strings.Contains(strings.ToLower(c.cfg.BaseURL), "openrouter.ai") {
		req.Header.Set("HTTP-Referer", "https://github.com/mlhher/late-cli")
		req.Header.Set("X-OpenRouter-Title", "Late-CLI")
		req.Header.Set("X-OpenRouter-Categories", "cli-agent")
	}
}

// chatCompletionURL builds the chat completions endpoint from BaseURL.
// If the base URL has no path (e.g. "http://localhost:8080"), /v1 is
// appended automatically for backwards compatibility. If a path is already
// present (e.g. "https://api.z.ai/api/coding/paas/v4"), it is used as-is
// and only /chat/completions is appended — consistent with the OpenAI SDK
// convention that base_url is a true base the caller controls.
func (c *Client) chatCompletionURL() string {
	base := strings.TrimSuffix(c.cfg.BaseURL, "/")
	if u, err := url.Parse(base); err == nil && (u.Path == "" || u.Path == "/") {
		base += "/v1"
	}
	return base + "/chat/completions"
}

// ChatCompletion sends a chat prompt to the OpenAI-compatible endpoint.
func (c *Client) ChatCompletion(ctx context.Context, req ChatCompletionRequest) (*ChatCompletionResponse, error) {
	if c.getBackend() == BackendUnknown || (c.getBackend() == BackendLlamaCPP && c.ContextSize() == -1) {
		_ = c.DiscoverBackend(ctx)
	}

	if req.Model == "" && c.cfg.Model != "" {
		req.Model = c.cfg.Model
	}

	req.LogitBias = c.mergeLogitBias(req.LogitBias)

	body, err := c.marshalFlattened(req)
	if err != nil {
		return nil, err
	}

	url := c.chatCompletionURL()
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	if c.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	c.applyHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.formatError(resp)
	}

	var chatResp ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, err
	}
	return &chatResp, nil
}

// ChatCompletionStream streams responses from the OpenAI-compatible endpoint.
func (c *Client) ChatCompletionStream(ctx context.Context, req ChatCompletionRequest) (<-chan ChatCompletionChunk, <-chan error) {
	req.Stream = true
	req.StreamOptions = &StreamOptions{IncludeUsage: true}
	out := make(chan ChatCompletionChunk)
	errCh := make(chan error, 1)

	go func() {
		defer close(out)
		defer close(errCh)

		if c.getBackend() == BackendUnknown || (c.getBackend() == BackendLlamaCPP && c.ContextSize() == -1) {
			_ = c.DiscoverBackend(ctx)
		}

		if req.Model == "" && c.cfg.Model != "" {
			req.Model = c.cfg.Model
		}

		req.LogitBias = c.mergeLogitBias(req.LogitBias)

		body, err := c.marshalFlattened(req)
		if err != nil {
			errCh <- err
			return
		}

		url := c.chatCompletionURL()
		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(body))
		if err != nil {
			errCh <- err
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")

		if c.cfg.APIKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
		}
		c.applyHeaders(httpReq)

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			errCh <- err
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			errCh <- c.formatError(resp)
			return
		}

		if req.OnConnect != nil {
			req.OnConnect()
		}

		scanner := bufio.NewScanner(resp.Body)
		// Some providers emit very long SSE lines (e.g. huge tool-call argument
		// deltas or inline base64 parts). The default 64 KB scanner limit would
		// abort the stream with bufio.ErrTooLong, which callers cannot recover
		// from, so raise the cap.
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")

			// Handle [DONE] sentinel (OpenAI standard)
			if data == "[DONE]" {
				break
			}

			// Handle empty data line — some servers signal end this way
			if data == "" {
				break
			}

			var chunk ChatCompletionChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}
			select {
			case out <- chunk:
			case <-ctx.Done():
				return
			}
		}

		// If the loop exited because scanner.Scan() returned false (connection closed)
		// or an empty data line, check for read errors and propagate them.
		if err := scanner.Err(); err != nil {
			select {
			case errCh <- &StreamInterruptedError{Err: err}:
			default:
			}
		}
	}()

	return out, errCh
}

// Completion sends a raw prompt to llama.cpp (used for Impersonation fallback).
func (c *Client) Completion(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	url := strings.TrimSuffix(c.cfg.BaseURL, "/") + "/completion"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	if c.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	c.applyHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.formatError(resp)
	}

	var completionResp CompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completionResp); err != nil {
		return nil, err
	}
	return &completionResp, nil
}

// HealthCheck asserts that the server is reachable and identifies its type.
func (c *Client) HealthCheck(ctx context.Context) error {
	if c.getBackend() == BackendUnknown {
		_ = c.DiscoverBackend(ctx)
	}

	url := strings.TrimSuffix(c.cfg.BaseURL, "/") + "/health"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	c.applyHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &StatusError{StatusCode: resp.StatusCode, Status: resp.Status}
	}
	return nil
}

// RefreshContextSize re-probes the backend properties to update the context size if it's llama.cpp.
func (c *Client) RefreshContextSize(ctx context.Context) {
	c.mu.RLock()
	isLlama := c.backend == BackendLlamaCPP
	baseURL := strings.TrimSuffix(c.cfg.BaseURL, "/")
	apiKey := c.cfg.APIKey
	c.mu.RUnlock()
	if !isLlama {
		return
	}

	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	propsURLs := []string{baseURL + "/props"}
	if u, err := url.Parse(baseURL); err == nil && u.Path != "" && u.Path != "/" {
		parent := strings.TrimSuffix(baseURL, u.Path)
		if parent != baseURL {
			propsURLs = append(propsURLs, parent+"/props")
		}
	}

	var newCtxSize int
	for _, propsURL := range propsURLs {
		req, err := http.NewRequestWithContext(probeCtx, "GET", propsURL, nil)
		if err != nil {
			continue
		}
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		c.applyHeaders(req)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			continue
		}

		if resp.StatusCode == http.StatusOK {
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err == nil {
				nCtx, _ := parsePropsBodyData(body)
				newCtxSize = nCtx
				if newCtxSize <= 0 {
					newCtxSize = c.probeModelsEndpoint(probeCtx, baseURL)
				}
			}
			break
		}
		resp.Body.Close()
	}

	if newCtxSize <= 0 {
		newCtxSize = c.probeModelsEndpoint(probeCtx, baseURL)
	}

	if newCtxSize > 0 {
		c.mu.Lock()
		c.ctxSize = newCtxSize
		c.mu.Unlock()
	}
}

// DiscoverBackend probes certain endpoints to identify the inference engine.
// It tries `/props` at the raw BaseURL and at the parent path (in case
// BaseURL includes a path prefix like "/v1").
//
// Crucially, this function performs network requests without holding c.mu,
// so that concurrent readers (e.g. TUI rendering ContextSize) are never blocked.
func (c *Client) DiscoverBackend(ctx context.Context) BackendType {
	c.mu.RLock()
	if c.backend == BackendLlamaCPP && c.ctxSize != -1 {
		b := c.backend
		c.mu.RUnlock()
		return b
	}
	baseURL := strings.TrimSuffix(c.cfg.BaseURL, "/")
	apiKey := c.cfg.APIKey
	c.mu.RUnlock()

	// Bound the discovery probe so an unreachable server never hangs indefinitely
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	propsURLs := []string{baseURL + "/props"}
	if u, err := url.Parse(baseURL); err == nil && u.Path != "" && u.Path != "/" {
		parent := strings.TrimSuffix(baseURL, u.Path)
		if parent != baseURL {
			propsURLs = append(propsURLs, parent+"/props")
		}
	}

	var (
		discoveredBackend        = BackendUnknown
		discoveredCtxSize        = -1
		discoveredSupportsVision = false
	)

	for _, propsURL := range propsURLs {
		req, err := http.NewRequestWithContext(probeCtx, "GET", propsURL, nil)
		if err != nil {
			continue
		}
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		c.applyHeaders(req)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			continue
		}

		if resp.StatusCode == http.StatusOK {
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err == nil {
				discoveredBackend = BackendLlamaCPP
				nCtx, vis := parsePropsBodyData(body)
				discoveredCtxSize = nCtx
				discoveredSupportsVision = vis
			}
			break
		}
		resp.Body.Close()
	}

	// If /props returned n_ctx <= 0 or failed entirely, try /v1/models
	if discoveredCtxSize <= 0 {
		ctxSize := c.probeModelsEndpoint(probeCtx, baseURL)
		if ctxSize > 0 {
			discoveredCtxSize = ctxSize
			discoveredBackend = BackendLlamaCPP
		}
	}

	if discoveredBackend == BackendUnknown {
		discoveredBackend = BackendGenericOpenAI
	}

	c.mu.Lock()
	c.backend = discoveredBackend
	if discoveredCtxSize > 0 {
		c.ctxSize = discoveredCtxSize
	}
	if discoveredSupportsVision {
		c.supportsVision = discoveredSupportsVision
	}
	res := c.backend
	c.mu.Unlock()

	return res
}

// probeModelsEndpoint fetches /v1/models and extracts n_ctx from the loaded model.
func (c *Client) probeModelsEndpoint(ctx context.Context, baseURL string) int {
	modelsURLs := []string{baseURL + "/v1/models"}

	// Also try parent path
	if u, err := url.Parse(baseURL); err == nil && u.Path != "" && u.Path != "/" {
		parent := strings.TrimSuffix(baseURL, u.Path)
		if parent != baseURL {
			modelsURLs = append(modelsURLs, parent+"/v1/models")
		}
	}

	for _, url := range modelsURLs {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			continue
		}
		if c.cfg.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
		}
		c.applyHeaders(req)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			continue
		}

		// Parse the OpenAI /v1/models response to find the loaded model's n_ctx
		var modelsResp struct {
			Data []struct {
				ID     string `json:"id"`
				Status struct {
					Value string `json:"value"`
				} `json:"status"`
				Meta *struct {
					NCtx int `json:"n_ctx"`
				} `json:"meta,omitempty"`
			} `json:"data"`
		}

		if err := json.Unmarshal(body, &modelsResp); err != nil {
			continue
		}

		for _, m := range modelsResp.Data {
			if m.Status.Value == "loaded" && m.Meta != nil && m.Meta.NCtx > 0 {
				return m.Meta.NCtx
			}
		}

		// Also accept any model with meta.n_ctx, even if not marked loaded
		for _, m := range modelsResp.Data {
			if m.Meta != nil && m.Meta.NCtx > 0 {
				return m.Meta.NCtx
			}
		}
	}

	return 0
}

// parsePropsBodyData extracts ctxSize and vision support from a /props JSON body.
func parsePropsBodyData(body []byte) (int, bool) {
	var props PropsResponse
	if err := json.Unmarshal(body, &props); err == nil {
		return props.DefaultGenerationSettings.NCtx, props.Modalities.Vision
	}

	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawMap); err != nil {
		return 0, false
	}

	var (
		nCtx int
		vis  bool
	)
	if dgs, ok := rawMap["default_generation_settings"]; ok {
		var dgsMap map[string]json.RawMessage
		if json.Unmarshal(dgs, &dgsMap) == nil {
			if nCtxRaw, ok := dgsMap["n_ctx"]; ok {
				_ = json.Unmarshal(nCtxRaw, &nCtx)
			}
		}
	}

	if modRaw, ok := rawMap["modalities"]; ok {
		var mods Modalities
		if json.Unmarshal(modRaw, &mods) == nil {
			vis = mods.Vision
		}
	}
	return nCtx, vis
}

func (c *Client) getBackend() BackendType {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.backend
}

func (c *Client) Backend() BackendType {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.backend
}

func (c *Client) BaseURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg.BaseURL
}

func (c *Client) ContextSize() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ctxSize
}

func (c *Client) IsLlamaCPP() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.backend == BackendLlamaCPP
}

func (c *Client) SupportsVision() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg.EnableImages || c.supportsVision
}

func (c *Client) marshalFlattened(req ChatCompletionRequest) ([]byte, error) {
	// Marshal the request normally first
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	// Unmarshal into a map
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}

	// Move everything from extra_body to the root
	if extra, ok := m["extra_body"].(map[string]any); ok {
		for k, v := range extra {
			m[k] = v
		}
		// Remove the extra_body field
		delete(m, "extra_body")
	}

	return json.Marshal(m)
}

// StatusError is a non-2xx HTTP response returned by the LLM server.
// It is errors.As-able so callers can classify retryability by status code.
// When the provider's JSON error body includes error.type / error.code, they
// are preserved in Type and Code (the code may be a string or a number) for
// retry classification and incident diagnosis.
type StatusError struct {
	StatusCode int
	Status     string // e.g. "500 Internal Server Error"
	// Body is the diagnostic response body: the provider's error.message when
	// the body is a JSON API error, otherwise a bounded, sanitized text
	// fallback. It is truncated to at most 1024 bytes (rune-safe).
	Body string
	// RetryAfter is the delay requested by the server via the Retry-After
	// header (delta-seconds or HTTP-date form), 0 when absent or invalid.
	// The executor honors it as a floor for the retry backoff.
	RetryAfter time.Duration
	Code       any    // provider error code (JSON error.code), if provided
	Type       string // provider error type (JSON error.type), if provided
}

// Error renders the same messages the previous fmt.Errorf calls produced,
// so logs and tests that match on the text keep working:
// "API error (%d): %s" when a body/message is available, "status: %d" otherwise.
func (e *StatusError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("API error (%d): %s", e.StatusCode, e.Body)
	}
	return fmt.Sprintf("status: %d", e.StatusCode)
}

// ErrPayloadTooLarge is the stable sentinel carried by every HTTP 413
// (Request Entity Too Large) API error: the provider rejected the request
// BODY outright, so resending the identical body can never succeed. Callers
// classify with errors.Is and must never retry — the executor's stream
// retry tiers all treat it as fail-fast — and the TUI surfaces the recovery
// guidance carried in the error text (PayloadTooLargeGuidance).
var ErrPayloadTooLarge = errors.New("payload too large")

// PayloadTooLargeGuidance is the actionable message rendered for 413
// failures. It travels inside the error text itself, so every surface that
// prints the error (TUI error box, logs) carries the recovery steps without
// any special-casing on the display side.
const PayloadTooLargeGuidance = "request body exceeds this provider's limit (413): compact the context with /jev-compact-context (consider raising compaction-threshold in config.json) or start a new session with /new"

// PayloadTooLargeError marks an HTTP 413 response. It wraps the underlying
// StatusError so errors.As still recovers the status/body/code details, and
// it carries ErrPayloadTooLarge so errors.Is classifies it anywhere in the
// (executor's "stream error: %w") wrap chain. Its Error text is the
// actionable guidance, not the legacy "API error (413): ..." line: a 413 is
// always operator-actionable, and the provider's raw body (which only
// repeats the verdict or names the byte cap) is appended for diagnostics.
type PayloadTooLargeError struct {
	Status *StatusError
}

func (e *PayloadTooLargeError) Error() string {
	if e.Status != nil && e.Status.Body != "" {
		return PayloadTooLargeGuidance + " (provider: " + e.Status.Body + ")"
	}
	return PayloadTooLargeGuidance
}

// Unwrap exposes both the sentinel (for errors.Is classification) and the
// underlying StatusError (for errors.As recovery of StatusCode/Body/Type).
func (e *PayloadTooLargeError) Unwrap() []error {
	return []error{ErrPayloadTooLarge, e.Status}
}

// StreamInterruptedError reports a transport failure while reading a
// 200-OK response body mid-stream: connection reset, HTTP/2 RST_STREAM
// or GOAWAY, truncated body. The server already accepted the request,
// so the failure is infrastructure, not the request's content.
type StreamInterruptedError struct {
	Err error // underlying transport error (e.g. http2 StreamError)
}

func (e *StreamInterruptedError) Error() string {
	return fmt.Sprintf("stream interrupted: %v", e.Err)
}

func (e *StreamInterruptedError) Unwrap() error { return e.Err }

const (
	// maxErrorBodyBytes bounds how much of an error response body is read
	// before parsing: a hostile or broken server can send arbitrarily large
	// bodies, and slurping one whole could exhaust memory.
	maxErrorBodyBytes = 8192
	// maxErrorMessageBytes bounds the diagnostic text stored on StatusError,
	// for both the structured JSON message and the sanitized text fallback.
	maxErrorMessageBytes = 1024
)

// formatError converts a non-2xx response into a *StatusError. The error body
// is read exactly once, bounded by maxErrorBodyBytes: a decoder that consumes
// bytes before failing would lose them, and an unbounded read could exhaust
// memory on a hostile server. When the body is a JSON API error, its message,
// type, and code are preserved; otherwise a bounded, sanitized text fallback
// keeps plain-text and HTML failures diagnosable. A Retry-After header
// (delta-seconds or HTTP-date form) is captured so the retry executor can
// honor the server's requested pacing.
func (c *Client) formatError(resp *http.Response) error {
	se := &StatusError{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
	}
	// Read the error body once, bounded. Read errors are best-effort: the
	// body is treated as empty so the status is still reported.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	var apiErr APIErrorResponse
	if err := json.Unmarshal(body, &apiErr); err == nil {
		// Preserve provider error type/code when present; zero values stay unset.
		if apiErr.Error.Type != "" {
			se.Type = apiErr.Error.Type
		}
		if apiErr.Error.Code != nil {
			se.Code = apiErr.Error.Code
		}
		if apiErr.Error.Message != "" {
			se.Body = truncateErrorText(apiErr.Error.Message, maxErrorMessageBytes)
		} else {
			// JSON body without a structured message: fall back to the
			// sanitized text so something is still diagnosable.
			se.Body = sanitizeErrorText(string(body), maxErrorMessageBytes)
		}
	} else {
		// Structured message unavailable: bounded, sanitized text fallback
		// so plain-text/HTML failures are still diagnosable.
		se.Body = sanitizeErrorText(string(body), maxErrorMessageBytes)
	}
	if ra := parseRetryAfter(resp.Header.Get("Retry-After")); ra > 0 {
		se.RetryAfter = ra
	}
	// 413 is classified, not just reported: the request body itself exceeded
	// the provider's limit, so the error carries the ErrPayloadTooLarge
	// sentinel and the actionable guidance instead of the generic status
	// line. RetryAfter stays parsed (harmless: no retry tier consumes a 413).
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		return &PayloadTooLargeError{Status: se}
	}
	return se
}

// parseRetryAfter parses a Retry-After header value in either delta-seconds
// ("2") or HTTP-date ("Wed, 21 Oct 2015 07:28:00 GMT") form. Empty, invalid,
// and non-positive values yield 0, which callers treat as "no requested
// delay"; HTTP dates are measured against the current time.
func parseRetryAfter(v string) time.Duration {
	return parseRetryAfterAt(v, time.Now())
}

// parseRetryAfterAt is parseRetryAfter with an injectable clock so the
// HTTP-date form can be tested deterministically.
func parseRetryAfterAt(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if date, err := http.ParseTime(v); err == nil {
		// Delay until the requested instant; 0 when it already passed.
		if d := date.Sub(now); d > 0 {
			return d
		}
		return 0
	}
	return 0
}

// sanitizeErrorText turns an arbitrary error body (plain text, HTML, binary
// junk) into a bounded diagnostic string: \r is dropped, control characters
// other than \n and \t are stripped, and runs of whitespace (including
// newlines and tabs) collapse to a single space, so the result is effectively
// single-line. It is trimmed and then truncated to at most limit bytes
// (rune-safe).
func sanitizeErrorText(s string, limit int) string {
	var b strings.Builder
	b.Grow(len(s))
	lastSpace := false
	for _, r := range s {
		if r == '\r' {
			continue
		}
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			continue
		}
		if unicode.IsSpace(r) {
			if !lastSpace {
				b.WriteRune(' ')
				lastSpace = true
			}
			continue
		}
		lastSpace = false
		b.WriteRune(r)
	}
	return truncateErrorText(strings.TrimSpace(b.String()), limit)
}

// truncateErrorText limits s to at most limit bytes without splitting a
// multi-byte rune: when limit falls inside a rune, the cut moves back to the
// start of that rune.
func truncateErrorText(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func (c *Client) APIKey() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg.APIKey
}

func (c *Client) HTTPClient() *http.Client {
	return c.httpClient
}

func (c *Client) LogitBias() map[string]int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg.LogitBias
}

func (c *Client) SetLogitBias(bias map[string]int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg.LogitBias = bias
}

// MergeLogitBiases merges dynamic biases with user overrides.
// User overrides take precedence over dynamic defaults on key collisions.
func MergeLogitBiases(defaults, overrides map[string]int) map[string]int {
	if len(defaults) == 0 && len(overrides) == 0 {
		return nil
	}
	merged := make(map[string]int, len(defaults)+len(overrides))
	for k, v := range defaults {
		merged[k] = v
	}
	for k, v := range overrides {
		merged[k] = v
	}
	return merged
}

func (c *Client) mergeLogitBias(reqBias map[string]int) map[string]int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return MergeLogitBiases(c.cfg.LogitBias, reqBias)
}
