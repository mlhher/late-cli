package client

import (
	"testing"
)

func TestSupportsVisionOverride(t *testing.T) {
	// Case 1: Config without EnableImages, and supportsVision is false
	c1 := NewClient(Config{
		BaseURL:      "http://localhost:8080",
		EnableImages: false,
	})
	if c1.SupportsVision() {
		t.Errorf("expected SupportsVision() to be false when EnableImages is false and backend support is unknown/false")
	}

	// Case 2: Config with EnableImages = true
	c2 := NewClient(Config{
		BaseURL:      "http://localhost:8080",
		EnableImages: true,
	})
	if !c2.SupportsVision() {
		t.Errorf("expected SupportsVision() to be true when EnableImages is true")
	}

	// Case 3: Config without EnableImages, but supportsVision is true
	c3 := NewClient(Config{
		BaseURL: "http://localhost:8080",
	})
	c3.supportsVision = true
	if !c3.SupportsVision() {
		t.Errorf("expected SupportsVision() to be true when c.supportsVision is true")
	}
}
