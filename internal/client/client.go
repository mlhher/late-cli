package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
}

type BackendType string

const (
	BackendUnknown       BackendType = "unknown"
	BackendLlamaCPP      BackendType = "llama.cpp"
	BackendGenericOpenAI BackendType = "openai"
)

type Client struct {
	mu         sync.RWMutex
	cfg        Config
	httpClient *http.Client
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

	url := strings.TrimSuffix(c.cfg.BaseURL, "/") + "/v1/chat/completions"
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

		url := strings.TrimSuffix(c.cfg.BaseURL, "/") + "/v1/chat/completions"
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

	url := strings.TrimSuffix(c.cfg.BaseURL, "/") + "/props"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return
	}
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var props PropsResponse
		if err := json.NewDecoder(resp.Body).Decode(&props); err == nil {
			c.mu.Lock()
			c.ctxSize = props.DefaultGenerationSettings.NCtx
			c.supportsVision = props.Modalities.Vision
			c.mu.Unlock()
		}
	}
}

// DiscoverBackend probes certain endpoints to identify the inference engine.
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

	url := strings.TrimSuffix(c.cfg.BaseURL, "/") + "/props"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		c.backend = BackendGenericOpenAI
		return c.backend
	}

	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Connection error: remain unknown to allow retry
		return c.backend
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		c.backend = BackendLlamaCPP
		var props PropsResponse
		if err := json.NewDecoder(resp.Body).Decode(&props); err == nil {
			c.ctxSize = props.DefaultGenerationSettings.NCtx
			c.supportsVision = props.Modalities.Vision
		}
	} else {
		// If we got a response but it's not OK, it's likely a standard OpenAI endpoint
		c.backend = BackendGenericOpenAI
	}

	return c.backend
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
	return c.supportsVision
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
