// Package kiro provides types and utilities for Kiro/CodeWhisperer API integration.
package kiro

import (
	"encoding/json"
	"log"
)

// AWSQ API endpoint
const (
	AWsqEndpoint = "https://q.us-east-1.amazonaws.com"
	AWsqPath     = "/generateAssistantResponse"
)

// Model ID mapping from Claude to AWSQ
// Format aligned with kiro.rs: use simplified model IDs
var ModelMap = map[string]string{
	// Opus 4.6 series
	"claude-opus-4-6": "claude-opus-4.6",
	// Sonnet 4.6 series
	"claude-sonnet-4-6": "claude-sonnet-4.6",
	// Opus 4.5 series
	"claude-opus-4-5":          "claude-opus-4.5",
	"claude-opus-4-5-20251101": "claude-opus-4.5",
	"claude-opus-4.5":          "claude-opus-4.5",
	// Haiku 4.5 series
	"claude-haiku-4-5":          "claude-haiku-4.5",
	"claude-haiku-4-5-20251001": "claude-haiku-4.5",
	// Sonnet 4.5 series
	"claude-sonnet-4-5":          "claude-sonnet-4.5",
	"claude-sonnet-4-5-20250929": "claude-sonnet-4.5",
	// Sonnet 4 series (map to sonnet-4.6)
	"claude-sonnet-4-20250514": "claude-sonnet-4.6",
	// Sonnet 3.7 series (map to sonnet-4.6)
	"claude-3-7-sonnet-20250219": "claude-sonnet-4.6",
	// Sonnet 3.5 series (legacy, map to sonnet-4.6)
	"claude-3-5-sonnet-20241022": "claude-sonnet-4.6",
	"claude-3-5-sonnet-latest":   "claude-sonnet-4.6",
	"claude-3-5-sonnet-v2":       "claude-sonnet-4.6",
	// Haiku 3.5 series (legacy, map to Haiku 4.5)
	"claude-3-5-haiku-20241022": "claude-haiku-4.5",
	"claude-3-5-haiku-latest":   "claude-haiku-4.5",
}

// Default model ID for AWSQ
const DefaultModelID = "claude-sonnet-4.6"

// GetModelID returns the CodeWhisperer model ID for a given Claude model
func GetModelID(claudeModel string) string {
	if modelID, ok := ModelMap[claudeModel]; ok {
		return modelID
	}
	log.Printf("[kiro-ModelMap] WARN model_fallback: %q not in ModelMap, using default %s", claudeModel, DefaultModelID)
	return DefaultModelID
}

// ==================== Request Types ====================

// CodeWhispererRequest represents the CodeWhisperer API request structure
type CodeWhispererRequest struct {
	ProfileArn        string            `json:"profileArn,omitempty"`
	ConversationState ConversationState `json:"conversationState"`
}

// ConversationState represents the conversation state
type ConversationState struct {
	AgentContinuationID string         `json:"agentContinuationId,omitempty"`
	AgentTaskType       string         `json:"agentTaskType,omitempty"`
	ChatTriggerType     string         `json:"chatTriggerType"`
	CurrentMessage      CurrentMessage `json:"currentMessage"`
	ConversationID      string         `json:"conversationId"`
	History             []HistoryEntry `json:"history,omitempty"`
}

// CurrentMessage represents the current message in the conversation
type CurrentMessage struct {
	UserInputMessage UserInputMessage `json:"userInputMessage"`
}

// UserInputMessage represents the user input message
type UserInputMessage struct {
	Content                 string                   `json:"content"`
	ModelID                 string                   `json:"modelId"`
	Origin                  string                   `json:"origin"`
	Images                  []CodeWhispererImage     `json:"images,omitempty"`
	UserInputMessageContext *UserInputMessageContext `json:"userInputMessageContext,omitempty"`
}

// UserInputMessageContext represents the context for user input
type UserInputMessageContext struct {
	ToolResults []ToolResult `json:"toolResults,omitempty"`
	Tools       []ToolItem   `json:"tools,omitempty"`
}

// ToolItem represents a tool item (standard tool or web search)
type ToolItem struct {
	Standard  *CodeWhispererTool `json:"-"`
	WebSearch *WebSearchTool     `json:"-"`
}

// WebSearchTool represents a web search tool
type WebSearchTool struct {
	Type string `json:"type"` // "web_search"
}

// MarshalJSON implements custom JSON marshaling for ToolItem
func (t ToolItem) MarshalJSON() ([]byte, error) {
	if t.WebSearch != nil {
		return json.Marshal(t.WebSearch)
	}
	if t.Standard != nil {
		return json.Marshal(t.Standard)
	}
	return json.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshaling for ToolItem
func (t *ToolItem) UnmarshalJSON(data []byte) error {
	var ws WebSearchTool
	if err := json.Unmarshal(data, &ws); err == nil && ws.Type == "web_search" {
		t.WebSearch = &ws
		return nil
	}
	var std CodeWhispererTool
	if err := json.Unmarshal(data, &std); err == nil {
		t.Standard = &std
		return nil
	}
	return nil
}

// CodeWhispererTool represents a tool definition
type CodeWhispererTool struct {
	ToolSpecification ToolSpecification `json:"toolSpecification"`
}

// ToolSpecification represents the tool specification
type ToolSpecification struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

// InputSchema represents the tool input schema
type InputSchema struct {
	JSON map[string]any `json:"json"`
}

// CodeWhispererImage represents an image in the request
type CodeWhispererImage struct {
	Format string      `json:"format"` // "jpeg", "png", "gif", "webp"
	Source ImageSource `json:"source"`
}

// ImageSource represents the image source
type ImageSource struct {
	Bytes string `json:"bytes"` // base64 encoded image data
}

// ToolResult represents a tool execution result
type ToolResult struct {
	ToolUseID string           `json:"toolUseId"`
	Content   []map[string]any `json:"content"`
	Status    string           `json:"status"` // "success" or "error"
	IsError   bool             `json:"isError,omitempty"`
}

// ToolUseEntry represents a tool use entry in assistant response
type ToolUseEntry struct {
	ToolUseID string         `json:"toolUseId"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input"`
}

// ==================== History Types ====================

// HistoryEntry represents a history entry (user or assistant message)
type HistoryEntry struct {
	MessageID string                    `json:"messageId"`
	Type      string                    // "user" or "assistant" (not serialized)
	User      *HistoryUserMessage       `json:"userInputMessage,omitempty"`
	Assistant *HistoryAssistantMessage  `json:"assistantResponseMessage,omitempty"`
}

// HistoryUserMessage represents a user message in history
type HistoryUserMessage struct {
	Content                 string                   `json:"content"`
	ModelID                 string                   `json:"modelId"`
	Origin                  string                   `json:"origin"`
	Images                  []CodeWhispererImage     `json:"images,omitempty"`
	UserInputMessageContext *UserInputMessageContext `json:"userInputMessageContext,omitempty"`
}

// HistoryAssistantMessage represents an assistant message in history
type HistoryAssistantMessage struct {
	Content  string         `json:"content"`
	ToolUses []ToolUseEntry `json:"toolUses,omitempty"`
}

// MarshalJSON implements custom JSON marshaling for HistoryEntry
func (he HistoryEntry) MarshalJSON() ([]byte, error) {
	if he.Type == "user" && he.User != nil {
		return json.Marshal(map[string]any{
			"messageId":        he.MessageID,
			"userInputMessage": he.User,
		})
	}
	if he.Type == "assistant" && he.Assistant != nil {
		return json.Marshal(map[string]any{
			"messageId":                he.MessageID,
			"assistantResponseMessage": he.Assistant,
		})
	}
	return json.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshaling for HistoryEntry
func (he *HistoryEntry) UnmarshalJSON(data []byte) error {
	var userWrapper struct {
		MessageID        string              `json:"messageId"`
		UserInputMessage *HistoryUserMessage `json:"userInputMessage"`
	}
	if err := json.Unmarshal(data, &userWrapper); err == nil && userWrapper.UserInputMessage != nil {
		he.MessageID = userWrapper.MessageID
		he.Type = "user"
		he.User = userWrapper.UserInputMessage
		return nil
	}

	var assistantWrapper struct {
		MessageID                string                   `json:"messageId"`
		AssistantResponseMessage *HistoryAssistantMessage `json:"assistantResponseMessage"`
	}
	if err := json.Unmarshal(data, &assistantWrapper); err == nil && assistantWrapper.AssistantResponseMessage != nil {
		he.MessageID = assistantWrapper.MessageID
		he.Type = "assistant"
		he.Assistant = assistantWrapper.AssistantResponseMessage
		return nil
	}

	return nil
}

// IsUser returns true if this is a user message
func (he *HistoryEntry) IsUser() bool {
	return he.Type == "user"
}

// IsAssistant returns true if this is an assistant message
func (he *HistoryEntry) IsAssistant() bool {
	return he.Type == "assistant"
}

// ==================== Response/Streaming Types ====================

// StreamEventType represents the type of stream event
type StreamEventType int

const (
	EventUnknown StreamEventType = iota
	EventMessageStart
	EventContentBlockStart
	EventTextDelta
	EventToolUseStart
	EventToolUseInputDelta
	EventToolUseStop
	EventContentBlockStop
	EventMessageStop
	EventBackendUsage
	EventError
	EventThinkingDelta
)

// StopReason represents the reason for message stop
type StopReason int

const (
	StopReasonEndTurn StopReason = iota
	StopReasonToolUse
)

// ToAnthropic converts stop reason to Anthropic format
func (r StopReason) ToAnthropic() string {
	switch r {
	case StopReasonToolUse:
		return "tool_use"
	default:
		return "end_turn"
	}
}

// ContentBlockKind represents the kind of content block
type ContentBlockKind int

const (
	BlockText ContentBlockKind = iota
	BlockToolUse
	BlockThinking
)

// ContentBlockType describes the content block type
type ContentBlockType struct {
	Kind     ContentBlockKind
	ToolID   string
	ToolName string
}

// StreamEvent represents a parsed stream event
type StreamEvent struct {
	Type StreamEventType

	// MessageStart
	MessageID string
	Model     string

	// ContentBlockStart/Stop
	Index     uint32
	BlockType ContentBlockType
	ToolInput any

	// TextDelta
	Text string

	// Tool events
	ToolID      string
	ToolName    string
	PartialJSON string

	// MessageStop
	StopReason StopReason

	// BackendUsage
	Credits           float64
	ContextPercentage float64

	// Error
	ErrorType    string
	ErrorMessage string
}

// ==================== Claude Request Types (for transformation) ====================

// ClaudeMessage represents a message in Claude format
type ClaudeMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []ContentBlock
}

// ContentBlock represents a content block in Claude format
type ContentBlock struct {
	Type      string             `json:"type"`
	Text      *string            `json:"text,omitempty"`
	Source    *ClaudeImageSource `json:"source,omitempty"` // Claude image source with media_type
	ID        *string            `json:"id,omitempty"`
	Name      *string            `json:"name,omitempty"`
	Input     any                `json:"input,omitempty"`
	ToolUseID *string            `json:"tool_use_id,omitempty"`
	Content   any                `json:"content,omitempty"`
	IsError   *bool              `json:"is_error,omitempty"`
	Thinking  *string            `json:"thinking,omitempty"`
	Signature *string            `json:"signature,omitempty"`
}

// ClaudeImageSource represents the image source in Claude format
type ClaudeImageSource struct {
	Type      string `json:"type,omitempty"`       // "base64"
	MediaType string `json:"media_type,omitempty"` // "image/png", "image/jpeg", etc.
	Data      string `json:"data,omitempty"`       // base64 encoded image data
}

// ClaudeTool represents a tool definition in Claude format
type ClaudeTool struct {
	Type        string         `json:"type,omitempty"` // Tool type (e.g., "web_search_20250305" for web search)
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// ==================== Unified Message Types ====================

// UnifiedMessage represents a unified message for transformation
type UnifiedMessage struct {
	Role        string
	TextParts   []string
	Images      []CodeWhispererImage
	ToolUses    []ToolUseData
	ToolResults []ToolResultData
	Thinking    *ThinkingData
}

// HasContent checks if the message has any content
func (m *UnifiedMessage) HasContent() bool {
	return len(m.TextParts) > 0 || len(m.Images) > 0 || len(m.ToolUses) > 0 || len(m.ToolResults) > 0
}

// HasToolUses checks if the message has tool uses
func (m *UnifiedMessage) HasToolUses() bool {
	return len(m.ToolUses) > 0
}

// HasToolResults checks if the message has tool results
func (m *UnifiedMessage) HasToolResults() bool {
	return len(m.ToolResults) > 0
}

// GetText returns the combined text content
func (m *UnifiedMessage) GetText() string {
	if len(m.TextParts) == 0 {
		return ""
	}
	if len(m.TextParts) == 1 {
		return m.TextParts[0]
	}
	result := ""
	for i, part := range m.TextParts {
		if i > 0 {
			result += "\n"
		}
		result += part
	}
	return result
}

// ToolUseData represents tool use data
type ToolUseData struct {
	ID    string
	Name  string
	Input map[string]any
}

// ToolResultData represents tool result data
type ToolResultData struct {
	ToolUseID string
	Content   string
	Images    []CodeWhispererImage // Images from tool result
	IsError   bool
}

// ThinkingData represents thinking data (for extended thinking support)
type ThinkingData struct {
	Content   string
	Signature string
}

// ==================== Helper Functions ====================

// MediaTypeToFormat converts MIME type to format
func MediaTypeToFormat(mediaType string) string {
	switch mediaType {
	case "image/jpeg":
		return "jpeg"
	case "image/png":
		return "png"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	default:
		return "jpeg"
	}
}

// FormatToMediaType converts format to MIME type
func FormatToMediaType(format string) string {
	switch format {
	case "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}
