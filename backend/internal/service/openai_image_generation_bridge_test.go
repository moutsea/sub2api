package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type codexImageBridgeUpstream struct {
	statusCode  int
	body        string
	contentType string
	lastReq     *http.Request
	lastBody    []byte
}

func (u *codexImageBridgeUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	u.lastReq = req
	body, _ := io.ReadAll(req.Body)
	u.lastBody = body

	statusCode := u.statusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	contentType := strings.TrimSpace(u.contentType)
	if contentType == "" {
		contentType = "application/json"
	}
	return &http.Response{
		StatusCode: statusCode,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(u.body)),
	}, nil
}

func newCodexImageBridgeTestContext(body []byte, userAgent string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(string(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	if userAgent != "" {
		c.Request.Header.Set("User-Agent", userAgent)
	}
	return c, rec
}

func newCodexImageBridgeTestService(upstream *codexImageBridgeUpstream, enabled bool) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				CodexImageGenerationBridgeEnabled: enabled,
			},
		},
		httpUpstream:  upstream,
		toolCorrector: NewCodexToolCorrector(),
	}
}

func newCodexImageBridgeTestAccount(extra map[string]any) *Account {
	return &Account{
		ID:       1,
		Name:     "openai-apikey",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: extra,
	}
}

func TestCodexImageGenerationBridgeDisabledDoesNotInjectTool(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","input":"write code","stream":false}`)
	upstream := &codexImageBridgeUpstream{
		body: `{"id":"resp_text","model":"gpt-5.4","usage":{"input_tokens":1,"output_tokens":1}}`,
	}
	svc := newCodexImageBridgeTestService(upstream, false)
	c, _ := newCodexImageBridgeTestContext(body, "codex_cli_rs/0.98.0")

	result, err := svc.Forward(context.Background(), c, newCodexImageBridgeTestAccount(nil), body)
	if err != nil {
		t.Fatalf("Forward() error = %v", err)
	}
	if result == nil {
		t.Fatalf("Forward() result is nil")
	}
	if gjson.GetBytes(upstream.lastBody, `tools.#(type=="image_generation")`).Exists() {
		t.Fatalf("image_generation tool was injected while bridge is disabled: %s", string(upstream.lastBody))
	}
	if strings.Contains(gjson.GetBytes(upstream.lastBody, "instructions").String(), codexImageGenerationBridgeMarker) {
		t.Fatalf("bridge instructions were injected while bridge is disabled: %s", string(upstream.lastBody))
	}
	if result.ImageCount != 0 || result.Model != "gpt-5.4" {
		t.Fatalf("result image billing changed unexpectedly: count=%d model=%s", result.ImageCount, result.Model)
	}
}

func TestCodexImageGenerationBridgeEnabledInjectsToolAndInstructions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","input":"draw a cat","stream":false}`)
	upstream := &codexImageBridgeUpstream{
		body: `{"id":"resp_text","model":"gpt-5.4","usage":{"input_tokens":1,"output_tokens":1}}`,
	}
	svc := newCodexImageBridgeTestService(upstream, true)
	c, _ := newCodexImageBridgeTestContext(body, "codex_cli_rs/0.98.0")

	result, err := svc.Forward(context.Background(), c, newCodexImageBridgeTestAccount(nil), body)
	if err != nil {
		t.Fatalf("Forward() error = %v", err)
	}
	if result == nil {
		t.Fatalf("Forward() result is nil")
	}
	if !gjson.GetBytes(upstream.lastBody, `tools.#(type=="image_generation")`).Exists() {
		t.Fatalf("image_generation tool was not injected: %s", string(upstream.lastBody))
	}
	if got := gjson.GetBytes(upstream.lastBody, `tools.#(type=="image_generation").output_format`).String(); got != "png" {
		t.Fatalf("injected output_format = %q, want png; body=%s", got, string(upstream.lastBody))
	}
	if instructions := gjson.GetBytes(upstream.lastBody, "instructions").String(); !strings.Contains(instructions, codexImageGenerationBridgeMarker) {
		t.Fatalf("bridge instructions missing: %s", string(upstream.lastBody))
	}
	if result.ImageCount != 0 || result.Model != "gpt-5.4" {
		t.Fatalf("text-only response should not use image billing: count=%d model=%s", result.ImageCount, result.Model)
	}
}

func TestOpenAIResponsesImageToolBillingOnlyWhenOutputImageExists(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation","size":"1024x1024"}],"input":"draw","stream":false}`)
	upstream := &codexImageBridgeUpstream{
		body: `{"id":"resp_img","model":"gpt-5.4","output":[{"id":"ig_1","type":"image_generation_call","result":"final-image","size":"1024x1024"}],"usage":{"input_tokens":10,"output_tokens":5,"output_tokens_details":{"image_tokens":123}}}`,
	}
	svc := newCodexImageBridgeTestService(upstream, false)
	c, _ := newCodexImageBridgeTestContext(body, "codex_cli_rs/0.98.0")

	result, err := svc.Forward(context.Background(), c, newCodexImageBridgeTestAccount(nil), body)
	if err != nil {
		t.Fatalf("Forward() error = %v", err)
	}
	if result == nil {
		t.Fatalf("Forward() result is nil")
	}
	if result.ImageCount != 1 {
		t.Fatalf("ImageCount = %d, want 1", result.ImageCount)
	}
	if result.Model != "gpt-image-2" {
		t.Fatalf("billing model = %q, want gpt-image-2", result.Model)
	}
	if result.ImageSize != "1K" {
		t.Fatalf("image size = %q, want 1K", result.ImageSize)
	}
	if result.Usage.ImageOutputTokens != 123 {
		t.Fatalf("image output tokens = %d, want 123", result.Usage.ImageOutputTokens)
	}
}

func TestOpenAIResponsesImageToolWithoutOutputKeepsTokenBillingModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation","size":"1024x1024"}],"input":"maybe draw","stream":false}`)
	upstream := &codexImageBridgeUpstream{
		body: `{"id":"resp_no_img","model":"gpt-5.4","output":[{"type":"message","content":[{"type":"output_text","text":"no image"}]}],"usage":{"input_tokens":10,"output_tokens":5}}`,
	}
	svc := newCodexImageBridgeTestService(upstream, false)
	c, _ := newCodexImageBridgeTestContext(body, "codex_cli_rs/0.98.0")

	result, err := svc.Forward(context.Background(), c, newCodexImageBridgeTestAccount(nil), body)
	if err != nil {
		t.Fatalf("Forward() error = %v", err)
	}
	if result.ImageCount != 0 {
		t.Fatalf("ImageCount = %d, want 0", result.ImageCount)
	}
	if result.Model != "gpt-5.4" {
		t.Fatalf("model = %q, want original text model", result.Model)
	}
}

func TestOpenAIResponsesImageToolNormalizesLegacyFormatFields(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation","format":"jpeg","compression":80}],"input":"draw","stream":false}`)
	upstream := &codexImageBridgeUpstream{
		body: `{"id":"resp_text","model":"gpt-5.4","usage":{"input_tokens":1,"output_tokens":1}}`,
	}
	svc := newCodexImageBridgeTestService(upstream, false)
	c, _ := newCodexImageBridgeTestContext(body, "some-client/1.0")

	_, err := svc.Forward(context.Background(), c, newCodexImageBridgeTestAccount(nil), body)
	if err != nil {
		t.Fatalf("Forward() error = %v", err)
	}
	if got := gjson.GetBytes(upstream.lastBody, `tools.#(type=="image_generation").output_format`).String(); got != "jpeg" {
		t.Fatalf("output_format = %q, want jpeg; body=%s", got, string(upstream.lastBody))
	}
	if !gjson.GetBytes(upstream.lastBody, `tools.#(type=="image_generation").output_compression`).Exists() {
		t.Fatalf("output_compression missing: %s", string(upstream.lastBody))
	}
	if gjson.GetBytes(upstream.lastBody, `tools.#(type=="image_generation").format`).Exists() {
		t.Fatalf("legacy format field still exists: %s", string(upstream.lastBody))
	}
	if gjson.GetBytes(upstream.lastBody, `tools.#(type=="image_generation").compression`).Exists() {
		t.Fatalf("legacy compression field still exists: %s", string(upstream.lastBody))
	}
}

func TestParseSSEUsageFromBodyCountsImagesAndImageTokens(t *testing.T) {
	svc := &OpenAIGatewayService{}
	body := "data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"ig_1\",\"type\":\"image_generation_call\",\"result\":\"final-a\",\"size\":\"1024x1024\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"output_tokens_details\":{\"image_tokens\":77}},\"output\":[{\"id\":\"ig_1\",\"type\":\"image_generation_call\",\"result\":\"final-a\",\"size\":\"1024x1024\"}]}}\n\n" +
		"data: [DONE]\n\n"

	usage := svc.parseSSEUsageFromBody(body)
	if usage.ImageCount != 1 {
		t.Fatalf("ImageCount = %d, want 1", usage.ImageCount)
	}
	if usage.ImageSize != "1K" {
		t.Fatalf("ImageSize = %q, want 1K", usage.ImageSize)
	}
	if usage.ImageOutputTokens != 77 {
		t.Fatalf("ImageOutputTokens = %d, want 77", usage.ImageOutputTokens)
	}
}
