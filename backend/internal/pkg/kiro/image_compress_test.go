package kiro

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// createTestPNG creates a PNG image of given dimensions and returns base64-encoded data.
// Uses pseudo-random noise pattern to defeat PNG compression (simulates real screenshots).
func createTestPNG(w, h int) string {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// Use noise-like pattern that PNG can't compress well (simulates screenshots with text/UI)
	seed := uint32(42)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// Simple LCG pseudo-random to generate varied pixel values
			seed = seed*1664525 + 1013904223
			r := uint8(seed >> 24)
			seed = seed*1664525 + 1013904223
			g := uint8(seed >> 24)
			seed = seed*1664525 + 1013904223
			b := uint8(seed >> 24)
			img.Set(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestEstimateImageDataTokens(t *testing.T) {
	tests := []struct {
		name      string
		dataLen   int
		wantMin   int
		wantMax   int
	}{
		{"small image uses minimum", 1000, ImageTokenEstimate, ImageTokenEstimate},
		{"200KB image", 200 * 1024, 200 * 1024 / CharsPerToken, 200*1024/CharsPerToken + 1},
		{"500KB image", 500 * 1024, 500 * 1024 / CharsPerToken, 500*1024/CharsPerToken + 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := estimateImageDataTokens(tt.dataLen)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("estimateImageDataTokens(%d) = %d, want [%d, %d]",
					tt.dataLen, got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestCompressImagesInRequest_NoImages(t *testing.T) {
	req := &ClaudeRequest{
		Messages: []ClaudeMessage{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "hi"},
		},
	}

	modified := CompressImagesInRequest(req)
	if modified {
		t.Error("expected no modification for text-only messages")
	}
}

func TestCompressImagesInRequest_SmallImage(t *testing.T) {
	// Small image (< 200KB) should not be compressed
	smallData := createTestPNG(50, 50) // tiny image
	req := &ClaudeRequest{
		Messages: []ClaudeMessage{
			{Role: "user", Content: []any{
				map[string]any{
					"type": "image",
					"source": map[string]any{
						"type":       "base64",
						"media_type": "image/png",
						"data":       smallData,
					},
				},
			}},
		},
	}

	modified := CompressImagesInRequest(req)
	if modified {
		t.Error("expected no modification for small image")
	}
}

func TestCompressImagesInRequest_LargeImage(t *testing.T) {
	// Create a large PNG image (will be > 200KB base64)
	largeData := createTestPNG(2000, 2000)
	if len(largeData) <= MaxSingleImageBase64Bytes {
		t.Skipf("test image too small: %d bytes, need > %d", len(largeData), MaxSingleImageBase64Bytes)
	}

	originalSize := len(largeData)
	t.Logf("Original image size: %d KB", originalSize/1024)

	req := &ClaudeRequest{
		Messages: []ClaudeMessage{
			{Role: "user", Content: []any{
				map[string]any{
					"type": "text",
					"text": "What's in this image?",
				},
				map[string]any{
					"type": "image",
					"source": map[string]any{
						"type":       "base64",
						"media_type": "image/png",
						"data":       largeData,
					},
				},
			}},
		},
	}

	modified := CompressImagesInRequest(req)
	if !modified {
		t.Fatal("expected modification for large image")
	}

	// Verify the image was compressed
	content := req.Messages[0].Content.([]any)
	imgBlock := content[1].(map[string]any)
	source := imgBlock["source"].(map[string]any)
	newData := source["data"].(string)
	newMediaType := source["media_type"].(string)

	t.Logf("Compressed image size: %d KB (was %d KB)", len(newData)/1024, originalSize/1024)
	t.Logf("New media type: %s", newMediaType)

	if len(newData) >= originalSize {
		t.Errorf("compressed image (%d) should be smaller than original (%d)", len(newData), originalSize)
	}

	if newMediaType != "image/jpeg" {
		t.Errorf("expected media_type image/jpeg, got %s", newMediaType)
	}

	// Text block should be unchanged
	textBlock := content[0].(map[string]any)
	if textBlock["text"] != "What's in this image?" {
		t.Error("text block was modified unexpectedly")
	}
}

func TestCompressImagesInRequest_ImageResizing(t *testing.T) {
	// Create an image that exceeds MaxImageLongSide
	largeData := createTestPNG(3000, 2000)
	t.Logf("Original image: 3000x2000, base64 size: %d KB", len(largeData)/1024)

	req := &ClaudeRequest{
		Messages: []ClaudeMessage{
			{Role: "user", Content: []any{
				map[string]any{
					"type": "image",
					"source": map[string]any{
						"type":       "base64",
						"media_type": "image/png",
						"data":       largeData,
					},
				},
			}},
		},
	}

	modified := CompressImagesInRequest(req)
	if !modified {
		t.Fatal("expected modification for oversized image")
	}

	// Verify it was resized + compressed
	content := req.Messages[0].Content.([]any)
	imgBlock := content[0].(map[string]any)
	source := imgBlock["source"].(map[string]any)
	newData := source["data"].(string)

	t.Logf("After resize+compress: %d KB", len(newData)/1024)

	if len(newData) >= len(largeData) {
		t.Errorf("resized image should be smaller")
	}
}

func TestNearestNeighborResize(t *testing.T) {
	// Create a 100x100 image
	src := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			src.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0, A: 255})
		}
	}

	dst := nearestNeighborResize(src, 50, 50)
	if dst.Bounds().Dx() != 50 || dst.Bounds().Dy() != 50 {
		t.Errorf("expected 50x50, got %dx%d", dst.Bounds().Dx(), dst.Bounds().Dy())
	}

	// Check a sample pixel — (25, 25) in dst corresponds to (50, 50) in src
	r, g, _, _ := dst.At(25, 25).RGBA()
	expectedR := uint8(50)
	expectedG := uint8(50)
	if uint8(r>>8) != expectedR || uint8(g>>8) != expectedG {
		t.Errorf("pixel mismatch at (25,25): got R=%d G=%d, want R=%d G=%d",
			uint8(r>>8), uint8(g>>8), expectedR, expectedG)
	}
}

func TestCompressImagesInToolResult(t *testing.T) {
	// Images can appear inside tool_result blocks
	largeData := createTestPNG(2000, 2000)
	if len(largeData) <= MaxSingleImageBase64Bytes {
		t.Skip("test image too small")
	}

	req := &ClaudeRequest{
		Messages: []ClaudeMessage{
			{Role: "user", Content: []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "test-id",
					"content": []any{
						map[string]any{
							"type": "image",
							"source": map[string]any{
								"type":       "base64",
								"media_type": "image/png",
								"data":       largeData,
							},
						},
					},
				},
			}},
		},
	}

	modified := CompressImagesInRequest(req)
	if !modified {
		t.Fatal("expected modification for image in tool_result")
	}

	content := req.Messages[0].Content.([]any)
	toolResult := content[0].(map[string]any)
	innerContent := toolResult["content"].([]any)
	imgBlock := innerContent[0].(map[string]any)
	source := imgBlock["source"].(map[string]any)
	newData := source["data"].(string)

	if len(newData) >= len(largeData) {
		t.Errorf("compressed image in tool_result should be smaller")
	}
}
