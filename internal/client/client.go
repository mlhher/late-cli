package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Config struct {
	BaseURL      string
	APIKey       string
	Model        string
	Timeout      time.Duration
	EnableImages bool
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
}

func NewClient(cfg Config) *Client {
	return &Client{
		cfg:     cfg,
		backend: BackendUnknown,
		ctxSize: -1, // -1 means unknown or not applicable
		httpClient: &http.Client{
			Transport: &http.Transport{
				DisableKeepAlives: true,
			},
			Timeout: 0, // Streaming needs no timeout here
		},
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

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
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
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status: %d", resp.StatusCode)
	}
	return nil
}

// RefreshContextSize re-probes the backend properties to update the context size if it's llama.cpp.
func (c *Client) RefreshContextSize(ctx context.Context) {
	c.mu.RLock()
	isLlama := c.backend == BackendLlamaCPP
	c.mu.RUnlock()
	if !isLlama {
		return
	}

	// Try /props at the raw BaseURL and at the parent path
	baseURL := strings.TrimSuffix(c.cfg.BaseURL, "/")
	propsURLs := []string{baseURL + "/props"}

	if u, err := url.Parse(baseURL); err == nil && u.Path != "" && u.Path != "/" {
		parent := strings.TrimSuffix(baseURL, u.Path)
		if parent != baseURL {
			propsURLs = append(propsURLs, parent+"/props")
		}
	}

	for _, propsURL := range propsURLs {
		req, err := http.NewRequestWithContext(ctx, "GET", propsURL, nil)
		if err != nil {
			continue
		}
		if c.cfg.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			continue
		}

		if resp.StatusCode == http.StatusOK {
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err == nil {
				c.mu.Lock()
				c.parsePropsBody(body)

				// If /props returned n_ctx <= 0, try /v1/models as fallback
				if c.ctxSize <= 0 {
					ctxSize := c.probeModelsEndpoint(ctx, baseURL)
					if ctxSize > 0 {
						c.ctxSize = ctxSize
					}
				}
				c.mu.Unlock()
			}
			return
		}
		resp.Body.Close()
	}

	// /props not found or failed — try /v1/models as fallback
	c.mu.Lock()
	ctxSize := c.probeModelsEndpoint(ctx, baseURL)
	if ctxSize > 0 {
		c.ctxSize = ctxSize
	}
	c.mu.Unlock()
}

// DiscoverBackend probes certain endpoints to identify the inference engine.
// It tries `/props` at the raw BaseURL and at the parent path (in case
// BaseURL includes a path prefix like "/v1").
func (c *Client) DiscoverBackend(ctx context.Context) BackendType {
	c.mu.RLock()
	if c.backend == BackendLlamaCPP && c.ctxSize != -1 {
		b := c.backend
		c.mu.RUnlock()
		return b
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check
	if c.backend == BackendLlamaCPP && c.ctxSize != -1 {
		return c.backend
	}

	// Try /props at the raw BaseURL and at the parent path
	baseURL := strings.TrimSuffix(c.cfg.BaseURL, "/")
	propsURLs := []string{baseURL + "/props"}

	// Also try parent path (e.g. if BaseURL is "http://h:8080/v1", try "http://h:8080/props")
	if u, err := url.Parse(baseURL); err == nil && u.Path != "" && u.Path != "/" {
		parent := strings.TrimSuffix(baseURL, u.Path)
		if parent != baseURL {
			propsURLs = append(propsURLs, parent+"/props")
		}
	}

	var propsResp *http.Response
	for _, propsURL := range propsURLs {
		req, err := http.NewRequestWithContext(ctx, "GET", propsURL, nil)
		if err != nil {
			continue
		}
		if c.cfg.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			continue
		}

		if resp.StatusCode == http.StatusOK {
			propsResp = resp
			break
		}
		resp.Body.Close()
	}

	if propsResp != nil {
		defer propsResp.Body.Close()
		c.backend = BackendLlamaCPP

		body, err := io.ReadAll(propsResp.Body)
		propsResp.Body.Close()
		if err == nil {
			c.parsePropsBody(body)
		}
	}

	// If /props returned n_ctx <= 0 or failed entirely, try /v1/models
	// to get the context size from the loaded model's metadata.
	if c.ctxSize <= 0 {
		ctxSize := c.probeModelsEndpoint(ctx, baseURL)
		if ctxSize > 0 {
			c.ctxSize = ctxSize
			c.backend = BackendLlamaCPP
		}
	}

	// If still unknown, mark as generic OpenAI
	if c.backend == BackendUnknown {
		c.backend = BackendGenericOpenAI
	}

	return c.backend
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

// parsePropsBody extracts ctxSize and vision support from a /props JSON body.
// It tries structured decode first, then falls back to raw JSON traversal.
func (c *Client) parsePropsBody(body []byte) {
	var props PropsResponse
	if err := json.Unmarshal(body, &props); err == nil {
		// n_ctx == 0 means "unlimited/default" — still a valid report
		c.ctxSize = props.DefaultGenerationSettings.NCtx
		c.supportsVision = props.Modalities.Vision
		return
	}

	// Fallback: extract n_ctx from raw JSON in case the structure differs
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawMap); err != nil {
		return
	}

	dgs, ok := rawMap["default_generation_settings"]
	if !ok {
		return
	}

	var dgsMap map[string]json.RawMessage
	if err := json.Unmarshal(dgs, &dgsMap); err != nil {
		return
	}

	if nCtxRaw, ok := dgsMap["n_ctx"]; ok {
		var nCtx int
		if json.Unmarshal(nCtxRaw, &nCtx) == nil {
			c.ctxSize = nCtx
		}
	}

	if modRaw, ok := rawMap["modalities"]; ok {
		var mods Modalities
		if json.Unmarshal(modRaw, &mods) == nil {
			c.supportsVision = mods.Vision
		}
	}
}

func (c *Client) getBackend() BackendType {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.backend
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

func (c *Client) formatError(resp *http.Response) error {
	var apiErr APIErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiErr); err == nil && apiErr.Error.Message != "" {
		return fmt.Errorf("API error (%d): %s", resp.StatusCode, apiErr.Error.Message)
	}
	return fmt.Errorf("status: %d", resp.StatusCode)
}
