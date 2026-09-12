package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// DefaultSuppressedThinkingPhrases contains the canonical string phrases targeted
// to suppress model overthinking when --suppress-thinking-words is enabled.
var DefaultSuppressedThinkingPhrases = []string{
	" Wait",
	"Wait",
	" Hmm",
	"Hmm",
	" Actually",
	"Actually",
	" reconsider",
	" perhaps",
	" maybe",
	" But",
	"But",
}

type tokenizeRequest struct {
	Content string `json:"content"`
}

type tokenizeResponse struct {
	Tokens []int `json:"tokens"`
}

// ResolveThinkingBiases dynamically queries llama-server's /tokenize endpoint for
// each phrase in DefaultSuppressedThinkingPhrases. Only single-token phrases are mapped
// to -100; multi-token phrases are skipped with a log to avoid unintended subword suppression.
func ResolveThinkingBiases(ctx context.Context, endpoint string, apiKey string, httpClient *http.Client) (map[string]int, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid endpoint URL: %w", err)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.Path = strings.TrimSuffix(u.Path, "/v1")
	u.Path = strings.TrimSuffix(u.Path, "/") + "/tokenize"
	tokenizeURL := u.String()

	result := make(map[string]int)

	for _, phrase := range DefaultSuppressedThinkingPhrases {
		reqBody, err := json.Marshal(tokenizeRequest{Content: phrase})
		if err != nil {
			return nil, fmt.Errorf("failed to marshal tokenize request: %w", err)
		}

		httpReq, err := http.NewRequestWithContext(ctx, "POST", tokenizeURL, bytes.NewReader(reqBody))
		if err != nil {
			return nil, fmt.Errorf("failed to create tokenize request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		}

		resp, err := httpClient.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("tokenize request failed for phrase %q: %w", phrase, err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read tokenize response: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("tokenize request returned status %d: %s", resp.StatusCode, string(body))
		}

		var tokResp tokenizeResponse
		if err := json.Unmarshal(body, &tokResp); err != nil {
			return nil, fmt.Errorf("failed to decode tokenize response: %w", err)
		}

		if len(tokResp.Tokens) == 1 {
			tokenIDStr := strconv.Itoa(tokResp.Tokens[0])
			result[tokenIDStr] = -100
		}
	}

	return result, nil
}

// ParseLogitBias parses a logit bias string which can be either:
// - A raw JSON string, e.g. `{"13428": -100, "13784": -100}`
// - Comma-separated key-value pairs, e.g. `13428:-100,13784:-100`
func ParseLogitBias(s string) (map[string]int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}

	result := make(map[string]int)

	// If the string is formatted as a JSON object
	if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
		if err := json.Unmarshal([]byte(s), &result); err == nil {
			for k := range result {
				if _, err := strconv.Atoi(k); err != nil {
					return nil, fmt.Errorf("invalid token ID %q (must be an integer): %w", k, err)
				}
			}
			return result, nil
		}
		var floatMap map[string]float64
		if err := json.Unmarshal([]byte(s), &floatMap); err == nil {
			for k, v := range floatMap {
				if _, err := strconv.Atoi(k); err != nil {
					return nil, fmt.Errorf("invalid token ID %q (must be an integer): %w", k, err)
				}
				if float64(int(v)) != v {
					return nil, fmt.Errorf("invalid bias value %v for token %q (must be a whole number representable as an integer)", v, k)
				}
				result[k] = int(v)
			}
			return result, nil
		}
	}

	// Parse comma-separated key-value pairs: TOKEN_ID:BIAS
	trimmed := strings.Trim(s, "{}")
	pairs := strings.Split(trimmed, ",")
	for _, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		kv := strings.SplitN(pair, ":", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("invalid token bias pair %q (expected TOKEN_ID:BIAS)", pair)
		}
		tokenID := strings.Trim(strings.TrimSpace(kv[0]), `"'`)
		biasStr := strings.Trim(strings.TrimSpace(kv[1]), `"'`)

		if tokenID == "" {
			return nil, fmt.Errorf("empty token ID in pair %q", pair)
		}

		if _, err := strconv.Atoi(tokenID); err != nil {
			return nil, fmt.Errorf("invalid token ID %q (must be an integer): %w", tokenID, err)
		}

		bias, err := strconv.Atoi(biasStr)
		if err != nil {
			f, ferr := strconv.ParseFloat(biasStr, 64)
			if ferr != nil {
				return nil, fmt.Errorf("invalid bias value %q for token %q: %w", biasStr, tokenID, err)
			}
			if float64(int(f)) != f {
				return nil, fmt.Errorf("invalid bias value %q for token %q (must be a whole number representable as an integer)", biasStr, tokenID)
			}
			bias = int(f)
		}
		result[tokenID] = bias
	}

	return result, nil
}
