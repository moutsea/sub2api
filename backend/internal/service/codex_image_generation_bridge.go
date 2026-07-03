package service

import "strings"

const featureKeyCodexImageGenerationBridge = "codex_image_generation_bridge"

func boolOverridePtr(v bool) *bool {
	return &v
}

func boolOverrideFromMap(values map[string]any, keys ...string) *bool {
	if values == nil {
		return nil
	}
	for _, key := range keys {
		if v, ok := values[key].(bool); ok {
			return boolOverridePtr(v)
		}
	}
	return nil
}

func (a *Account) CodexImageGenerationBridgeOverride() *bool {
	if a == nil || a.Platform != PlatformOpenAI || a.Extra == nil {
		return nil
	}
	if override := boolOverrideFromMap(a.Extra, featureKeyCodexImageGenerationBridge, "codex_image_generation_bridge_enabled"); override != nil {
		return override
	}
	openaiConfig, _ := a.Extra[PlatformOpenAI].(map[string]any)
	return boolOverrideFromMap(openaiConfig, featureKeyCodexImageGenerationBridge, "codex_image_generation_bridge_enabled")
}

func (s *OpenAIGatewayService) isCodexImageGenerationBridgeEnabled(account *Account) bool {
	if override := account.CodexImageGenerationBridgeOverride(); override != nil {
		return *override
	}
	return s != nil && s.cfg != nil && s.cfg.Gateway.CodexImageGenerationBridgeEnabled
}

func GroupAllowsImageGeneration(group *Group) bool {
	return true
}

func getAPIKeyFromGinContext(c interface{ Get(string) (any, bool) }) *APIKey {
	if c == nil {
		return nil
	}
	v, exists := c.Get("api_key")
	if !exists {
		return nil
	}
	apiKey, _ := v.(*APIKey)
	return apiKey
}

func firstNonEmptyString(values ...any) string {
	for _, value := range values {
		switch v := value.(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case []byte:
			if s := strings.TrimSpace(string(v)); s != "" {
				return s
			}
		}
	}
	return ""
}
