package service

import "testing"

func TestOpenAIActualInputTokensKiroOAuthExcludesCacheCreation(t *testing.T) {
	account := &Account{Platform: PlatformKiro, Type: AccountTypeOAuth}
	result := &OpenAIForwardResult{}
	result.Usage.InputTokens = 1000
	result.Usage.CacheReadInputTokens = 600
	result.Usage.CacheCreationInputTokens = 250

	if got := openAIActualInputTokens(result, account); got != 150 {
		t.Fatalf("actual input tokens = %d, want 150", got)
	}
}

func TestOpenAIActualInputTokensKiroAPIKeyKeepsUpstreamSemantics(t *testing.T) {
	account := &Account{Platform: PlatformKiro, Type: AccountTypeAPIKey, Credentials: map[string]any{"auth_type": KiroAuthMethodAPIKey}}
	result := &OpenAIForwardResult{}
	result.Usage.InputTokens = 1000
	result.Usage.CacheReadInputTokens = 600
	result.Usage.CacheCreationInputTokens = 250

	if got := openAIActualInputTokens(result, account); got != 1000 {
		t.Fatalf("actual input tokens = %d, want 1000", got)
	}
}

func TestOpenAIActualInputTokensClampsAtZero(t *testing.T) {
	account := &Account{Platform: PlatformKiro, Type: AccountTypeOAuth}
	result := &OpenAIForwardResult{}
	result.Usage.InputTokens = 100
	result.Usage.CacheReadInputTokens = 80
	result.Usage.CacheCreationInputTokens = 50

	if got := openAIActualInputTokens(result, account); got != 0 {
		t.Fatalf("actual input tokens = %d, want 0", got)
	}
}
