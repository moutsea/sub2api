// Package kiro provides URL image resolution for CodeWhisperer compatibility.
//
// CodeWhisperer only supports base64-encoded images, not URL references.
// This module resolves URL images (HTTP/HTTPS) by downloading them and
// converting to base64 format before CW transformation.
package kiro

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// Image URL resolution constants
const (
	// maxImageDownloadSize is the maximum allowed image download size (10MB).
	maxImageDownloadSize = 10 * 1024 * 1024
	// imageDownloadTimeout is the HTTP timeout for downloading images.
	imageDownloadTimeout = 15 * time.Second
)

// imageHTTPClient is a shared HTTP client with SSRF protection and conservative timeouts.
var imageHTTPClient = &http.Client{
	Timeout: imageDownloadTimeout,
	Transport: &http.Transport{
		// SSRF protection: reject connections to private/loopback/link-local IPs
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("invalid address: %w", err)
			}

			// Resolve hostname to IPs
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("DNS lookup failed: %w", err)
			}

			for _, ip := range ips {
				if isPrivateIP(ip.IP) {
					return nil, fmt.Errorf("blocked: target resolves to private IP %s", ip.IP)
				}
			}

			// Connect to the first allowed IP
			dialer := &net.Dialer{Timeout: 10 * time.Second}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
	},
	// Don't follow too many redirects
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	},
}

// ResolveURLImagesInRequest walks through all messages in a Claude request and
// resolves URL-based images (source.type == "url") to base64 format.
// This is necessary because CodeWhisperer only supports base64-encoded images.
//
// For data URLs (data:image/...;base64,...), the data is extracted directly.
// For HTTP/HTTPS URLs, the image is downloaded and converted to base64.
//
// Should be called BEFORE CompressImagesInRequest and TransformClaudeToCodeWhisperer.
// Returns true if any image was resolved.
func ResolveURLImagesInRequest(req *ClaudeRequest) bool {
	if req == nil || len(req.Messages) == 0 {
		return false
	}

	modified := false
	for i := range req.Messages {
		if resolveURLImagesInMessage(&req.Messages[i]) {
			modified = true
		}
	}
	return modified
}

// resolveURLImagesInMessage resolves URL images in a single message.
func resolveURLImagesInMessage(msg *ClaudeMessage) bool {
	if msg == nil {
		return false
	}

	switch content := msg.Content.(type) {
	case []any:
		modified := false
		for _, block := range content {
			blockMap, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if resolveURLImageBlock(blockMap) {
				modified = true
			}
		}
		return modified
	default:
		return false
	}
}

// resolveURLImageBlock resolves a single URL image block to base64.
func resolveURLImageBlock(blockMap map[string]any) bool {
	blockType, _ := blockMap["type"].(string)
	if blockType != "image" {
		// Check tool_result blocks for nested images
		if blockType == "tool_result" {
			return resolveURLImagesInToolResult(blockMap)
		}
		return false
	}

	source, ok := blockMap["source"].(map[string]any)
	if !ok {
		return false
	}

	sourceType, _ := source["type"].(string)
	if sourceType != "url" {
		return false // already base64
	}

	url, _ := source["url"].(string)
	if url == "" {
		return false
	}

	// Try data URL first
	if mediaType, data, ok := parseDataURL(url); ok {
		source["type"] = "base64"
		source["media_type"] = mediaType
		source["data"] = data
		delete(source, "url")
		log.Printf("[kiro] resolved data URL image: media_type=%s, data_size=%dKB", mediaType, len(data)/1024)
		return true
	}

	// HTTP/HTTPS URL — download
	mediaType, data, err := downloadImageAsBase64(url)
	if err != nil {
		log.Printf("[kiro] failed to download image URL %s: %v", truncateURL(url), err)
		// Convert to text placeholder so the model knows an image was intended
		blockMap["type"] = "text"
		blockMap["text"] = fmt.Sprintf("[Image from URL: %s — download failed: %v]", truncateURL(url), err)
		delete(blockMap, "source")
		return true
	}

	source["type"] = "base64"
	source["media_type"] = mediaType
	source["data"] = data
	delete(source, "url")
	log.Printf("[kiro] downloaded URL image: %s -> %s, %dKB", truncateURL(url), mediaType, len(data)/1024)
	return true
}

// resolveURLImagesInToolResult resolves URL images nested in tool_result content.
func resolveURLImagesInToolResult(blockMap map[string]any) bool {
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
		if resolveURLImageBlock(itemMap) {
			modified = true
		}
	}
	return modified
}

// downloadImageAsBase64 downloads an image from a URL and returns base64-encoded data.
func downloadImageAsBase64(url string) (mediaType, base64Data string, err error) {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "", "", fmt.Errorf("unsupported URL scheme")
	}

	resp, err := imageHTTPClient.Get(url)
	if err != nil {
		return "", "", fmt.Errorf("HTTP request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Read with size limit
	limitReader := io.LimitReader(resp.Body, maxImageDownloadSize+1)
	data, err := io.ReadAll(limitReader)
	if err != nil {
		return "", "", fmt.Errorf("read body: %w", err)
	}
	if len(data) > maxImageDownloadSize {
		return "", "", fmt.Errorf("image too large (>%dMB)", maxImageDownloadSize/1024/1024)
	}

	// Determine media type from Content-Type header or URL extension
	mediaType = detectMediaType(resp.Header.Get("Content-Type"), url)
	if mediaType == "" {
		return "", "", fmt.Errorf("unable to determine image type")
	}

	base64Data = base64.StdEncoding.EncodeToString(data)
	return mediaType, base64Data, nil
}

// detectMediaType determines the image media type from Content-Type header or URL.
func detectMediaType(contentType, url string) string {
	// Try Content-Type header first
	if contentType != "" {
		// Strip parameters (e.g., "image/png; charset=utf-8" → "image/png")
		mt := strings.Split(contentType, ";")[0]
		mt = strings.TrimSpace(mt)
		if strings.HasPrefix(mt, "image/") {
			return mt
		}
	}

	// Fall back to URL extension
	urlLower := strings.ToLower(url)
	// Strip query string
	if idx := strings.Index(urlLower, "?"); idx != -1 {
		urlLower = urlLower[:idx]
	}

	switch {
	case strings.HasSuffix(urlLower, ".png"):
		return "image/png"
	case strings.HasSuffix(urlLower, ".jpg"), strings.HasSuffix(urlLower, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(urlLower, ".gif"):
		return "image/gif"
	case strings.HasSuffix(urlLower, ".webp"):
		return "image/webp"
	case strings.HasSuffix(urlLower, ".svg"):
		return "image/svg+xml"
	default:
		// Unknown extension — return empty to let caller handle as error
		return ""
	}
}

// isPrivateIP checks if an IP address is private, loopback, link-local, or otherwise
// not suitable for external image downloads (SSRF protection).
func isPrivateIP(ip net.IP) bool {
	// Loopback (127.0.0.0/8, ::1)
	if ip.IsLoopback() {
		return true
	}
	// Link-local (169.254.0.0/16, fe80::/10)
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	// Private ranges (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, fc00::/7)
	if ip.IsPrivate() {
		return true
	}
	// Unspecified (0.0.0.0, ::)
	if ip.IsUnspecified() {
		return true
	}
	return false
}

// truncateURL truncates a URL for logging, stripping query params to avoid leaking secrets.
func truncateURL(url string) string {
	// Strip query parameters first
	if idx := strings.Index(url, "?"); idx != -1 {
		url = url[:idx] + "?..."
	}
	if len(url) <= 100 {
		return url
	}
	return url[:97] + "..."
}
