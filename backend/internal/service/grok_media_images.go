package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const grokImagesDefaultModel = "grok-imagine-image-quality"

func (s *OpenAIGatewayService) forwardGrokImages(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	parsed *OpenAIImagesRequest,
) (*OpenAIForwardResult, error) {
	if account == nil || !account.IsGrok() {
		return nil, fmt.Errorf("grok account is required")
	}
	startTime := time.Now()
	requestModel := strings.TrimSpace(parsed.Model)
	if requestModel == "" || strings.HasPrefix(strings.ToLower(requestModel), "gpt-image-") {
		requestModel = grokImagesDefaultModel
	}
	if !xai.IsGrokImagineModel(requestModel) {
		return nil, fmt.Errorf("unsupported Grok image model: %s", requestModel)
	}
	upstreamModel := resolveGrokRequestModel(account, requestModel)
	if !xai.IsGrokImagineModel(upstreamModel) {
		return nil, fmt.Errorf("unsupported Grok image model: %s", upstreamModel)
	}

	forwardBody, contentType, err := prepareGrokImagesBody(body, parsed, upstreamModel)
	if err != nil {
		return nil, err
	}
	if c != nil {
		c.Set(OpsUpstreamRequestBodyKey, string(forwardBody))
	}

	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}
	baseURL := account.GetGrokMediaBaseURL()
	if s.cfg != nil {
		baseURL, err = s.validateUpstreamBaseURL(baseURL)
		if err != nil {
			return nil, err
		}
	}
	targetURL, err := buildGrokImagesURL(baseURL, parsed.Endpoint)
	if err != nil {
		return nil, err
	}
	requestCtx, release := detachUpstreamContext(ctx)
	defer release()
	upstreamReq, err := http.NewRequestWithContext(requestCtx, http.MethodPost, targetURL, bytes.NewReader(forwardBody))
	if err != nil {
		return nil, err
	}
	upstreamReq.Header.Set("Authorization", "Bearer "+token)
	upstreamReq.Header.Set("Accept", "application/json, text/event-stream")
	upstreamReq.Header.Set("Content-Type", contentType)
	if account.IsGrokOAuth() && isGrokCLIProxyTarget(targetURL) {
		xai.ApplyCLIProxyHeaders(upstreamReq)
	}
	if c != nil && c.Request != nil {
		for key, values := range c.Request.Header {
			lowerKey := strings.ToLower(key)
			if lowerKey == "session_id" || lowerKey == "conversation_id" || !openaiAllowedHeaders[lowerKey] || strings.EqualFold(key, "content-type") {
				continue
			}
			for _, value := range values {
				upstreamReq.Header.Add(key, value)
			}
		}
	}
	account.ApplyHeaderOverrides(upstreamReq.Header)

	s.applyGrokRequestJitter(ctx)
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	heartbeat := (*openAIImagesJSONHeartbeat)(nil)
	if !parsed.Stream {
		heartbeat = startOpenAIImagesJSONHeartbeat(c)
	}
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		if heartbeat != nil {
			heartbeat.stop()
		}
		return nil, fmt.Errorf("Grok image upstream request failed: %w", err)
	}
	if resp == nil || resp.Body == nil {
		if heartbeat != nil {
			heartbeat.stop()
		}
		return nil, fmt.Errorf("Grok image upstream returned an empty response")
	}
	defer func() { _ = resp.Body.Close() }()
	s.updateGrokUsageSnapshot(ctx, account.ID, xai.ParseQuotaHeaders(resp.Header, resp.StatusCode))

	if resp.StatusCode >= 400 {
		if heartbeat != nil {
			heartbeat.stop()
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		message := sanitizeUpstreamErrorMessage(extractUpstreamErrorMessage(respBody))
		if s.shouldFailoverGrokUpstreamError(resp.StatusCode, respBody) {
			if s.rateLimitService != nil {
				s.rateLimitService.HandleUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)
			}
			return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode, Message: message}
		}
		return s.handleErrorResponse(ctx, resp, c, account)
	}

	var usage OpenAIUsage
	imageCount := parsed.N
	var firstTokenMs *int
	if parsed.Stream {
		streamUsage, count, ttft, err := s.handleOpenAIImagesStreamingResponse(resp, c, startTime)
		if err != nil {
			return nil, err
		}
		usage = streamUsage
		if count > 0 {
			imageCount = count
		}
		firstTokenMs = ttft
	} else {
		nonStreamUsage, count, err := s.handleOpenAIImagesNonStreamingResponse(resp, c, parsed.Upscale, heartbeat)
		if err != nil {
			return nil, err
		}
		usage = nonStreamUsage
		if count > 0 {
			imageCount = count
		}
	}

	return &OpenAIForwardResult{
		RequestID:    firstNonEmptyGrokHeader(resp.Header, "x-request-id", "xai-request-id"),
		Usage:        usage,
		Model:        upstreamModel,
		Stream:       parsed.Stream,
		Duration:     time.Since(startTime),
		FirstTokenMs: firstTokenMs,
		ImageCount:   imageCount,
		ImageSize:    parsed.SizeTier,
	}, nil
}

func prepareGrokImagesBody(body []byte, parsed *OpenAIImagesRequest, model string) ([]byte, string, error) {
	if parsed == nil {
		return nil, "", fmt.Errorf("parsed images request is required")
	}
	if isUnsupportedGrokImage4KSize(parsed.Size) {
		return nil, "", fmt.Errorf("Grok image size %q is not supported; use 2k or a supported aspect ratio", parsed.Size)
	}
	if parsed.Multipart {
		payload := map[string]any{"model": model, "prompt": parsed.Prompt, "n": parsed.N}
		if parsed.Size != "" {
			applyGrokImageSize(payload, parsed.Size)
		}
		images := make([]any, 0, len(parsed.Uploads))
		for _, upload := range parsed.Uploads {
			if len(upload.Data) == 0 {
				continue
			}
			contentType := strings.TrimSpace(upload.ContentType)
			if contentType == "" {
				contentType = "image/png"
			}
			dataURL := "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(upload.Data)
			images = append(images, map[string]any{"url": dataURL, "type": "image_url"})
		}
		if len(images) > 0 {
			payload["image"] = images[0]
			if len(images) > 1 {
				payload["images"] = images
			}
		}
		if parsed.MaskUpload != nil && len(parsed.MaskUpload.Data) > 0 {
			contentType := strings.TrimSpace(parsed.MaskUpload.ContentType)
			if contentType == "" {
				contentType = "image/png"
			}
			payload["mask"] = map[string]any{
				"url":  "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(parsed.MaskUpload.Data),
				"type": "image_url",
			}
		} else if maskURL := strings.TrimSpace(parsed.MaskImageURL); maskURL != "" {
			payload["mask"] = map[string]any{"url": maskURL, "type": "image_url"}
		}
		encoded, err := json.Marshal(payload)
		return encoded, "application/json", err
	}

	if !gjson.ValidBytes(body) {
		return nil, "", fmt.Errorf("invalid Grok image request body")
	}
	out, err := sjson.SetBytes(body, "model", model)
	if err != nil {
		return nil, "", err
	}
	if parsed.Size != "" {
		var payload map[string]any
		if err := json.Unmarshal(out, &payload); err == nil {
			applyGrokImageSize(payload, parsed.Size)
			out, err = json.Marshal(payload)
			if err != nil {
				return nil, "", err
			}
		}
	}
	for _, field := range []string{"image", "images", "mask", "reference_images"} {
		out, err = normalizeGrokImageField(out, field)
		if err != nil {
			return nil, "", err
		}
	}
	return out, "application/json", nil
}

func applyGrokImageSize(payload map[string]any, size string) {
	size = strings.ToLower(strings.TrimSpace(size))
	resolution := ""
	aspect := ""
	switch size {
	case "1k", "1024x1024":
		resolution, aspect = "1k", "1:1"
	case "2k", "1536x1024":
		resolution, aspect = "2k", "3:2"
	case "1024x1536":
		resolution, aspect = "2k", "2:3"
	case "1792x1024":
		resolution, aspect = "2k", "16:9"
	case "1024x1792":
		resolution, aspect = "2k", "9:16"
	}
	if resolution != "" {
		payload["resolution"] = resolution
	}
	if aspect != "" {
		payload["aspect_ratio"] = aspect
	}
	delete(payload, "size")
}

func isUnsupportedGrokImage4KSize(size string) bool {
	switch strings.ToLower(strings.TrimSpace(size)) {
	case "4k", "2048x2048":
		return true
	default:
		return false
	}
}

func normalizeGrokImageField(body []byte, field string) ([]byte, error) {
	value := gjson.GetBytes(body, field)
	if !value.Exists() {
		return body, nil
	}
	convert := func(item gjson.Result) map[string]any {
		if item.Type == gjson.String {
			return map[string]any{"url": item.String(), "type": "image_url"}
		}
		if nested := item.Get("image_url"); nested.Exists() {
			if nested.Type == gjson.String {
				return map[string]any{"url": nested.String(), "type": "image_url"}
			}
			if u := strings.TrimSpace(nested.Get("url").String()); u != "" {
				return map[string]any{"url": u, "type": "image_url"}
			}
		}
		if u := strings.TrimSpace(item.Get("url").String()); u != "" {
			return map[string]any{"url": u, "type": "image_url"}
		}
		return nil
	}
	if value.IsArray() {
		converted := make([]map[string]any, 0, len(value.Array()))
		for _, item := range value.Array() {
			if image := convert(item); image != nil {
				converted = append(converted, image)
			}
		}
		if len(converted) == 0 {
			return body, nil
		}
		return sjson.SetBytes(body, field, converted)
	}
	if image := convert(value); image != nil {
		return sjson.SetBytes(body, field, image)
	}
	return body, nil
}

func buildGrokImagesURL(baseURL, endpoint string) (string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid Grok upstream URL")
	}
	pathName := strings.TrimSpace(endpoint)
	if pathName == "" {
		pathName = openAIImagesGenerationsEndpoint
	}
	if !strings.HasPrefix(pathName, "/") {
		pathName = "/" + pathName
	}
	if strings.HasSuffix(parsed.Path, pathName) {
		return parsed.String(), nil
	}
	if strings.HasSuffix(parsed.Path, "/v1") && strings.HasPrefix(pathName, "/v1/") {
		pathName = strings.TrimPrefix(pathName, "/v1")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + pathName
	return parsed.String(), nil
}

func isGrokCLIProxyTarget(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	return err == nil && strings.EqualFold(parsed.Hostname(), "cli-chat-proxy.grok.com")
}

func firstNonEmptyGrokHeader(headers http.Header, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(headers.Get(key)); value != "" {
			return value
		}
	}
	return ""
}
