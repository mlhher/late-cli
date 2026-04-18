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

type Client struct {
	cfg        Config
	httpClient *http.Client
}

func NewClient(cfg Config) *Client {
	return &Client{
		cfg: cfg,
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

	body, err := json.Marshal(req)
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

		body, err := json.Marshal(req)
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

// HealthCheck asserts that the llama.cpp server is reachable.
func (c *Client) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.cfg.BaseURL+"/health", nil)
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
