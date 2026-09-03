package service

import (
	"net/http"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"golang.org/x/net/http/httpguts"
)

const (
	credentialHeaderOverrideEnabled = "header_override_enabled"
	credentialHeaderOverrides       = "header_overrides"

	maxHeaderOverrideEntries     = 64
	maxHeaderOverrideNameLength  = 200
	maxHeaderOverrideValueLength = 8192
)

var blockedHeaderOverrideNames = map[string]struct{}{
	"host":                     {},
	"content-length":           {},
	"content-type":             {},
	"transfer-encoding":        {},
	"connection":               {},
	"keep-alive":               {},
	"proxy-authenticate":       {},
	"proxy-authorization":      {},
	"proxy-connection":         {},
	"te":                       {},
	"trailer":                  {},
	"upgrade":                  {},
	"authorization":            {},
	"x-api-key":                {},
	"x-goog-api-key":           {},
	"cookie":                   {},
	"accept-encoding":          {},
	"sec-websocket-key":        {},
	"sec-websocket-version":    {},
	"sec-websocket-extensions": {},
	"sec-websocket-protocol":   {},
	"sec-websocket-accept":     {},
	"session_id":               {},
	"conversation_id":          {},
	"x-codex-turn-state":       {},
	"x-codex-turn-metadata":    {},
	"chatgpt-account-id":       {},
	"x-client-request-id":      {},
	"openai-organization":      {},
	"openai-project":           {},
	"forwarded":                {},
	"via":                      {},
	"x-forwarded-for":          {},
	"x-forwarded-host":         {},
	"x-forwarded-port":         {},
	"x-forwarded-proto":        {},
	"x-real-ip":                {},
	"cf-connecting-ip":         {},
	"true-client-ip":           {},
}

func (a *Account) IsHeaderOverrideEligible() bool {
	// OpenAI API keys and Grok credentials both target OpenAI-compatible
	// upstreams. OAuth Grok requests are eligible as well because the xAI CLI
	// identity headers are re-applied after overrides by the request builder.
	return a != nil && (a.IsOpenAIApiKey() || a.IsGrok())
}

func (a *Account) IsHeaderOverrideEnabled() bool {
	if !a.IsHeaderOverrideEligible() || a.Credentials == nil {
		return false
	}
	enabled, ok := a.Credentials[credentialHeaderOverrideEnabled].(bool)
	return ok && enabled
}

func (a *Account) GetHeaderOverrides() map[string]string {
	if !a.IsHeaderOverrideEnabled() {
		return nil
	}

	result := make(map[string]string)
	switch raw := a.Credentials[credentialHeaderOverrides].(type) {
	case map[string]any:
		for name, rawValue := range raw {
			value, ok := rawValue.(string)
			if !ok {
				continue
			}
			lowerName, normalizedValue, err := normalizeHeaderOverrideEntry(name, value)
			if err == nil && lowerName != "" && normalizedValue != "" {
				result[lowerName] = normalizedValue
			}
		}
	case map[string]string:
		for name, value := range raw {
			lowerName, normalizedValue, err := normalizeHeaderOverrideEntry(name, value)
			if err == nil && lowerName != "" && normalizedValue != "" {
				result[lowerName] = normalizedValue
			}
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func (a *Account) ApplyHeaderOverrides(headers http.Header) {
	if headers == nil {
		return
	}
	for name, value := range a.GetHeaderOverrides() {
		for existing := range headers {
			if strings.EqualFold(existing, name) {
				delete(headers, existing)
			}
		}
		headers[http.CanonicalHeaderKey(name)] = []string{value}
	}
}

func NormalizeHeaderOverrideCredentials(credentials map[string]any) error {
	if credentials == nil {
		return nil
	}
	if raw, ok := credentials[credentialHeaderOverrideEnabled]; ok && raw != nil {
		if _, ok := raw.(bool); !ok {
			return infraerrors.New(http.StatusBadRequest, "INVALID_HEADER_OVERRIDE", "header_override_enabled must be a boolean")
		}
	}

	raw, ok := credentials[credentialHeaderOverrides]
	if !ok || raw == nil {
		return nil
	}

	entries := make(map[string]any)
	switch values := raw.(type) {
	case map[string]any:
		entries = values
	case map[string]string:
		for name, value := range values {
			entries[name] = value
		}
	default:
		return infraerrors.New(http.StatusBadRequest, "INVALID_HEADER_OVERRIDE", "header_overrides must be an object of header name to string value")
	}
	if len(entries) > maxHeaderOverrideEntries {
		return infraerrors.Newf(http.StatusBadRequest, "INVALID_HEADER_OVERRIDE", "header_overrides supports at most %d entries", maxHeaderOverrideEntries)
	}

	normalized := make(map[string]any, len(entries))
	for name, rawValue := range entries {
		value, ok := rawValue.(string)
		if !ok {
			return infraerrors.Newf(http.StatusBadRequest, "INVALID_HEADER_OVERRIDE", "header %q value must be a string", name)
		}
		lowerName, normalizedValue, err := normalizeHeaderOverrideEntry(name, value)
		if err != nil {
			return err
		}
		if lowerName == "" {
			continue
		}
		if _, exists := normalized[lowerName]; exists {
			return infraerrors.Newf(http.StatusBadRequest, "INVALID_HEADER_OVERRIDE", "duplicate header name %q", lowerName)
		}
		normalized[lowerName] = normalizedValue
	}
	credentials[credentialHeaderOverrides] = normalized
	return nil
}

func normalizeHeaderOverrideEntry(name, value string) (string, string, error) {
	lowerName := strings.ToLower(strings.TrimSpace(name))
	value = strings.TrimSpace(value)
	if lowerName == "" {
		if value == "" {
			return "", "", nil
		}
		return "", "", infraerrors.New(http.StatusBadRequest, "INVALID_HEADER_OVERRIDE", "header name must not be empty")
	}
	if len(lowerName) > maxHeaderOverrideNameLength || !httpguts.ValidHeaderFieldName(lowerName) {
		return "", "", infraerrors.Newf(http.StatusBadRequest, "INVALID_HEADER_OVERRIDE", "invalid header name %q", lowerName)
	}
	if _, blocked := blockedHeaderOverrideNames[lowerName]; blocked {
		return "", "", infraerrors.Newf(http.StatusBadRequest, "INVALID_HEADER_OVERRIDE", "header %q is not allowed to be overridden", lowerName)
	}
	if len(value) > maxHeaderOverrideValueLength || !httpguts.ValidHeaderFieldValue(value) {
		return "", "", infraerrors.Newf(http.StatusBadRequest, "INVALID_HEADER_OVERRIDE", "header %q has an invalid value", lowerName)
	}
	return lowerName, value, nil
}
