package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
)

func newSuffixTestContext(betaHeader string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	if betaHeader != "" {
		c.Request.Header.Set("anthropic-beta", betaHeader)
	}
	return c
}

func TestApplyModelContextSuffix_StripsAndInjectsBeta(t *testing.T) {
	c := newSuffixTestContext("")
	body := []byte(`{"model":"claude-sonnet-5[1m]","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)

	gotModel, gotBody, normalized, err := applyModelContextSuffix(c, "claude-sonnet-5[1m]", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !normalized {
		t.Fatal("expected normalized = true")
	}
	if gotModel != "claude-sonnet-5" {
		t.Errorf("model = %q, want claude-sonnet-5", gotModel)
	}

	// body 里的 model 必须同步改写，否则上游仍会收到带后缀的模型名。
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(gotBody, &parsed); err != nil {
		t.Fatalf("rewritten body not valid json: %v", err)
	}
	var bodyModel string
	if err := json.Unmarshal(parsed["model"], &bodyModel); err != nil {
		t.Fatalf("model field not a string: %v", err)
	}
	if bodyModel != "claude-sonnet-5" {
		t.Errorf("body model = %q, want claude-sonnet-5", bodyModel)
	}

	// 其余字段必须保留。
	if _, ok := parsed["messages"]; !ok {
		t.Error("messages field lost during rewrite")
	}
	if _, ok := parsed["max_tokens"]; !ok {
		t.Error("max_tokens field lost during rewrite")
	}

	if got := c.GetHeader("anthropic-beta"); !strings.Contains(got, claude.BetaContext1M) {
		t.Errorf("anthropic-beta = %q, want it to contain %q", got, claude.BetaContext1M)
	}
}

func TestApplyModelContextSuffix_PreservesExistingBeta(t *testing.T) {
	c := newSuffixTestContext(claude.BetaOAuth)
	body := []byte(`{"model":"claude-opus-5[1m]"}`)

	_, _, normalized, err := applyModelContextSuffix(c, "claude-opus-5[1m]", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !normalized {
		t.Fatal("expected normalized = true")
	}

	got := c.GetHeader("anthropic-beta")
	if !strings.Contains(got, claude.BetaOAuth) {
		t.Errorf("anthropic-beta = %q, lost original flag %q", got, claude.BetaOAuth)
	}
	if !strings.Contains(got, claude.BetaContext1M) {
		t.Errorf("anthropic-beta = %q, missing %q", got, claude.BetaContext1M)
	}
}

func TestApplyModelContextSuffix_NoSuffixIsNoop(t *testing.T) {
	c := newSuffixTestContext("")
	body := []byte(`{"model":"claude-sonnet-5"}`)

	gotModel, gotBody, normalized, err := applyModelContextSuffix(c, "claude-sonnet-5", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if normalized {
		t.Error("expected normalized = false for plain model name")
	}
	if gotModel != "claude-sonnet-5" {
		t.Errorf("model = %q, want claude-sonnet-5", gotModel)
	}
	if string(gotBody) != string(body) {
		t.Errorf("body mutated: got %s", gotBody)
	}
	// 没有 1M 请求时不应注入 beta flag。
	if got := c.GetHeader("anthropic-beta"); got != "" {
		t.Errorf("anthropic-beta = %q, want empty", got)
	}
}

func TestApplyModelContextSuffix_InvalidBodyReturnsError(t *testing.T) {
	c := newSuffixTestContext("")
	body := []byte(`not json`)

	gotModel, gotBody, normalized, err := applyModelContextSuffix(c, "claude-sonnet-5[1m]", body)
	if err == nil {
		t.Fatal("expected error for invalid json body")
	}
	if normalized {
		t.Error("expected normalized = false on error")
	}
	// 失败时必须回退到原值，避免调用方拿到半改写状态。
	if gotModel != "claude-sonnet-5[1m]" {
		t.Errorf("model = %q, want original", gotModel)
	}
	if string(gotBody) != string(body) {
		t.Errorf("body = %s, want original", gotBody)
	}
}
