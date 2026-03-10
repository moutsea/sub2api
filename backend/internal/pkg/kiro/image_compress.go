// Package kiro provides image compression utilities for CodeWhisperer body size limits.
//
// CodeWhisperer/AWSQ has a hard body size limit of ~810KB. Base64-encoded images
// (especially PNG screenshots) can easily exceed this. This module compresses
// images in Claude requests before CW transformation to prevent 400 errors.
package kiro

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/gif" // register GIF decoder
	_ "image/png" // register PNG decoder
	"log"
)

// Image compression constants
const (
	// MaxSingleImageBase64Bytes is the threshold above which a single image gets compressed.
	// 200KB base64 ≈ 150KB raw image data.
	MaxSingleImageBase64Bytes = 200 * 1024

	// MaxImageLongSide is the maximum pixel dimension on the longer side.
	// Matches Anthropic's recommendation for optimal token usage.
	MaxImageLongSide = 1568

	// jpegDefaultQuality is the starting JPEG quality for compression.
	jpegDefaultQuality = 80
	// jpegMinQuality is the minimum JPEG quality before giving up further compression.
	jpegMinQuality = 40
	// jpegQualityStep is the quality reduction step during iterative compression.
	jpegQualityStep = 15
)

// CompressImagesInRequest walks through all messages in a Claude request and
// compresses base64-encoded images that exceed MaxSingleImageBase64Bytes.
// Images are resized if they exceed MaxImageLongSide and re-encoded as JPEG.
//
// This should be called BEFORE TransformClaudeToCodeWhisperer to ensure:
// 1. Token estimation reflects actual compressed sizes
// 2. Serialized CW body fits within the ~800KB limit
//
// Returns true if any image was modified.
func CompressImagesInRequest(req *ClaudeRequest) bool {
	if req == nil || len(req.Messages) == 0 {
		return false
	}

	modified := false
	for i := range req.Messages {
		if compressImagesInMessage(&req.Messages[i]) {
			modified = true
		}
	}
	return modified
}

// compressImagesInMessage compresses images in a single Claude message.
func compressImagesInMessage(msg *ClaudeMessage) bool {
	if msg == nil {
		return false
	}

	// Content can be string, []any (from JSON unmarshal), or []ContentBlock
	switch content := msg.Content.(type) {
	case []any:
		modified := false
		for _, block := range content {
			blockMap, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if compressImageBlock(blockMap) {
				modified = true
			}
		}
		return modified

	default:
		// string content or other types don't contain images
		return false
	}
}

// compressImageBlock compresses a single image content block (map[string]any).
// Returns true if the block was modified.
func compressImageBlock(blockMap map[string]any) bool {
	blockType, _ := blockMap["type"].(string)
	if blockType != "image" {
		// Also check tool_result blocks which may contain nested image blocks
		if blockType == "tool_result" {
			return compressImagesInToolResult(blockMap)
		}
		return false
	}

	source, ok := blockMap["source"].(map[string]any)
	if !ok {
		return false
	}

	sourceType, _ := source["type"].(string)
	if sourceType != "base64" {
		return false
	}

	data, _ := source["data"].(string)
	if data == "" || len(data) <= MaxSingleImageBase64Bytes {
		return false
	}

	mediaType, _ := source["media_type"].(string)

	// Warn about GIF animation loss (only first frame is preserved after JPEG re-encoding)
	if mediaType == "image/gif" {
		log.Printf("[kiro] warning: compressing GIF image will lose animation (only first frame kept)")
	}

	// Compress the image
	compressedData, newMediaType, err := compressBase64Image(data, mediaType)
	if err != nil {
		log.Printf("[kiro] image compression failed (media_type=%s, size=%d): %v", mediaType, len(data), err)
		return false
	}

	if len(compressedData) >= len(data) {
		// Compression didn't help (rare for PNG→JPEG)
		return false
	}

	log.Printf("[kiro] image compressed: %s %dKB -> %s %dKB (%.0f%% reduction)",
		mediaType, len(data)/1024, newMediaType, len(compressedData)/1024,
		float64(len(data)-len(compressedData))/float64(len(data))*100)

	source["data"] = compressedData
	source["media_type"] = newMediaType
	return true
}

// compressImagesInToolResult compresses images nested inside tool_result content.
func compressImagesInToolResult(blockMap map[string]any) bool {
	content, ok := blockMap["content"].([]any)
	if !ok {
		return false
	}

	modified := false
	for _, item := range content {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if compressImageBlock(itemMap) {
			modified = true
		}
	}
	return modified
}

// compressBase64Image decodes a base64 image, optionally resizes it, and re-encodes
// as JPEG with progressive quality reduction until it fits within the size limit.
// Strategy: first try quality reduction, then iteratively shrink dimensions.
func compressBase64Image(base64Data, mediaType string) (string, string, error) {
	// Decode base64
	rawData, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		// Try URL-safe base64
		rawData, err = base64.URLEncoding.DecodeString(base64Data)
		if err != nil {
			return "", "", fmt.Errorf("base64 decode: %w", err)
		}
	}

	// Pre-check dimensions to prevent DoS (crafted image with huge declared dimensions)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(rawData))
	if err != nil {
		return "", "", fmt.Errorf("image config decode (format=%s): %w", mediaType, err)
	}
	const maxPixels = 100_000_000 // 100 megapixels
	if int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return "", "", fmt.Errorf("image too large: %dx%d (%d megapixels, max %d)",
			cfg.Width, cfg.Height, int64(cfg.Width)*int64(cfg.Height)/1_000_000, maxPixels/1_000_000)
	}

	// Decode image
	img, _, err := image.Decode(bytes.NewReader(rawData))
	if err != nil {
		return "", "", fmt.Errorf("image decode (format=%s): %w", mediaType, err)
	}

	// Phase 1: Resize to MaxImageLongSide if needed
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	img = resizeIfNeeded(img, w, h)

	// Phase 2: Try JPEG encoding with decreasing quality
	for quality := jpegDefaultQuality; quality >= jpegMinQuality; quality -= jpegQualityStep {
		encoded, err := encodeJPEGBase64(img, quality)
		if err != nil {
			return "", "", err
		}
		if len(encoded) <= MaxSingleImageBase64Bytes {
			return encoded, "image/jpeg", nil
		}
	}

	// Phase 3: Quality alone wasn't enough. Progressively shrink dimensions
	// from the phase-1 image to avoid cumulative quality loss.
	// Scale factors: 75%, 60%, 45%, 30% of the phase-1 dimensions.
	origBounds := img.Bounds()
	origW, origH := origBounds.Dx(), origBounds.Dy()
	var currentImg image.Image
	for scale := 0.75; scale >= 0.25; scale -= 0.15 {
		newW := int(float64(origW) * scale)
		newH := int(float64(origH) * scale)
		if newW < 100 || newH < 100 {
			break
		}

		log.Printf("[kiro] aggressive resize: %dx%d -> %dx%d (scale=%.0f%%)",
			origW, origH, newW, newH, scale*100)
		currentImg = nearestNeighborResize(img, newW, newH)

		encoded, err := encodeJPEGBase64(currentImg, jpegMinQuality)
		if err != nil {
			return "", "", err
		}
		if len(encoded) <= MaxSingleImageBase64Bytes {
			return encoded, "image/jpeg", nil
		}
	}

	// Phase 4: Last resort — return the smallest version we can produce
	// even if it exceeds the single image limit. The body-size truncation
	// will handle it downstream.
	lastImg := currentImg
	if lastImg == nil {
		lastImg = img // Phase 3 loop didn't execute
	}
	encoded, err := encodeJPEGBase64(lastImg, jpegMinQuality)
	if err != nil {
		return "", "", err
	}

	// Only return if it's actually smaller than the original
	if len(encoded) < len(base64Data) {
		return encoded, "image/jpeg", nil
	}

	return "", "", fmt.Errorf("compression ineffective: %d -> %d bytes", len(base64Data), len(encoded))
}

// encodeJPEGBase64 encodes an image as JPEG and returns the base64 string.
func encodeJPEGBase64(img image.Image, quality int) (string, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return "", fmt.Errorf("jpeg encode (quality=%d): %w", quality, err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// resizeIfNeeded downscales an image if its longest side exceeds MaxImageLongSide.
// Uses nearest-neighbor sampling (stdlib only, no external dependencies).
// This is acceptable quality for screenshots which have sharp edges and solid colors.
func resizeIfNeeded(img image.Image, w, h int) image.Image {
	longSide := w
	if h > longSide {
		longSide = h
	}

	if longSide <= MaxImageLongSide {
		return img
	}

	// Calculate new dimensions preserving aspect ratio
	scale := float64(MaxImageLongSide) / float64(longSide)
	newW := int(float64(w) * scale)
	newH := int(float64(h) * scale)

	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}

	log.Printf("[kiro] resizing image: %dx%d -> %dx%d", w, h, newW, newH)
	return nearestNeighborResize(img, newW, newH)
}

// nearestNeighborResize performs a fast nearest-neighbor downscale using stdlib only.
// Quality is sufficient for screenshots (sharp edges, solid colors, text).
func nearestNeighborResize(src image.Image, newW, newH int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	srcBounds := src.Bounds()
	srcMinX, srcMinY := srcBounds.Min.X, srcBounds.Min.Y
	srcW := srcBounds.Dx()
	srcH := srcBounds.Dy()

	for y := 0; y < newH; y++ {
		srcY := srcMinY + int(int64(y)*int64(srcH)/int64(newH))
		for x := 0; x < newW; x++ {
			srcX := srcMinX + int(int64(x)*int64(srcW)/int64(newW))
			r, g, b, a := src.At(srcX, srcY).RGBA()
			dst.SetRGBA(x, y, color.RGBA{
				R: uint8(r >> 8),
				G: uint8(g >> 8),
				B: uint8(b >> 8),
				A: uint8(a >> 8),
			})
		}
	}

	return dst
}
