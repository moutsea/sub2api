package service

import (
	"regexp"
	"strings"
)

const maxKiroClientErrorMessageLength = 1024

var (
	kiroAWSARNRegex         = regexp.MustCompile(`(?i)\barn:[a-z0-9-]+:[^\s"'<>]+`)
	kiroBearerRegex         = regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/=-]+`)
	kiroSensitiveFieldRegex = regexp.MustCompile(`(?i)(\b(?:api[_-]?key|x-api-key|client[_-]?secret|access[_-]?token|refresh[_-]?token)\b["']?\s*[:=]\s*["']?)[^"',;\s}]+`)
	kiroProfileFieldRegex   = regexp.MustCompile(`(?i)(\bprofile(?:_?arn|_?id)?\b["']?\s*[:=]\s*["']?)[^"',;\s}]+`)
)

func sanitizeKiroClientErrorMessage(message string) string {
	message = sanitizeUpstreamErrorMessage(strings.TrimSpace(message))
	message = kiroBearerRegex.ReplaceAllString(message, "Bearer ***")
	message = kiroSensitiveFieldRegex.ReplaceAllString(message, `${1}***`)
	message = kiroProfileFieldRegex.ReplaceAllString(message, `${1}***`)
	message = kiroAWSARNRegex.ReplaceAllString(message, "[redacted-arn]")
	return truncateString(message, maxKiroClientErrorMessageLength)
}
