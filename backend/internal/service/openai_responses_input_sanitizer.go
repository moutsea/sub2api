package service

import (
	"net/url"
	"strings"
)

func normalizeOpenAIResponsesFunctionCallOutputImageURLs(reqBody map[string]any) bool {
	if reqBody == nil {
		return false
	}
	input, ok := reqBody["input"].([]any)
	if !ok {
		return false
	}

	modified := false
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(firstNonEmptyString(item["type"])) != "function_call_output" {
			continue
		}
		normalizedOutput, changed := normalizeFunctionCallOutputImageURLValue(item["output"])
		if changed {
			item["output"] = normalizedOutput
			modified = true
		}
	}
	return modified
}

func normalizeFunctionCallOutputImageURLValue(output any) (any, bool) {
	items, ok := output.([]any)
	if !ok {
		return output, false
	}

	hasInvalidImageURL := false
	textParts := make([]string, 0, len(items))
	for _, rawItem := range items {
		switch item := rawItem.(type) {
		case string:
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				textParts = append(textParts, trimmed)
			}
		case map[string]any:
			if rawImageURL, exists := item["image_url"]; exists {
				if !isValidOpenAIResponsesOutputImageURL(rawImageURL) {
					hasInvalidImageURL = true
					textParts = append(textParts, invalidOutputImageURLText(rawImageURL))
				}
				continue
			}
			if text := firstNonEmptyString(item["text"], item["content"], item["message"]); strings.TrimSpace(text) != "" {
				textParts = append(textParts, strings.TrimSpace(text))
			}
		}
	}

	if !hasInvalidImageURL {
		return output, false
	}
	if len(textParts) == 0 {
		return "[image output omitted: invalid or non-public image_url]", true
	}
	return strings.Join(textParts, "\n"), true
}

func isValidOpenAIResponsesOutputImageURL(raw any) bool {
	switch value := raw.(type) {
	case string:
		return isHTTPOrHTTPSURL(value)
	case map[string]any:
		return isHTTPOrHTTPSURL(firstNonEmptyString(value["url"]))
	default:
		return false
	}
}

func isHTTPOrHTTPSURL(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil {
		return false
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func invalidOutputImageURLText(raw any) string {
	value := firstNonEmptyString(raw)
	if value == "" {
		if rawMap, ok := raw.(map[string]any); ok {
			value = firstNonEmptyString(rawMap["url"])
		}
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "[image output omitted: invalid image_url]"
	}
	if strings.HasPrefix(value, "data:") {
		return "[image output omitted: inline data image_url is not reusable by the upstream Responses API]"
	}
	return "[image output omitted: non-public or invalid image_url]"
}
