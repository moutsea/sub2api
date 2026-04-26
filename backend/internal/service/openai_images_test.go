package service

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

func TestOpenAIGatewayServiceParseOpenAIImagesRequestJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","size":"1024x1024","quality":"high","stream":true}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, openAIImagesGenerationsEndpoint, parsed.Endpoint)
	require.Equal(t, "gpt-image-2", parsed.Model)
	require.Equal(t, "draw a cat", parsed.Prompt)
	require.True(t, parsed.Stream)
	require.Equal(t, "1024x1024", parsed.Size)
	require.Equal(t, "1K", parsed.SizeTier)
	require.Equal(t, OpenAIImagesCapabilityNative, parsed.RequiredCapability)
	require.False(t, parsed.Multipart)
}

func TestOpenAIImagesRequestNormalizeForOAuthIgnoresNativeOptions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","quality":"high","style":"vivid","output_format":"jpeg"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.Equal(t, OpenAIImagesCapabilityNative, parsed.RequiredCapability)
	require.ElementsMatch(t, []string{"quality", "style", "output_format"}, parsed.NativeOptions)

	normalized, ignored := parsed.NormalizeForAccount(&Account{Type: AccountTypeOAuth})
	require.NotSame(t, parsed, normalized)
	require.ElementsMatch(t, []string{"quality", "style", "output_format"}, ignored)
	require.False(t, normalized.HasNativeOptions)
	require.Nil(t, normalized.NativeOptions)
	require.Equal(t, OpenAIImagesCapabilityBasic, normalized.RequiredCapability)

	preserved, ignored := parsed.NormalizeForAccount(&Account{Type: AccountTypeAPIKey})
	require.Same(t, parsed, preserved)
	require.Nil(t, ignored)
}

func TestOpenAIImagesRequestNormalizeForOAuthDoesNotIgnoreMask(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "replace background"))
	require.NoError(t, writer.WriteField("quality", "high"))
	imagePart, err := writer.CreateFormFile("image", "source.png")
	require.NoError(t, err)
	_, err = imagePart.Write([]byte("fake-image-bytes"))
	require.NoError(t, err)
	maskPart, err := writer.CreateFormFile("mask", "mask.png")
	require.NoError(t, err)
	_, err = maskPart.Write([]byte("fake-mask-bytes"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body.Bytes())
	require.NoError(t, err)
	require.True(t, parsed.HasMask)
	require.Equal(t, OpenAIImagesCapabilityNative, parsed.RequiredCapability)

	normalized, ignored := parsed.NormalizeForAccount(&Account{Type: AccountTypeOAuth})
	require.Same(t, parsed, normalized)
	require.Nil(t, ignored)
}

func TestExtractOpenAIImageConversationPointersPrefersToolOutputs(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"mapping": map[string]any{
			"user-msg": map[string]any{
				"message": map[string]any{
					"author": map[string]any{"role": "user"},
					"content": map[string]any{
						"content_type": "multimodal_text",
						"parts": []any{
							map[string]any{
								"content_type":  "image_asset_pointer",
								"asset_pointer": "file-service://source-image",
							},
							"edit this image",
						},
					},
					"metadata": map[string]any{
						"attachments": []any{
							map[string]any{"id": "source-image"},
						},
					},
				},
			},
			"tool-msg": map[string]any{
				"message": map[string]any{
					"author":      map[string]any{"role": "tool"},
					"create_time": 123.0,
					"metadata": map[string]any{
						"async_task_type": "image_gen",
						"image_gen_title": "blue cat icon",
					},
					"content": map[string]any{
						"content_type": "multimodal_text",
						"parts": []any{
							map[string]any{
								"content_type":  "image_asset_pointer",
								"asset_pointer": "file-service://edited-image",
							},
						},
					},
				},
			},
		},
	})
	require.NoError(t, err)

	toolPointers, fallbackPointers := extractOpenAIImageConversationPointers(body)
	require.Len(t, toolPointers, 1)
	require.Equal(t, "file-service://edited-image", toolPointers[0].Pointer)
	require.Equal(t, "blue cat icon", toolPointers[0].Prompt)
	require.Len(t, fallbackPointers, 2)
}

func TestExcludeOpenAIUploadedPointerInfos(t *testing.T) {
	items := []openAIImagePointerInfo{
		{Pointer: "file-service://source-image"},
		{Pointer: "file-service://edited-image"},
		{Pointer: "sediment://source-image"},
		{Pointer: "sediment://preview-image"},
	}
	uploads := []openAIUploadedImage{
		{FileID: "source-image"},
	}

	filtered := excludeOpenAIUploadedPointerInfos(items, uploads)
	require.Equal(t, []openAIImagePointerInfo{
		{Pointer: "file-service://edited-image"},
		{Pointer: "sediment://source-image"},
		{Pointer: "sediment://preview-image"},
	}, filtered)
}

type errAfterDataReadCloser struct {
	reader *bytes.Reader
	err    error
	done   bool
}

func (r *errAfterDataReadCloser) Read(p []byte) (int, error) {
	if r.done {
		return 0, r.err
	}
	n, readErr := r.reader.Read(p)
	if errors.Is(readErr, io.EOF) {
		r.done = true
		if n > 0 {
			return n, nil
		}
		return 0, r.err
	}
	return n, readErr
}

func (r *errAfterDataReadCloser) Close() error {
	return nil
}

func TestReadOpenAIImageConversationStreamPreservesConversationOnError(t *testing.T) {
	payload := "data: {\"conversation_id\":\"conv-123\",\"asset_pointer\":\"file-service://edited-image\",\"revised_prompt\":\"blue cat\"}\n"
	resp := &req.Response{
		Response: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body: &errAfterDataReadCloser{
				reader: bytes.NewReader([]byte(payload)),
				err:    errors.New("stream interrupted"),
			},
		},
	}

	conversationID, pointers, usage, firstTokenMs, err := readOpenAIImageConversationStream(resp, time.Now())
	require.Error(t, err)
	require.Equal(t, "stream interrupted", err.Error())
	require.Equal(t, "conv-123", conversationID)
	require.Equal(t, OpenAIUsage{}, usage)
	require.NotNil(t, firstTokenMs)
	require.Len(t, pointers, 1)
	require.Equal(t, "file-service://edited-image", pointers[0].Pointer)
	require.Equal(t, "blue cat", pointers[0].Prompt)
}

func TestBuildOpenAIImageConversationRequestForEditUsesPromptFirstAndSnakeCaseMimeType(t *testing.T) {
	parsed := &OpenAIImagesRequest{Prompt: "turn the cat blue"}
	uploads := []openAIUploadedImage{
		{
			FileID:   "file-123",
			FileName: "source.png",
			FileSize: 12345,
			MimeType: "image/png",
			Width:    1254,
			Height:   1254,
		},
	}

	req := buildOpenAIImageConversationRequest(parsed, "parent-123", uploads)
	messages, _ := req["messages"].([]any)
	require.Len(t, messages, 1)

	message, _ := messages[0].(map[string]any)
	content, _ := message["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	require.Len(t, parts, 2)
	require.Equal(t, "turn the cat blue", parts[0])

	imagePart, _ := parts[1].(map[string]any)
	require.Equal(t, "image_asset_pointer", imagePart["content_type"])
	require.Equal(t, "file-service://file-123", imagePart["asset_pointer"])
	require.EqualValues(t, 1254, imagePart["width"])
	require.EqualValues(t, 1254, imagePart["height"])

	metadata, _ := message["metadata"].(map[string]any)
	attachments, _ := metadata["attachments"].([]map[string]any)
	if attachments == nil {
		rawAttachments, _ := metadata["attachments"].([]any)
		require.Len(t, rawAttachments, 1)
		attachment, _ := rawAttachments[0].(map[string]any)
		require.Equal(t, "image/png", attachment["mime_type"])
		_, hasCamel := attachment["mimeType"]
		require.False(t, hasCamel)
		require.EqualValues(t, 1254, attachment["width"])
		require.EqualValues(t, 1254, attachment["height"])
		return
	}

	require.Len(t, attachments, 1)
	require.Equal(t, "image/png", attachments[0]["mime_type"])
	_, hasCamel := attachments[0]["mimeType"]
	require.False(t, hasCamel)
	require.EqualValues(t, 1254, attachments[0]["width"])
	require.EqualValues(t, 1254, attachments[0]["height"])
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequestMultipartEditRemainsBasic(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pngBytes, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO5qS9sAAAAASUVORK5CYII=")
	require.NoError(t, err)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "replace background"))
	require.NoError(t, writer.WriteField("size", "1536x1024"))
	part, err := writer.CreateFormFile("image", "source.png")
	require.NoError(t, err)
	_, err = part.Write(pngBytes)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body.Bytes())
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, openAIImagesEditsEndpoint, parsed.Endpoint)
	require.True(t, parsed.Multipart)
	require.Equal(t, "gpt-image-2", parsed.Model)
	require.Equal(t, "replace background", parsed.Prompt)
	require.Equal(t, "1536x1024", parsed.Size)
	require.Equal(t, "2K", parsed.SizeTier)
	require.Len(t, parsed.Uploads, 1)
	require.Equal(t, 1, parsed.Uploads[0].Width)
	require.Equal(t, 1, parsed.Uploads[0].Height)
	require.Equal(t, OpenAIImagesCapabilityBasic, parsed.RequiredCapability)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequestMultipartEditWithMaskRequiresNative(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "replace background"))
	imagePart, err := writer.CreateFormFile("image", "source.png")
	require.NoError(t, err)
	_, err = imagePart.Write([]byte("fake-image-bytes"))
	require.NoError(t, err)
	maskPart, err := writer.CreateFormFile("mask", "mask.png")
	require.NoError(t, err)
	_, err = maskPart.Write([]byte("fake-mask-bytes"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body.Bytes())
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.True(t, parsed.HasMask)
	require.Len(t, parsed.Uploads, 1)
	require.Equal(t, OpenAIImagesCapabilityNative, parsed.RequiredCapability)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequestPromptOnlyDefaultsToBasic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"prompt":"draw a cat"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, "gpt-image-2", parsed.Model)
	require.Equal(t, OpenAIImagesCapabilityBasic, parsed.RequiredCapability)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequestExplicitModelRemainsBasic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, OpenAIImagesCapabilityBasic, parsed.RequiredCapability)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequestExplicitSizeRemainsBasic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"prompt":"draw a cat","size":"1024x1024"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, "1K", parsed.SizeTier)
	require.Equal(t, OpenAIImagesCapabilityBasic, parsed.RequiredCapability)
}

func TestResolveOpenAIImageBillingSizeTierOAuthAlwaysUses1K(t *testing.T) {
	require.Equal(t, "1K", resolveOpenAIImageBillingSizeTier(&Account{Type: AccountTypeOAuth}, &OpenAIImagesRequest{SizeTier: "4K", ExplicitSize: true}))
	require.Equal(t, "4K", resolveOpenAIImageBillingSizeTier(&Account{Type: AccountTypeAPIKey}, &OpenAIImagesRequest{SizeTier: "4K", ExplicitSize: true}))
	require.Equal(t, "1K", resolveOpenAIImageBillingSizeTier(&Account{Type: AccountTypeAPIKey}, &OpenAIImagesRequest{SizeTier: "2K", ExplicitSize: false}))
	require.Equal(t, "", resolveOpenAIImageBillingSizeTier(nil, nil))
}
