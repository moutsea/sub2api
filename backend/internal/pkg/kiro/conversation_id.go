// Package kiro provides stable conversation ID generation for CodeWhisperer API.
// Stable IDs enable server-side caching on AWS CodeWhisperer, improving cache hit rates.
package kiro

import (
	"crypto/md5"
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ConversationIDManager manages conversation ID generation with caching
type ConversationIDManager struct {
	mu    sync.RWMutex
	cache map[string]string
}

// NewConversationIDManager creates a new conversation ID manager
func NewConversationIDManager() *ConversationIDManager {
	return &ConversationIDManager{
		cache: make(map[string]string),
	}
}

// GenerateConversationID generates a conversation ID based on client characteristics
// Priority:
// 1. X-Session-ID header (client-provided)
// 2. X-Conversation-ID header (backward compatibility)
// 3. MD5 hash of IP + User-Agent + API-Key (stable generation)
func (m *ConversationIDManager) GenerateConversationID(ctx *gin.Context) string {
	// 1. Prefer client-provided X-Session-ID
	if sessionID := ctx.GetHeader("X-Session-ID"); sessionID != "" {
		return sessionID
	}

	// 2. Backward compatibility: use X-Conversation-ID
	if customConvID := ctx.GetHeader("X-Conversation-ID"); customConvID != "" {
		return customConvID
	}

	// 3. Generate stable ID based on client characteristics
	clientIP := getClientIP(ctx)
	userAgent := ctx.GetHeader("User-Agent")
	apiKey := extractAPIKey(ctx)

	clientSignature := fmt.Sprintf("%s|%s|%s", clientIP, userAgent, apiKey)

	// Check cache
	m.mu.RLock()
	if cachedID, exists := m.cache[clientSignature]; exists {
		m.mu.RUnlock()
		return cachedID
	}
	m.mu.RUnlock()

	// Generate MD5 hash for stable conversation ID
	hash := md5.Sum([]byte(clientSignature))
	conversationID := fmt.Sprintf("conv-%x", hash[:8])

	// Cache result
	m.mu.Lock()
	m.cache[clientSignature] = conversationID
	m.mu.Unlock()

	return conversationID
}

// Global instance - singleton pattern
var globalConversationIDManager = NewConversationIDManager()

// GenerateStableConversationID generates a deterministic conversation ID based on client characteristics.
// This enables AWS CodeWhisperer to cache responses for the same client session.
func GenerateStableConversationID(c *gin.Context) string {
	if c == nil {
		return uuid.New().String()
	}
	return globalConversationIDManager.GenerateConversationID(c)
}

// GenerateStableAgentContinuationID generates a deterministic agent continuation ID.
// Uses similar logic to conversation ID but generates standard UUID format.
func GenerateStableAgentContinuationID(c *gin.Context) string {
	if c == nil {
		return uuid.New().String()
	}

	// Check for custom agent continuation ID header
	if customAgentID := c.GetHeader("X-Agent-Continuation-ID"); customAgentID != "" {
		return customAgentID
	}

	// Build client signature for agent
	clientIP := getClientIP(c)
	userAgent := c.GetHeader("User-Agent")
	apiKey := extractAPIKey(c)

	clientSignature := fmt.Sprintf("agent|%s|%s|%s", clientIP, userAgent, apiKey)

	// Generate deterministic UUID format
	return generateDeterministicGUID(clientSignature, "agent")
}

// generateDeterministicGUID generates a deterministic GUID based on input string
// Uses MD5 hash with UUID v3 version bits
func generateDeterministicGUID(input, namespace string) string {
	namespacedInput := fmt.Sprintf("%s|%s", namespace, input)

	hash := md5.Sum([]byte(namespacedInput))

	// Set version bits (Version 3 - MD5 namespace-based UUID)
	hash[6] = (hash[6] & 0x0f) | 0x30 // Version 3
	hash[8] = (hash[8] & 0x3f) | 0x80 // Variant bits

	// Format as standard GUID: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		hash[0:4], hash[4:6], hash[6:8], hash[8:10], hash[10:16])
}

// getClientIP extracts the client IP from the request.
// Priority: X-Real-IP header > Gin's ClientIP().
func getClientIP(c *gin.Context) string {
	if realIP := c.GetHeader("X-Real-IP"); realIP != "" {
		return realIP
	}
	return c.ClientIP()
}

// ExtractAPIKey extracts the API key from the request.
// Exported for use by service layer when generating content-based session IDs.
func ExtractAPIKey(c *gin.Context) string {
	return extractAPIKey(c)
}

// extractAPIKey extracts the API key from the request
func extractAPIKey(c *gin.Context) string {
	// 1. Extract from Authorization header (Bearer token)
	auth := c.GetHeader("Authorization")
	if len(auth) > 7 && auth[:7] == "Bearer " {
		return auth[7:]
	}

	// 2. Extract from x-api-key header (Anthropic style)
	if apiKey := c.GetHeader("x-api-key"); apiKey != "" {
		return apiKey
	}

	// 3. Get from context (set by auth middleware)
	if apiKey, exists := c.Get("api_key"); exists {
		if key, ok := apiKey.(string); ok {
			return key
		}
	}

	// 4. No API key found
	return ""
}

// GenerateContentBasedConversationID generates a stable conversation ID based on
// request content (model + first meaningful user message) and an optional account seed,
// similar to proxycast's SessionManager. This ensures that identical conversations
// produce the same ConversationID for prompt cache hits, while different accounts
// never collide even with identical content.
//
// Strategy:
//  1. Hash accountSeed (apikey / refresh_token) for account isolation
//  2. Hash model name for differentiation
//  3. Find first user message with >10 chars (skip short probes and system reminders)
//  4. SHA256(accountSeed + model + message) → "conv-{hash first 16 chars}"
//  5. Fallback: hash last message if no meaningful user message found
//  6. Fallback: return empty string if no messages at all (caller should use client-based ID)
func GenerateContentBasedConversationID(accountSeed string, model string, messages []ClaudeMessage) string {
	if len(messages) == 0 {
		return ""
	}

	h := sha256.New()
	if accountSeed != "" {
		h.Write([]byte(accountSeed))
		h.Write([]byte("|"))
	}
	h.Write([]byte(model))
	h.Write([]byte("|"))

	contentFound := false
	for _, msg := range messages {
		if msg.Role != "user" {
			continue
		}

		text := extractMessageText(msg)
		// Strip <system-reminder>...</system-reminder> blocks before evaluating
		clean := stripSystemReminders(strings.TrimSpace(text))

		// Skip short messages (probes)
		if len(clean) > 10 {
			h.Write([]byte(clean))
			contentFound = true
			break // Only use first meaningful user message as anchor
		}
	}

	if !contentFound {
		// Fallback: hash last message content
		lastMsg := messages[len(messages)-1]
		text := extractMessageText(lastMsg)
		if text == "" {
			return "" // No usable content
		}
		h.Write([]byte(text))
	}

	hash := fmt.Sprintf("%x", h.Sum(nil))
	return fmt.Sprintf("conv-%s", hash[:16])
}

// extractMessageText extracts plain text from a ClaudeMessage's Content field.
// Handles string content, []any content blocks, and []ContentBlock.
func extractMessageText(msg ClaudeMessage) string {
	switch content := msg.Content.(type) {
	case string:
		return content
	case []any:
		var parts []string
		for _, block := range content {
			blockMap, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if blockMap["type"] == "text" {
				if text, ok := blockMap["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, " ")
	case []ContentBlock:
		var parts []string
		for _, block := range content {
			if block.Type == "text" && block.Text != nil {
				parts = append(parts, *block.Text)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// systemReminderRe matches <system-reminder>...</system-reminder> blocks (including multiline).
var systemReminderRe = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

// stripSystemReminders removes all <system-reminder>...</system-reminder> blocks from text
// and trims the result. This prevents dynamic system-injected content from affecting
// the content-based session fingerprint.
func stripSystemReminders(text string) string {
	return strings.TrimSpace(systemReminderRe.ReplaceAllString(text, ""))
}
