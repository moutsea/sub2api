package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
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

func TestOpenAIGatewayServiceParseOpenAIImagesRequestJSONReferenceRequiresOAuthAndSniffsJPEG(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var imageBody bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.White)
	require.NoError(t, jpeg.Encode(&imageBody, img, nil))

	body, err := json.Marshal(map[string]any{
		"model":            "gpt-image-2",
		"prompt":           "extend this image",
		"response_format":  "b64_json",
		"reference_images": []string{base64.StdEncoding.EncodeToString(imageBody.Bytes())},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, OpenAIImagesCapabilityOAuth, parsed.RequiredCapability)
	require.Len(t, parsed.Uploads, 1)
	require.Equal(t, "image/jpeg", parsed.Uploads[0].ContentType)
	require.Equal(t, "reference_0.jpg", parsed.Uploads[0].FileName)
	require.Equal(t, 1, parsed.Uploads[0].Width)
	require.Equal(t, 1, parsed.Uploads[0].Height)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequestJSONImageURLRequiresOAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body, err := json.Marshal(map[string]any{
		"model":  "gpt-image-2",
		"prompt": "turn the cat blue",
		"image_url": map[string]any{
			"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO5qS9sAAAAASUVORK5CYII=",
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, openAIImagesEditsEndpoint, parsed.Endpoint)
	require.Equal(t, OpenAIImagesCapabilityOAuth, parsed.RequiredCapability)
	require.Len(t, parsed.Uploads, 1)
	require.Equal(t, "image/png", parsed.Uploads[0].ContentType)
	require.Equal(t, "reference_0.png", parsed.Uploads[0].FileName)
	require.Equal(t, 1, parsed.Uploads[0].Width)
	require.Equal(t, 1, parsed.Uploads[0].Height)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequestJSONInvalidReferenceErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body, err := json.Marshal(map[string]any{
		"model":            "gpt-image-2",
		"prompt":           "extend this image",
		"reference_images": []string{base64.StdEncoding.EncodeToString([]byte("not an image"))},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.Error(t, err)
	require.Nil(t, parsed)
	require.Contains(t, err.Error(), "reference_images")
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

func TestExtractOpenAIImageConversationPointersAcceptsToolBareImageFileID(t *testing.T) {
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
						},
					},
				},
			},
			"tool-msg": map[string]any{
				"message": map[string]any{
					"author":      map[string]any{"role": "tool"},
					"create_time": 124.0,
					"metadata": map[string]any{
						"async_task_type": "image_gen",
						"image_gen_title": "blue cat icon",
					},
					"content": map[string]any{
						"content_type": "multimodal_text",
						"parts": []any{
							`file_00000000abcdefabcdefabcdefabcdef`,
						},
					},
				},
			},
		},
	})
	require.NoError(t, err)

	toolPointers, fallbackPointers := extractOpenAIImageConversationPointers(body)
	require.Len(t, toolPointers, 1)
	require.Equal(t, "file-service://file_00000000abcdefabcdefabcdefabcdef", toolPointers[0].Pointer)
	require.Equal(t, "blue cat icon", toolPointers[0].Prompt)
	require.Len(t, fallbackPointers, 1)
}

func TestExtractOpenAIImageConversationPointersAcceptsAssistantImageGenMetadataAttachment(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"mapping": map[string]any{
			"assistant-msg": map[string]any{
				"message": map[string]any{
					"author":      map[string]any{"role": "assistant"},
					"create_time": 124.0,
					"metadata": map[string]any{
						"async_task_type": "image_gen",
						"image_gen_title": "blue cat icon",
						"attachments": []any{
							map[string]any{"id": "file_00000000abcdefabcdefabcdefabcdef"},
						},
					},
					"content": map[string]any{
						"content_type": "text",
						"parts":        []any{"Done."},
					},
				},
			},
		},
	})
	require.NoError(t, err)

	toolPointers, fallbackPointers := extractOpenAIImageConversationPointers(body)
	require.Len(t, toolPointers, 1)
	require.Equal(t, "file-service://file_00000000abcdefabcdefabcdefabcdef", toolPointers[0].Pointer)
	require.Equal(t, "blue cat icon", toolPointers[0].Prompt)
	require.Empty(t, fallbackPointers)
}

func TestExtractOpenAIImageConversationPointersIgnoresAssistantReferencedImageIDs(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"mapping": map[string]any{
			"assistant-msg": map[string]any{
				"message": map[string]any{
					"author":      map[string]any{"role": "assistant"},
					"create_time": 124.0,
					"metadata": map[string]any{
						"dalle": map[string]any{"prompt": "blue cat icon"},
					},
					"content": map[string]any{
						"content_type": "text",
						"parts": []any{
							`{"referenced_image_ids":["file_00000000abcdefabcdefabcdefabcdef"]}`,
						},
					},
				},
			},
		},
	})
	require.NoError(t, err)

	toolPointers, fallbackPointers := extractOpenAIImageConversationPointers(body)
	require.Empty(t, toolPointers)
	require.Empty(t, fallbackPointers)
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

func TestReferenceUploadDataHashSetMatchesUploadedBytes(t *testing.T) {
	source := []byte("source-image-bytes")
	generated := []byte("generated-image-bytes")
	hashes := referenceUploadDataHashSet([]openAIUploadedImage{
		{DataSHA256: hashOpenAIImageBytes(source)},
	})

	require.True(t, isReferenceUploadData(source, hashes))
	require.False(t, isReferenceUploadData(generated, hashes))
	require.False(t, isReferenceUploadData(nil, hashes))
	require.False(t, isReferenceUploadData(source, nil))
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

func TestReadOpenAIImageConversationStreamReturnsAfterConversationAndPointer(t *testing.T) {
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
	require.NoError(t, err)
	require.Equal(t, "conv-123", conversationID)
	require.Equal(t, OpenAIUsage{}, usage)
	require.NotNil(t, firstTokenMs)
	require.Len(t, pointers, 1)
	require.Equal(t, "file-service://edited-image", pointers[0].Pointer)
	require.Equal(t, "blue cat", pointers[0].Prompt)
}

func TestReadOpenAIImageConversationStreamPreservesConversationOnErrorWithoutPointer(t *testing.T) {
	payload := "data: {\"conversation_id\":\"conv-123\",\"message\":{\"content\":{\"parts\":[\"working\"]}}}\n"
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
	require.Empty(t, pointers)
	require.Equal(t, OpenAIUsage{}, usage)
	require.NotNil(t, firstTokenMs)
}

func TestOpenAIImagesJSONHeartbeatDoesNotCommitBeforeDelay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	heartbeat := uncommittedOpenAIImagesHeartbeatForTest()
	err := writeOpenAIImagesJSONResponse(c, heartbeat, http.StatusOK, openAIImagesJSONContentType, []byte(`{"ok":true}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"ok":true}`, rec.Body.String())
	require.Equal(t, `{"ok":true}`, rec.Body.String())
}

func TestOpenAIImagesJSONHeartbeatCanBeDisabledForAsyncJobs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	c.Set(OpenAIImagesDisableJSONHeartbeatContextKey, true)

	heartbeat := startOpenAIImagesJSONHeartbeatWithTiming(c, http.StatusOK, 0, time.Millisecond)
	err := writeOpenAIImagesJSONResponse(c, heartbeat, http.StatusBadGateway, openAIImagesJSONContentType, []byte(`{"error":{"message":"failed"}}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Equal(t, `{"error":{"message":"failed"}}`, rec.Body.String())
}

func TestOpenAIImagesJSONHeartbeatKeepsFinalJSONValid(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	heartbeat := committedOpenAIImagesHeartbeatForTest(t, c)
	err := writeOpenAIImagesJSONResponse(c, heartbeat, http.StatusOK, openAIImagesJSONContentType, []byte(`{"ok":true}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "\n")
	require.JSONEq(t, `{"ok":true}`, rec.Body.String())
}

func TestOpenAIImagesJSONHeartbeatWritesErrorBodyAfterCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	heartbeat := committedOpenAIImagesHeartbeatForTest(t, c)
	err := finishOpenAIImagesError(c, heartbeat, errors.New("upstream failed"), "upstream_error", "Upstream failed")
	require.EqualError(t, err, "upstream failed")
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"error":{"type":"upstream_error","message":"Upstream failed"}}`, rec.Body.String())
}

func TestOpenAIImagesJSONHeartbeatWritesFailoverErrorBodyAfterCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	heartbeat := committedOpenAIImagesHeartbeatForTest(t, c)
	err := finishOpenAIImagesError(
		c,
		heartbeat,
		&UpstreamFailoverError{StatusCode: http.StatusTooManyRequests, Message: "rate limited"},
		"rate_limit_error",
		"Upstream rate limit exceeded, please retry later",
	)
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"error":{"type":"rate_limit_error","message":"Upstream rate limit exceeded, please retry later"}}`, rec.Body.String())
}

func TestCommittedOpenAIImagesUpstreamErrorWritesRawErrorBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name        string
		statusCode  int
		body        string
		wantPayload string
	}{
		{
			name:       "non failover upstream error",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"message":"bad request"}}`,
			wantPayload: `{
				"error": {
					"type": "upstream_error",
					"message": "Upstream request failed"
				}
			}`,
		},
		{
			name:       "failover upstream error does not return sentinel after commit",
			statusCode: http.StatusTooManyRequests,
			body:       `{"error":{"message":"rate limited"}}`,
			wantPayload: `{
				"error": {
					"type": "rate_limit_error",
					"message": "Upstream rate limit exceeded, please retry later"
				}
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
			committedOpenAIImagesHeartbeatForTest(t, c)

			resp := &http.Response{
				StatusCode: tt.statusCode,
				Header:     http.Header{"X-Request-Id": []string{"req_test"}},
			}
			svc := &OpenAIGatewayService{}
			err := svc.handleCommittedOpenAIImagesUpstreamError(
				context.Background(),
				c,
				&Account{ID: 123, Name: "openai", Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
				resp,
				[]byte(tt.body),
				"upstream says no",
			)
			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr))
			require.Equal(t, http.StatusOK, rec.Code)
			require.JSONEq(t, tt.wantPayload, rec.Body.String())
		})
	}
}

func uncommittedOpenAIImagesHeartbeatForTest() *openAIImagesJSONHeartbeat {
	doneCh := make(chan struct{})
	close(doneCh)
	return &openAIImagesJSONHeartbeat{
		stopCh: make(chan struct{}),
		doneCh: doneCh,
	}
}

func committedOpenAIImagesHeartbeatForTest(t *testing.T, c *gin.Context) *openAIImagesJSONHeartbeat {
	t.Helper()
	flusher, ok := c.Writer.(http.Flusher)
	require.True(t, ok)
	doneCh := make(chan struct{})
	close(doneCh)
	heartbeat := &openAIImagesJSONHeartbeat{
		writer:  c.Writer,
		flusher: flusher,
		status:  http.StatusOK,
		stopCh:  make(chan struct{}),
		doneCh:  doneCh,
	}
	require.True(t, heartbeat.writeWhitespace())
	return heartbeat
}

func TestBuildOpenAIImageConversationRequestForEditUsesPromptFirstAndSnakeCaseMimeType(t *testing.T) {
	parsed := &OpenAIImagesRequest{Model: "gpt-image-2", Prompt: "turn the cat blue"}
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
		require.Equal(t, "auto", req["model"])
		return
	}

	require.Len(t, attachments, 1)
	require.Equal(t, "image/png", attachments[0]["mime_type"])
	_, hasCamel := attachments[0]["mimeType"]
	require.False(t, hasCamel)
	require.EqualValues(t, 1254, attachments[0]["width"])
	require.EqualValues(t, 1254, attachments[0]["height"])
	require.Equal(t, "auto", req["model"])
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
