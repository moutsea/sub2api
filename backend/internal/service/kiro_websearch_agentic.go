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

	// ginKeyWebSearchEvents is the gin.Context key for storing web search events
	ginKeyWebSearchEvents = "web_search_events"
)

// WebSearchEvent records a single web search execution for response injection
type WebSearchEvent struct {
	ID      string              // tool_use ID (server_tool_use)
	Query   string              // search query
	Results []WebSearchResultItem // search results
}

// SetWebSearchEvents stores web search events on gin.Context for response injection
func SetWebSearchEvents(c *gin.Context, events []WebSearchEvent) {
	c.Set(ginKeyWebSearchEvents, events)
}

// GetWebSearchEvents retrieves web search events from gin.Context
func GetWebSearchEvents(c *gin.Context) []WebSearchEvent {
	val, exists := c.Get(ginKeyWebSearchEvents)
	if !exists {
		return nil
	}
	events, _ := val.([]WebSearchEvent)
	return events
}

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

			// Remove WebSearch tool_use blocks, convert to text to preserve message structure
			if blockType == "tool_use" {
				name, _ := blockMap["name"].(string)
				if IsWebSearchTool(name) {
					id, _ := blockMap["id"].(string)
					if id != "" {
						removedToolUseIDs[id] = true
					}
					query := ""
					if input, ok := blockMap["input"].(map[string]any); ok {
						query, _ = input["query"].(string)
					}
					if query != "" {
						filteredContent = append(filteredContent, map[string]any{
							"type": "text",
							"text": fmt.Sprintf("<web_search_query>%s</web_search_query>", query),
						})
					}
					continue
				}
			}

			// Convert tool_result blocks (whose tool_use was removed) to text blocks
			// to preserve search results in context
			if blockType == "tool_result" {
				toolUseID, _ := blockMap["tool_use_id"].(string)
				if removedToolUseIDs[toolUseID] {
					text := extractToolResultText(blockMap["content"])
					if text != "" {
						filteredContent = append(filteredContent, map[string]any{
							"type": "text",
							"text": fmt.Sprintf("<web_search_results>\n%s</web_search_results>", text),
						})
					}
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

// extractToolResultText extracts text content from a tool_result content field.
// Content can be a string or []any of content blocks.
func extractToolResultText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, item := range c {
			itemMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := itemMap["type"].(string); t == "text" {
				if text, ok := itemMap["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	if content != nil {
		return fmt.Sprintf("%v", content)
	}
	return ""
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
	var allSearchEvents []WebSearchEvent

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
			if iteration == 1 {
				// First iteration: no web_search detected, use original body directly
				// Avoids re-executing request and potential message structure corruption from filterWebSearchTools
				return s.Forward(ctx, c, account, filterWebSearchTools(claudeReq))
			}
			// After web_search loop: need to filter and re-execute with stream setting restored
			SetWebSearchEvents(c, allSearchEvents)
			finalReq := *currentReq
			finalReq.Stream = claudeReq.Stream
			return s.Forward(ctx, c, account, filterWebSearchTools(&finalReq))
		}

		// Execute web_search
		toolResults, searchEvents := ExecuteWebSearch(ctx, webSearchCalls)
		allSearchEvents = append(allSearchEvents, searchEvents...)
		if len(toolResults) == 0 {
			if iteration == 1 {
				return s.Forward(ctx, c, account, filterWebSearchTools(claudeReq))
			}
			finalReq := *currentReq
			finalReq.Stream = claudeReq.Stream
			return s.Forward(ctx, c, account, filterWebSearchTools(&finalReq))
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
	SetWebSearchEvents(c, allSearchEvents)
	finalReq := *currentReq
	finalReq.Stream = claudeReq.Stream
	return s.Forward(ctx, c, account, filterWebSearchTools(&finalReq))
}

// forwardStreamWithWebSearch handles streaming requests with web_search agentic loop
// Strategy: execute non-streaming first to detect web_search, then stream the final response
func (s *KiroGatewayService) forwardStreamWithWebSearch(ctx context.Context, c *gin.Context, account *Account, claudeReq *kiro.ClaudeRequest, prefix string) (*ForwardResult, error) {
	currentReq := claudeReq
	iteration := 0
	parser := &KiroResponseParser{}
	var allSearchEvents []WebSearchEvent

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
			if iteration == 1 {
				// First iteration: no web_search, use original request with streaming
				streamReq := *claudeReq
				streamReq.Stream = true
				return s.Forward(ctx, c, account, filterWebSearchTools(&streamReq))
			}
			SetWebSearchEvents(c, allSearchEvents)
			streamReq := *currentReq
			streamReq.Stream = true
			return s.Forward(ctx, c, account, filterWebSearchTools(&streamReq))
		}

		// Execute web_search
		toolResults, searchEvents := ExecuteWebSearch(ctx, webSearchCalls)
		allSearchEvents = append(allSearchEvents, searchEvents...)
		if len(toolResults) == 0 {
			if iteration == 1 {
				streamReq := *claudeReq
				streamReq.Stream = true
				return s.Forward(ctx, c, account, filterWebSearchTools(&streamReq))
			}
			SetWebSearchEvents(c, allSearchEvents)
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
	SetWebSearchEvents(c, allSearchEvents)
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
	endpoint := fmt.Sprintf("https://q.%s.amazonaws.com/generateAssistantResponse", region)

	// Generate machine ID for User-Agent headers
	machineID := kiro.GenerateMachineID(account.GetKiroRefreshToken())
	kiroVersion := "1.6.0"
	awsHost := fmt.Sprintf("q.%s.amazonaws.com", region)

	// Create HTTP request
	upstreamReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(cwReqBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Set headers
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Authorization", "Bearer "+accessToken)
	upstreamReq.Header.Set("Accept", "text/event-stream")
	upstreamReq.Header.Set("User-Agent", fmt.Sprintf("aws-sdk-js/1.0.27 ua/2.1 os/linux lang/js md/nodejs#22.12.0 api/codewhispererstreaming#1.0.27 m/E KiroIDE-%s-%s", kiroVersion, machineID))
	upstreamReq.Header.Set("x-amz-user-agent", fmt.Sprintf("aws-sdk-js/1.0.27 KiroIDE-%s-%s", kiroVersion, machineID))
	upstreamReq.Header.Set("x-amzn-kiro-agent-mode", "vibe")
	upstreamReq.Header.Set("x-amzn-codewhisperer-optout", "true")
	upstreamReq.Header.Set("Host", awsHost)
	upstreamReq.Header.Set("Connection", "close")
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
