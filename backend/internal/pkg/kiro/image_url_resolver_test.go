package kiro

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseDataURL(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantMedia string
		wantOK    bool
		wantData  bool
	}{
		{"valid png", "data:image/png;base64,iVBORw0KGgo=", "image/png", true, true},
		{"valid jpeg", "data:image/jpeg;base64,/9j/4AAQ", "image/jpeg", true, true},
		{"no media type", "data:;base64,iVBOR", "application/octet-stream", true, true},
		{"not data url", "https://example.com/img.png", "", false, false},
		{"no base64", "data:image/png,rawdata", "", false, false},
		{"empty data", "data:image/png;base64,", "", false, false},
		{"no comma", "data:image/png;base64", "", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mediaType, data, ok := parseDataURL(tt.url)
			if ok != tt.wantOK {
				t.Errorf("parseDataURL(%q) ok=%v, want %v", tt.url, ok, tt.wantOK)
			}
			if ok {
				if mediaType != tt.wantMedia {
					t.Errorf("mediaType=%q, want %q", mediaType, tt.wantMedia)
				}
				if tt.wantData && data == "" {
					t.Error("expected non-empty data")
				}
			}
		})
	}
}

func TestResolveURLImagesInRequest_DataURL(t *testing.T) {
	// Simulate an OpenAI-style data URL that wasn't caught by the adapter
	// (e.g., from Claude native client using URL source with data URL)
	dataURL := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUg=="

	req := &ClaudeRequest{
		Messages: []ClaudeMessage{
			{Role: "user", Content: []any{
				map[string]any{
					"type": "image",
					"source": map[string]any{
						"type": "url",
						"url":  dataURL,
					},
				},
			}},
		},
	}

	modified := ResolveURLImagesInRequest(req)
	if !modified {
		t.Fatal("expected modification for data URL image")
	}

	content := req.Messages[0].Content.([]any)
	imgBlock := content[0].(map[string]any)
	source := imgBlock["source"].(map[string]any)

	if source["type"] != "base64" {
		t.Errorf("source type=%v, want base64", source["type"])
	}
	if source["media_type"] != "image/png" {
		t.Errorf("media_type=%v, want image/png", source["media_type"])
	}
	if source["data"] == "" {
		t.Error("expected non-empty data")
	}
	if _, exists := source["url"]; exists {
		t.Error("url field should have been removed")
	}
}

func TestResolveURLImagesInRequest_HTTPURL_SSRF_Blocked(t *testing.T) {
	// httptest.NewServer listens on 127.0.0.1 which should be blocked by SSRF protection.
	// This test verifies the SSRF filter works correctly.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0})
	}))
	defer ts.Close()

	req := &ClaudeRequest{
		Messages: []ClaudeMessage{
			{Role: "user", Content: []any{
				map[string]any{
					"type": "image",
					"source": map[string]any{
						"type": "url",
						"url":  ts.URL + "/test.jpg",
					},
				},
			}},
		},
	}

	modified := ResolveURLImagesInRequest(req)
	if !modified {
		t.Fatal("expected modification (should convert to text placeholder due to SSRF block)")
	}

	// Should be converted to text placeholder because localhost is blocked
	content := req.Messages[0].Content.([]any)
	block := content[0].(map[string]any)
	if block["type"] != "text" {
		t.Errorf("block type=%v, want text (placeholder due to SSRF block)", block["type"])
	}
	text, _ := block["text"].(string)
	if text == "" {
		t.Error("expected non-empty placeholder text")
	}
	t.Logf("SSRF blocked placeholder: %s", text)
}

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		ip      string
		private bool
	}{
		{"127.0.0.1", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true}, // AWS IMDS
		{"::1", true},
		{"0.0.0.0", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"203.0.113.1", false},
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("failed to parse IP: %s", tt.ip)
			}
			got := isPrivateIP(ip)
			if got != tt.private {
				t.Errorf("isPrivateIP(%s) = %v, want %v", tt.ip, got, tt.private)
			}
		})
	}
}

func TestResolveURLImagesInRequest_FailedDownload(t *testing.T) {
	// Test with a URL that fails — use an unreachable host with short timeout
	// (localhost is blocked by SSRF protection, so we use a non-routable IP)
	req := &ClaudeRequest{
		Messages: []ClaudeMessage{
			{Role: "user", Content: []any{
				map[string]any{
					"type": "image",
					"source": map[string]any{
						"type": "url",
						"url":  "https://invalid.test.example.com/broken.jpg",
					},
				},
			}},
		},
	}

	modified := ResolveURLImagesInRequest(req)
	if !modified {
		t.Fatal("expected modification (should convert to text placeholder)")
	}

	// Should be converted to text placeholder
	content := req.Messages[0].Content.([]any)
	block := content[0].(map[string]any)
	if block["type"] != "text" {
		t.Errorf("block type=%v, want text (placeholder)", block["type"])
	}
	text, _ := block["text"].(string)
	if text == "" {
		t.Error("expected non-empty placeholder text")
	}
	t.Logf("Placeholder text: %s", text)
}

func TestResolveURLImagesInRequest_Base64Untouched(t *testing.T) {
	// base64 images should not be modified
	req := &ClaudeRequest{
		Messages: []ClaudeMessage{
			{Role: "user", Content: []any{
				map[string]any{
					"type": "image",
					"source": map[string]any{
						"type":       "base64",
						"media_type": "image/png",
						"data":       "iVBORw0KGgo=",
					},
				},
			}},
		},
	}

	modified := ResolveURLImagesInRequest(req)
	if modified {
		t.Error("base64 images should not be modified")
	}
}

func TestDetectMediaType(t *testing.T) {
	tests := []struct {
		contentType string
		url         string
		want        string
	}{
		{"image/png", "", "image/png"},
		{"image/jpeg; charset=utf-8", "", "image/jpeg"},
		{"", "https://example.com/img.png", "image/png"},
		{"", "https://example.com/photo.jpg?size=large", "image/jpeg"},
		{"", "https://example.com/anim.gif", "image/gif"},
		{"", "https://example.com/pic.webp", "image/webp"},
		{"text/html", "https://example.com/img.png", "image/png"},
		{"", "https://example.com/unknown", ""}, // unknown extension returns empty
	}

	for _, tt := range tests {
		name := fmt.Sprintf("ct=%q url=%q", tt.contentType, tt.url)
		t.Run(name, func(t *testing.T) {
			got := detectMediaType(tt.contentType, tt.url)
			if got != tt.want {
				t.Errorf("detectMediaType(%q, %q) = %q, want %q", tt.contentType, tt.url, got, tt.want)
			}
		})
	}
}
