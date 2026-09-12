package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestParseLogitBias(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    map[string]int
		wantErr bool
	}{
		{
			name:    "empty string",
			input:   "",
			want:    nil,
			wantErr: false,
		},
		{
			name:    "whitespace only",
			input:   "   ",
			want:    nil,
			wantErr: false,
		},
		{
			name:  "raw JSON string",
			input: `{"13428": -100, "13784": -100}`,
			want: map[string]int{
				"13428": -100,
				"13784": -100,
			},
			wantErr: false,
		},
		{
			name:  "comma-separated pairs",
			input: "13428:-100,13784:-100,85152:-100",
			want: map[string]int{
				"13428": -100,
				"13784": -100,
				"85152": -100,
			},
			wantErr: false,
		},
		{
			name:  "comma-separated pairs with whitespace and quotes",
			input: ` "13428" : "-100" , 13784 : -100 `,
			want: map[string]int{
				"13428": -100,
				"13784": -100,
			},
			wantErr: false,
		},
		{
			name:  "raw JSON with floats",
			input: `{"13428": -100.0, "85152": 50}`,
			want: map[string]int{
				"13428": -100,
				"85152": 50,
			},
			wantErr: false,
		},
		{
			name:  "pairs with whole-number decimal",
			input: "13428:-100.0",
			want:  map[string]int{"13428": -100},
		},
		{
			name:    "fractional JSON bias",
			input:   `{"13428": -0.5}`,
			wantErr: true,
		},
		{
			name:    "fractional pair bias",
			input:   "13428:64.5",
			wantErr: true,
		},
		{
			name:    "invalid pair format",
			input:   "13428-100",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "invalid bias value",
			input:   "13428:notanumber",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "empty token id",
			input:   ":-100",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "non-numeric token id",
			input:   "Wait:-100",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "non-numeric token id in JSON",
			input:   `{"Wait": -100}`,
			want:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLogitBias(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseLogitBias(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseLogitBias(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestDefaultSuppressedThinkingPhrases(t *testing.T) {
	expectedPhrases := []string{
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

	if len(DefaultSuppressedThinkingPhrases) != len(expectedPhrases) {
		t.Fatalf("expected %d phrases in DefaultSuppressedThinkingPhrases, got %d", len(expectedPhrases), len(DefaultSuppressedThinkingPhrases))
	}

	for i, phrase := range expectedPhrases {
		if DefaultSuppressedThinkingPhrases[i] != phrase {
			t.Errorf("phrase at %d expected %q, got %q", i, phrase, DefaultSuppressedThinkingPhrases[i])
		}
	}
}

func TestResolveThinkingBiases(t *testing.T) {
	// Mock tokenize server that returns single token for " Wait" and "Wait",
	// multi-token for " Hmm" (should be skipped), and handles unknown phrases
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tokenize" {
			http.NotFound(w, r)
			return
		}
		var req tokenizeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		switch req.Content {
		case " Wait":
			json.NewEncoder(w).Encode(tokenizeResponse{Tokens: []int{13428}})
		case "Wait":
			json.NewEncoder(w).Encode(tokenizeResponse{Tokens: []int{13784}})
		case " Hmm":
			// Multi-token response: should be skipped
			json.NewEncoder(w).Encode(tokenizeResponse{Tokens: []int{100, 200}})
		default:
			// Multi-token for all others to test skipping
			json.NewEncoder(w).Encode(tokenizeResponse{Tokens: []int{999, 888}})
		}
	}))
	defer ts.Close()

	resolved, err := ResolveThinkingBiases(context.Background(), ts.URL, "", ts.Client())
	if err != nil {
		t.Fatalf("unexpected error resolving thinking biases: %v", err)
	}

	if len(resolved) != 2 {
		t.Fatalf("expected 2 resolved single-token biases, got %d: %v", len(resolved), resolved)
	}

	if val, ok := resolved["13428"]; !ok || val != -100 {
		t.Errorf("expected resolved['13428'] = -100, got %d (ok=%v)", val, ok)
	}
	if val, ok := resolved["13784"]; !ok || val != -100 {
		t.Errorf("expected resolved['13784'] = -100, got %d (ok=%v)", val, ok)
	}

	// Test endpoint with reverse proxy prefix and /v1 suffix (e.g. http://server/prefix/v1)
	prefixTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/custom/prefix/tokenize" {
			http.NotFound(w, r)
			return
		}
		var req tokenizeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if req.Content == " Wait" {
			json.NewEncoder(w).Encode(tokenizeResponse{Tokens: []int{13428}})
		} else {
			json.NewEncoder(w).Encode(tokenizeResponse{Tokens: []int{1, 2}})
		}
	}))
	defer prefixTS.Close()

	prefixResolved, err := ResolveThinkingBiases(context.Background(), prefixTS.URL+"/custom/prefix/v1", "", prefixTS.Client())
	if err != nil {
		t.Fatalf("unexpected error resolving thinking biases with prefix: %v", err)
	}
	if prefixResolved["13428"] != -100 {
		t.Errorf("expected prefixResolved['13428'] = -100, got %d", prefixResolved["13428"])
	}

	// Test authentication header propagation
	var capturedAuthHeader string
	authTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tokenizeResponse{Tokens: []int{1, 2}})
	}))
	defer authTS.Close()

	_, err = ResolveThinkingBiases(context.Background(), authTS.URL, "secret-token", authTS.Client())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedAuthHeader != "Bearer secret-token" {
		t.Errorf("expected Authorization header 'Bearer secret-token', got %q", capturedAuthHeader)
	}
}

func TestLogitBiasPrecedence(t *testing.T) {
	// Defaults from dynamic thinking suppression
	dynamicDefaults := map[string]int{
		"13428": -100,
		"13784": -100,
		"85152": -100,
	}

	// User overrides via --logit-bias
	userOverrides := map[string]int{
		"13428": 50,    // Collision: user override must take precedence over dynamic default
		"99999": -50,   // Additional user token
	}

	merged := MergeLogitBiases(dynamicDefaults, userOverrides)

	// Verify collision override
	if merged["13428"] != 50 {
		t.Errorf("expected merged['13428'] to be 50 (user override), got %d", merged["13428"])
	}
	// Verify default kept when no collision
	if merged["13784"] != -100 {
		t.Errorf("expected merged['13784'] to be -100 (dynamic default), got %d", merged["13784"])
	}
	if merged["85152"] != -100 {
		t.Errorf("expected merged['85152'] to be -100 (dynamic default), got %d", merged["85152"])
	}
	// Verify user-only token present
	if merged["99999"] != -50 {
		t.Errorf("expected merged['99999'] to be -50 (user token), got %d", merged["99999"])
	}
}

func TestPayloadInjection_LogitBias(t *testing.T) {
	var capturedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		bodyBytes, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bodyBytes, &capturedBody)

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL: server.URL,
		LogitBias: map[string]int{
			"13428": -100,
			"13784": -100,
		},
	})

	req := ChatCompletionRequest{
		Model: "test-model",
		Messages: []ChatMessage{
			{Role: "user", Content: TextContent("hello")},
		},
	}

	_, err := client.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logitBiasRaw, ok := capturedBody["logit_bias"]
	if !ok {
		t.Fatalf("expected logit_bias field in request body, got %v", capturedBody)
	}

	logitBias, ok := logitBiasRaw.(map[string]any)
	if !ok {
		t.Fatalf("expected logit_bias to be map[string]any, got %T", logitBiasRaw)
	}

	if val, ok := logitBias["13428"].(float64); !ok || int(val) != -100 {
		t.Errorf("expected logit_bias['13428'] to be -100, got %v", logitBias["13428"])
	}
	if val, ok := logitBias["13784"].(float64); !ok || int(val) != -100 {
		t.Errorf("expected logit_bias['13784'] to be -100, got %v", logitBias["13784"])
	}
}

func TestPayloadInjection_LogitBias_Stream(t *testing.T) {
	var capturedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		bodyBytes, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bodyBytes, &capturedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL: server.URL,
		LogitBias: map[string]int{
			"85152": -100,
		},
	})

	req := ChatCompletionRequest{
		Model: "test-model",
		Messages: []ChatMessage{
			{Role: "user", Content: TextContent("hello")},
		},
	}

	outCh, errCh := client.ChatCompletionStream(context.Background(), req)
	for range outCh {
	}
	if err := <-errCh; err != nil {
		t.Fatalf("unexpected stream error: %v", err)
	}

	logitBiasRaw, ok := capturedBody["logit_bias"]
	if !ok {
		t.Fatalf("expected logit_bias field in stream request body, got %v", capturedBody)
	}

	logitBias, ok := logitBiasRaw.(map[string]any)
	if !ok {
		t.Fatalf("expected logit_bias to be map[string]any, got %T", logitBiasRaw)
	}

	if val, ok := logitBias["85152"].(float64); !ok || int(val) != -100 {
		t.Errorf("expected logit_bias['85152'] to be -100, got %v", logitBias["85152"])
	}
}
