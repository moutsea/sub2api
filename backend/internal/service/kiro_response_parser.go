package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

// KiroResponseParser parses Kiro/CodeWhisperer responses
type KiroResponseParser struct{}

// ParseResult represents parsed response result
type ParseResult struct {
	TextContent string
	ToolCalls   []ToolCall
}

// ToolCall represents a tool call in the response
type ToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

// ParseComplete parses a complete (non-streaming) Kiro response
func (p *KiroResponseParser) ParseComplete(data []byte) (*ParseResult, error) {
	// Use kiro package's parser
	completeResp := kiro.ParseCompleteResponse(data)
	if completeResp == nil {
		return &ParseResult{}, nil
	}

	result := &ParseResult{
		TextContent: completeResp.Text,
		ToolCalls:   make([]ToolCall, 0, len(completeResp.ToolCalls)),
	}

	// Convert tool calls
	for _, tc := range completeResp.ToolCalls {
		var args map[string]any
		if tc.ArgumentsRaw != "" {
			_ = json.Unmarshal([]byte(tc.ArgumentsRaw), &args)
		}
		if args == nil {
			args = make(map[string]any)
		}

		result.ToolCalls = append(result.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Name,
			Arguments: args,
		})
	}

	return result, nil
}

// DetectWebSearchToolCalls detects web_search tool calls in the response
func DetectWebSearchToolCalls(toolCalls []ToolCall) []WebSearchToolUse {
	var webSearchCalls []WebSearchToolUse
	for _, tc := range toolCalls {
		if IsWebSearchTool(tc.Name) {
			query := ""
			if q, ok := tc.Arguments["query"].(string); ok {
				query = q
			}
			webSearchCalls = append(webSearchCalls, WebSearchToolUse{
				ID:    tc.ID,
				Query: query,
			})
		}
	}
	return webSearchCalls
}

// HasOnlyWebSearchTools checks if only web_search tools are called (no other tools)
func HasOnlyWebSearchTools(toolCalls []ToolCall) bool {
	if len(toolCalls) == 0 {
		return false
	}
	for _, tc := range toolCalls {
		if !IsWebSearchTool(tc.Name) {
			return false
		}
	}
	return true
}

// WebSearchToolUse represents a detected web_search tool call
type WebSearchToolUse struct {
	ID    string
	Query string
}

// ExecuteWebSearch executes web_search and returns results
func ExecuteWebSearch(ctx context.Context, calls []WebSearchToolUse) []map[string]any {
	ws := GetWebSearchService()
	if ws == nil {
		return nil
	}

	var results []map[string]any
	for _, call := range calls {
		if call.Query == "" {
			continue
		}

		searchResult, err := ws.Search(ctx, call.Query)
		if err != nil {
			// Return error as tool_result
			results = append(results, map[string]any{
				"type":        "tool_result",
				"tool_use_id": call.ID,
				"content":     fmt.Sprintf("Search failed: %v", err),
				"is_error":    true,
			})
			continue
		}

		// Format search results
		formattedResult := FormatSearchResultsForModel(searchResult)
		results = append(results, map[string]any{
			"type":        "tool_result",
			"tool_use_id": call.ID,
			"content":     formattedResult,
		})
	}

	return results
}

// BuildFollowUpRequest builds a follow-up request with search results
func BuildFollowUpRequest(originalReq *kiro.ClaudeRequest, assistantContent []map[string]any, toolResults []map[string]any) *kiro.ClaudeRequest {
	// Deep copy original request using JSON marshal/unmarshal
	// This ensures all nested structures are properly copied
	originalJSON, err := json.Marshal(originalReq)
	if err != nil {
		// Fallback to shallow copy if marshal fails
		newReq := *originalReq
		newReq.Messages = make([]kiro.ClaudeMessage, len(originalReq.Messages))
		copy(newReq.Messages, originalReq.Messages)
		newReq.Messages = append(newReq.Messages,
			kiro.ClaudeMessage{Role: "assistant", Content: toAnySlice(assistantContent)},
			kiro.ClaudeMessage{Role: "user", Content: toAnySlice(toolResults)},
		)
		return &newReq
	}

	var newReq kiro.ClaudeRequest
	if err := json.Unmarshal(originalJSON, &newReq); err != nil {
		// Fallback to shallow copy if unmarshal fails
		newReq = *originalReq
		newReq.Messages = make([]kiro.ClaudeMessage, len(originalReq.Messages))
		copy(newReq.Messages, originalReq.Messages)
	}

	// Add assistant message (with tool_use)
	// Convert []map[string]any to []any for proper parsing by parseClaudeMessage
	assistantMsg := kiro.ClaudeMessage{
		Role:    "assistant",
		Content: toAnySlice(assistantContent),
	}
	newReq.Messages = append(newReq.Messages, assistantMsg)

	// Add user message (with tool_result)
	// Convert []map[string]any to []any for proper parsing by parseClaudeMessage
	userMsg := kiro.ClaudeMessage{
		Role:    "user",
		Content: toAnySlice(toolResults),
	}
	newReq.Messages = append(newReq.Messages, userMsg)

	return &newReq
}

// toAnySlice converts []map[string]any to []any
// This is needed because Go's type system doesn't allow direct assignment
// of []map[string]any to []any, but parseClaudeMessage expects []any
func toAnySlice(slice []map[string]any) []any {
	result := make([]any, len(slice))
	for i, v := range slice {
		result[i] = v
	}
	return result
}
