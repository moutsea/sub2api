package service

import "encoding/json"

// CleanGeminiNativeThoughtSignatures replaces native Gemini thoughtSignature
// fields with a dummy signature to avoid cross-account signature validation
// failures after sticky-session failover.
func CleanGeminiNativeThoughtSignatures(body []byte) []byte {
	if len(body) == 0 {
		return body
	}

	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}

	cleaned := replaceThoughtSignaturesRecursive(payload)
	result, err := json.Marshal(cleaned)
	if err != nil {
		return body
	}
	return result
}

func replaceThoughtSignaturesRecursive(data any) any {
	switch value := data.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, nested := range value {
			if key == "thoughtSignature" {
				result[key] = geminiDummyThoughtSignature
				continue
			}
			result[key] = replaceThoughtSignaturesRecursive(nested)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, nested := range value {
			result[i] = replaceThoughtSignaturesRecursive(nested)
		}
		return result
	default:
		return value
	}
}
