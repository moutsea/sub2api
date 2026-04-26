package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
)

type stubOpenAIHTTPUpstream struct {
	resp *http.Response
	err  error
}

func (s stubOpenAIHTTPUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	return s.resp, s.err
}

func TestOpenAIGatewayService_ForwardNormalizesServiceTier(t *testing.T) {
	gin.SetMode(gin.TestMode)

	reqBody := []byte(`{"model":"gpt-5.5","stream":false,"service_tier":"fast","input":"hello"}`)
	respBody := []byte(`{"id":"resp_1","model":"gpt-5.5","usage":{"input_tokens":11,"output_tokens":5,"input_tokens_details":{"cached_tokens":7}}}`)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewReader(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")

	svc := &OpenAIGatewayService{
		cfg: &config.Config{},
		httpUpstream: stubOpenAIHTTPUpstream{
			resp: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
					"X-Request-Id": []string{"req_123"},
				},
				Body: io.NopCloser(bytes.NewReader(respBody)),
			},
		},
	}

	account := &Account{
		ID:          1,
		Name:        "openai-api-key",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Schedulable: true,
		Status:      StatusActive,
		Credentials: map[string]any{"api_key": "sk-test"},
	}

	result, err := svc.Forward(context.Background(), c, account, reqBody)
	if err != nil {
		t.Fatalf("Forward error = %v", err)
	}
	if result == nil || result.ServiceTier == nil {
		t.Fatalf("expected service tier on forward result")
	}
	if *result.ServiceTier != "priority" {
		t.Fatalf("service tier = %q, want %q", *result.ServiceTier, "priority")
	}
}

func TestOpenAIGatewayService_ForwardUsesActualUpstreamServiceTier(t *testing.T) {
	gin.SetMode(gin.TestMode)

	reqBody := []byte(`{"model":"gpt-5.5","stream":false,"service_tier":"fast","input":"hello"}`)
	respBody := []byte(`{"id":"resp_1","model":"gpt-5.5","service_tier":"default","usage":{"input_tokens":11,"output_tokens":5,"input_tokens_details":{"cached_tokens":7}}}`)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewReader(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")

	svc := &OpenAIGatewayService{
		cfg: &config.Config{},
		httpUpstream: stubOpenAIHTTPUpstream{
			resp: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
					"X-Request-Id": []string{"req_123"},
				},
				Body: io.NopCloser(bytes.NewReader(respBody)),
			},
		},
	}

	account := &Account{
		ID:          1,
		Name:        "openai-api-key",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Schedulable: true,
		Status:      StatusActive,
		Credentials: map[string]any{"api_key": "sk-test"},
	}

	result, err := svc.Forward(context.Background(), c, account, reqBody)
	if err != nil {
		t.Fatalf("Forward error = %v", err)
	}
	if result == nil {
		t.Fatalf("expected non-nil result")
	}
	if result.ServiceTier != nil {
		t.Fatalf("service tier = %q, want nil because upstream returned default", *result.ServiceTier)
	}
}

func TestNormalizeOpenAIServiceTier(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want *string
	}{
		{name: "priority", raw: "priority", want: strPtr("priority")},
		{name: "fast alias", raw: "fast", want: strPtr("priority")},
		{name: "flex", raw: " flex ", want: strPtr("flex")},
		{name: "empty", raw: "", want: nil},
		{name: "unknown", raw: "standard", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeOpenAIServiceTier(tt.raw)
			switch {
			case got == nil && tt.want == nil:
				return
			case got == nil || tt.want == nil:
				t.Fatalf("normalizeOpenAIServiceTier(%q) = %v, want %v", tt.raw, got, tt.want)
			case *got != *tt.want:
				t.Fatalf("normalizeOpenAIServiceTier(%q) = %q, want %q", tt.raw, *got, *tt.want)
			}
		})
	}
}

func strPtr(v string) *string {
	return &v
}
