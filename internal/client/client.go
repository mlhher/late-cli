package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
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
	cfg        Config
	httpClient *http.Client
	backend    BackendType
}

func NewClient(cfg Config) *Client {
	return &Client{
		cfg:     cfg,
		backend: BackendUnknown,
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
		return nil, fmt.Errorf("status: %d", resp.StatusCode)
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
			errCh <- fmt.Errorf("status: %d", resp.StatusCode)
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

	var completionResp CompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completionResp); err != nil {
		return nil, err
	}
	return &completionResp, nil
}

// HealthCheck asserts that the server is reachable and identifies its type.
func (c *Client) HealthCheck(ctx context.Context) error {
	if c.backend == BackendUnknown {
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

// DiscoverBackend probes certain endpoints to identify the inference engine.
func (c *Client) DiscoverBackend(ctx context.Context) BackendType {
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
		c.backend = BackendGenericOpenAI
		return c.backend
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		c.backend = BackendLlamaCPP
	} else {
		c.backend = BackendGenericOpenAI
	}

	return c.backend
}

func (c *Client) IsLlamaCPP() bool {
	return c.backend == BackendLlamaCPP
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
