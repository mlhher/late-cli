package client

import (
	"encoding/json"
	"testing"
)

// TestContentPartVideoURLSerialization verifies that a video_url content part
// serializes to the OpenAI-compatible "video_url" schema so multimodal models
// (e.g. MiniMax-M3) can receive video input alongside existing text/image parts.
func TestContentPartVideoURLSerialization(t *testing.T) {
	part := ContentPart{
		Type: ContentPartVideoURL,
		VideoURL: &VideoURL{
			URL:    "https://example.com/clip.mp4",
			Detail: "high",
		},
	}
	raw, err := json.Marshal(part)
	if err != nil {
		t.Fatalf("marshal video part: %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal video part: %v", err)
	}

	if typ, _ := json.Marshal(got["type"]); string(typ) != `"video_url"` {
		t.Errorf("expected type \"video_url\", got %s", typ)
	}

	var vu VideoURL
	if err := json.Unmarshal(got["video_url"], &vu); err != nil {
		t.Fatalf("unmarshal video_url field: %v", err)
	}
	if vu.URL != "https://example.com/clip.mp4" {
		t.Errorf("expected video url, got %q", vu.URL)
	}
	if vu.Detail != "high" {
		t.Errorf("expected detail high, got %q", vu.Detail)
	}

	// image_url field must not leak into a video part.
	if _, ok := got["image_url"]; ok {
		t.Errorf("video part must not serialize an image_url field")
	}
}

// TestMessageContentVideoRoundTrip ensures a multimodal message containing text,
// image, and video parts marshals and unmarshals without losing the video part.
func TestMessageContentVideoRoundTrip(t *testing.T) {
	content := MessageContent{Parts: []ContentPart{
		{Type: ContentPartText, Text: "describe this clip"},
		{Type: ContentPartImageURL, ImageURL: &ImageURL{URL: "data:image/png;base64,AAA"}},
		{Type: ContentPartVideoURL, VideoURL: &VideoURL{URL: "data:video/mp4;base64,BBB"}},
	}}

	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}

	var back MessageContent
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	if len(back.Parts) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(back.Parts))
	}
	if back.Parts[2].Type != ContentPartVideoURL {
		t.Errorf("expected third part video_url, got %q", back.Parts[2].Type)
	}
	if back.Parts[2].VideoURL == nil || back.Parts[2].VideoURL.URL != "data:video/mp4;base64,BBB" {
		t.Errorf("video url not preserved on round-trip")
	}
}

// TestTextContentStillString verifies that pure-text content still serializes as
// a plain JSON string, preserving the existing text-only message handling.
func TestTextContentStillString(t *testing.T) {
	raw, err := json.Marshal(TextContent("hello"))
	if err != nil {
		t.Fatalf("marshal text: %v", err)
	}
	if string(raw) != `"hello"` {
		t.Errorf("expected plain string, got %s", raw)
	}
}
