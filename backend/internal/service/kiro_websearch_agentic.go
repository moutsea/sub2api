package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	// MaxWebSearchIterations maximum web_search iteration count
	MaxWebSearchIterations = 3
)

// filterWebSearchTools removes Claude's built-in web_search tools from a ClaudeRequest.
// It filters both the Tools list AND any WebSearch tool_use/tool_result content blocks
// from Messages, which are left behind by the agentic loop. Without this cleanup,
// CodeWhisperer returns 400 because tool_use references a tool not in the tools list.
func filterWebSearchTools(req *kiro.ClaudeRequest) []byte {
	// 1. Filter tools list (existing logic)
	filteredTools := make([]kiro.ClaudeTool, 0, len(req.Tools))
	for _, tool := range req.Tools {
		if isClaudeBuiltinWebSearch(tool) {
			continue
		}
		filteredTools = append(filteredTools, tool)
	}

	// 2. Clean WebSearch tool_use/tool_result from messages
	removedToolUseIDs := make(map[string]bool)
	filteredMessages := make([]kiro.ClaudeMessage, 0, len(req.Messages))

	for _, msg := range req.Messages {
		contentSlice, ok := msg.Content.([]any)
		if !ok {
			// Content is string or other non-array type, keep as-is
			filteredMessages = append(filteredMessages, msg)
			continue
		}

		var filteredContent []any
		for _, block := range contentSlice {
			blockMap, ok := block.(map[string]any)
			if !ok {
				filteredContent = append(filteredContent, block)
				continue
			}

			blockType, _ := blockMap["type"].(string)

			// Remove WebSearch tool_use blocks
			if blockType == "tool_use" {
				name, _ := blockMap["name"].(string)
				if IsWebSearchTool(name) {
					id, _ := blockMap["id"].(string)
					if id != "" {
						removedToolUseIDs[id] = true
					}
					continue
				}
			}

			// Remove tool_result blocks whose tool_use was removed
			if blockType == "tool_result" {
				toolUseID, _ := blockMap["tool_use_id"].(string)
				if removedToolUseIDs[toolUseID] {
					continue
				}
			}

			filteredContent = append(filteredContent, block)
		}

		// Skip messages that became empty after filtering
		if len(filteredContent) == 0 {
			continue
		}

		filteredMessages = append(filteredMessages, kiro.ClaudeMessage{
			Role:    msg.Role,
			Content: filteredContent,
		})
	}

	filteredReq := *req
	filteredReq.Tools = filteredTools
	filteredReq.Messages = filteredMessages
	body, _ := json.Marshal(filteredReq)
	return body
}

// isClaudeBuiltinWebSearch checks if the tool is Claude's built-in web_search
// Claude's built-in web_search has Type field like "web_search_20250305"
// MCP tools have Type="" even if named "web_search"
func isClaudeBuiltinWebSearch(tool kiro.ClaudeTool) bool {
	if tool.Type == "" {
		return false
	}
	return tool.Type == "web_search" ||
		tool.Type == "web_search_20250305" ||
		strings.HasPrefix(tool.Type, "web_search_")
}

// ForwardWithWebSearch handles Claude API requests with web_search agentic loop support
func (s *KiroGatewayService) ForwardWithWebSearch(ctx context.Context, c *gin.Context, account *Account, body []byte, claudeReq *kiro.ClaudeRequest) (*ForwardResult, error) {
	prefix := fmt.Sprintf("[kiro-WebSearch] account=%s", account.Name)

	// Check if WebSearch is enabled
	if !IsWebSearchEnabled() {
		// WebSearch not enabled, filter out Claude's built-in web_search tools and use normal forward
		return s.Forward(ctx, c, account, filterWebSearchTools(claudeReq))
	}

	// Check if request has Claude's built-in web_search tool (Type field like "web_search_20250305")
	// MCP tools named "web_search" are NOT detected here - they are regular tools
	hasBuiltinWebSearch := false
	for _, tool := range claudeReq.Tools {
		if isClaudeBuiltinWebSearch(tool) {
			hasBuiltinWebSearch = true
			break
		}
	}

	if !hasBuiltinWebSearch {
		// No Claude built-in web_search tool, use normal forward (no filtering needed)
		return s.Forward(ctx, c, account, body)
	}

	log.Printf("%s Claude built-in web_search tool detected, starting agentic loop", prefix)

	// For streaming requests, we need to handle differently
	if claudeReq.Stream {
		return s.forwardStreamWithWebSearch(ctx, c, account, claudeReq, prefix)
	}

	// Non-streaming: agentic loop
	return s.forwardNonStreamWithWebSearch(ctx, c, account, claudeReq, prefix)
}

// forwardNonStreamWithWebSearch handles non-streaming requests with web_search agentic loop
func (s *KiroGatewayService) forwardNonStreamWithWebSearch(ctx context.Context, c *gin.Context, account *Account, claudeReq *kiro.ClaudeRequest, prefix string) (*ForwardResult, error) {
	currentReq := claudeReq
	iteration := 0
	parser := &KiroResponseParser{}

	for iteration < MaxWebSearchIterations {
		iteration++

		// Check if context is cancelled (client disconnected)
		select {
		case <-ctx.Done():
			log.Printf("%s iteration=%d context cancelled: %v", prefix, iteration, ctx.Err())
			return nil, ctx.Err()
		default:
		}

		// Execute request (non-streaming)
		nonStreamReq := *currentReq
		nonStreamReq.Stream = false

		// Marshal request
		reqBody, err := json.Marshal(nonStreamReq)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}

		// Execute CodeWhisperer request
		resp, err := s.executeCodeWhispererRequest(ctx, c, account, reqBody, &nonStreamReq)
		if err != nil {
			return nil, err
		}

		// Read response body
		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read response: %w", err)
		}

		// Parse response
		parseResult, err := parser.ParseComplete(respBody)
		if err != nil {
			return nil, fmt.Errorf("parse response: %w", err)
		}

		// Detect web_search tool calls
		webSearchCalls := DetectWebSearchToolCalls(parseResult.ToolCalls)

		// If no web_search calls, or has other tools, return final response
		if len(webSearchCalls) == 0 || !HasOnlyWebSearchTools(parseResult.ToolCalls) {
			// Return final response by re-executing with original stream setting, filtering out web_search tools
			return s.Forward(ctx, c, account, filterWebSearchTools(&nonStreamReq))
		}

		// Execute web_search
		toolResults := ExecuteWebSearch(ctx, webSearchCalls)
		if len(toolResults) == 0 {
			return s.Forward(ctx, c, account, filterWebSearchTools(&nonStreamReq))
		}

		// Build assistant content
		// Note: Kiro returns tool name as "WebSearch" but we need to use the original Claude tool name
		var assistantContent []map[string]any
		if parseResult.TextContent != "" {
			assistantContent = append(assistantContent, map[string]any{
				"type": "text",
				"text": parseResult.TextContent,
			})
		}
		for _, tc := range parseResult.ToolCalls {
			// Keep tool name as-is (e.g., "WebSearch") to match tools list
			assistantContent = append(assistantContent, map[string]any{
				"type":  "tool_use",
				"id":    tc.ID,
				"name":  tc.Name,
				"input": tc.Arguments,
			})
		}

		// Build follow-up request
		currentReq = BuildFollowUpRequest(currentReq, assistantContent, toolResults)
	}

	// Max iterations reached
	return s.Forward(ctx, c, account, filterWebSearchTools(currentReq))
}

// forwardStreamWithWebSearch handles streaming requests with web_search agentic loop
// Strategy: execute non-streaming first to detect web_search, then stream the final response
func (s *KiroGatewayService) forwardStreamWithWebSearch(ctx context.Context, c *gin.Context, account *Account, claudeReq *kiro.ClaudeRequest, prefix string) (*ForwardResult, error) {
	currentReq := claudeReq
	iteration := 0
	parser := &KiroResponseParser{}

	for iteration < MaxWebSearchIterations {
		iteration++

		// Check if context is cancelled (client disconnected)
		select {
		case <-ctx.Done():
			log.Printf("%s stream iteration=%d context cancelled: %v", prefix, iteration, ctx.Err())
			return nil, ctx.Err()
		default:
		}

		// Execute as non-streaming to detect web_search
		nonStreamReq := *currentReq
		nonStreamReq.Stream = false

		reqBody, err := json.Marshal(nonStreamReq)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}

		// Execute CodeWhisperer request
		resp, err := s.executeCodeWhispererRequest(ctx, c, account, reqBody, &nonStreamReq)
		if err != nil {
			log.Printf("%s stream iteration=%d execute error: %v", prefix, iteration, err)
			return nil, err
		}

		// Read response body
		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read response: %w", err)
		}

		// Parse response
		parseResult, err := parser.ParseComplete(respBody)
		if err != nil {
			return nil, fmt.Errorf("parse response: %w", err)
		}

		// Detect web_search tool calls
		webSearchCalls := DetectWebSearchToolCalls(parseResult.ToolCalls)

		// If no web_search calls, or has other tools, switch to streaming
		if len(webSearchCalls) == 0 || !HasOnlyWebSearchTools(parseResult.ToolCalls) {
			// Re-execute with streaming enabled, filtering out web_search tools
			streamReq := *currentReq
			streamReq.Stream = true
			return s.Forward(ctx, c, account, filterWebSearchTools(&streamReq))
		}

		// Execute web_search
		toolResults := ExecuteWebSearch(ctx, webSearchCalls)
		if len(toolResults) == 0 {
			streamReq := *currentReq
			streamReq.Stream = true
			return s.Forward(ctx, c, account, filterWebSearchTools(&streamReq))
		}

		// Build assistant content
		var assistantContent []map[string]any
		if parseResult.TextContent != "" {
			assistantContent = append(assistantContent, map[string]any{
				"type": "text",
				"text": parseResult.TextContent,
			})
		}
		for _, tc := range parseResult.ToolCalls {
			// Keep tool name as-is (e.g., "WebSearch") to match tools list
			assistantContent = append(assistantContent, map[string]any{
				"type":  "tool_use",
				"id":    tc.ID,
				"name":  tc.Name,
				"input": tc.Arguments,
			})
		}

		// Build follow-up request
		currentReq = BuildFollowUpRequest(currentReq, assistantContent, toolResults)
	}

	// Max iterations reached, return streaming response
	streamReq := *currentReq
	streamReq.Stream = true
	return s.Forward(ctx, c, account, filterWebSearchTools(&streamReq))
}

// executeCodeWhispererRequest executes a CodeWhisperer request
func (s *KiroGatewayService) executeCodeWhispererRequest(ctx context.Context, c *gin.Context, account *Account, reqBody []byte, claudeReq *kiro.ClaudeRequest) (*http.Response, error) {
	// Get access token
	accessToken, err := s.tokenProvider.GetAccessToken(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("get access_token failed: %w", err)
	}

	// Get profile ARN
	profileArn := account.GetKiroProfileArn()

	// Transform to CodeWhisperer format
	// Note: web_search tools are converted to standard toolSpecification format in TransformClaudeToCodeWhisperer
	cwReq, err := kiro.TransformClaudeToCodeWhisperer(claudeReq, profileArn, c)
	if err != nil {
		return nil, fmt.Errorf("transform request: %w", err)
	}

	// Marshal CodeWhisperer request
	cwReqBody, err := json.Marshal(cwReq)
	if err != nil {
		return nil, fmt.Errorf("marshal cw request: %w", err)
	}

	// Build endpoint
	region := account.GetKiroRegion()
	endpoint := fmt.Sprintf("https://codewhisperer.%s.amazonaws.com/generateAssistantResponse", region)

	// Create HTTP request
	upstreamReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(cwReqBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Set headers
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Authorization", "Bearer "+accessToken)
	upstreamReq.Header.Set("Accept", "text/event-stream")
	upstreamReq.Header.Set("User-Agent", "aws-sdk-js/3.738.0 ua/2.1 os/deno lang/ts KiroGateway")
	upstreamReq.Header.Set("x-amz-user-agent", "aws-sdk-js/3.738.0 KiroGateway")
	upstreamReq.Header.Set("x-amzn-kiro-agent-mode", "spec")
	upstreamReq.Header.Set("x-amzn-codewhisperer-optout", "true")
	upstreamReq.Header.Set("amz-sdk-invocation-id", uuid.New().String())
	upstreamReq.Header.Set("amz-sdk-request", "attempt=1; max=3")

	// Get proxy URL
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	// Execute request
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return nil, fmt.Errorf("upstream request failed: %w", err)
	}

	// Check status
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		log.Printf("[kiro-WebSearch] upstream error status=%d response=%s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("upstream returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return resp, nil
}
