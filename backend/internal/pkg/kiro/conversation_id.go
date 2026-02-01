// Package kiro provides stable conversation ID generation for CodeWhisperer API.
// Stable IDs enable server-side caching on AWS CodeWhisperer, improving cache hit rates.
package kiro

import (
	"crypto/md5"
	"fmt"
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
	clientIP := ctx.ClientIP()
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
	clientIP := c.ClientIP()
	userAgent := c.GetHeader("User-Agent")
	apiKey := extractAPIKey(c)

	clientSignature := fmt.Sprintf("agent|%s|%s|%s", clientIP, userAgent, apiKey)

	// Generate deterministic UUID format
	return generateDeterministicGUID(clientSignature, "agent")
}

// generateDeterministicGUID generates a deterministic GUID based on input string
// Follows UUID v5 specification using MD5 hash
func generateDeterministicGUID(input, namespace string) string {
	namespacedInput := fmt.Sprintf("%s|%s", namespace, input)

	hash := md5.Sum([]byte(namespacedInput))

	// Set version bits (Version 5 - namespace-based UUID)
	hash[6] = (hash[6] & 0x0f) | 0x50 // Version 5
	hash[8] = (hash[8] & 0x3f) | 0x80 // Variant bits

	// Format as standard GUID: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		hash[0:4], hash[4:6], hash[6:8], hash[8:10], hash[10:16])
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
