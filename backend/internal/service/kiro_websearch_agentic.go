package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	// MaxWebSearchIterations maximum web_search iteration count
	MaxWebSearchIterations = 3
)

// ForwardWithWebSearch handles Claude API requests with web_search agentic loop support
func (s *KiroGatewayService) ForwardWithWebSearch(ctx context.Context, c *gin.Context, account *Account, body []byte, claudeReq *kiro.ClaudeRequest) (*ForwardResult, error) {
	prefix := fmt.Sprintf("[kiro-WebSearch] account=%s", account.Name)

	// Check if WebSearch is enabled
	if !IsWebSearchEnabled() {
		// WebSearch not enabled, use normal forward
		return s.Forward(ctx, c, account, body)
	}

	// Check if request has web_search tool
	hasWebSearchTool := false
	for _, tool := range claudeReq.Tools {
		if IsWebSearchTool(tool.Name) || IsWebSearchTool(tool.Type) {
			hasWebSearchTool = true
			break
		}
	}

	if !hasWebSearchTool {
		// No web_search tool, use normal forward
		return s.Forward(ctx, c, account, body)
	}

	log.Printf("%s web_search tool detected, starting agentic loop", prefix)

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
			log.Printf("%s completed after %d iterations", prefix, iteration)
			// Return final response by re-executing with original stream setting
			return s.Forward(ctx, c, account, reqBody)
		}

		// Execute web_search
		toolResults := ExecuteWebSearch(ctx, webSearchCalls)
		if len(toolResults) == 0 {
			log.Printf("%s web_search failed, returning current response", prefix)
			return s.Forward(ctx, c, account, reqBody)
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
			// Map Kiro tool name back to Claude tool name
			toolName := tc.Name
			if IsWebSearchTool(tc.Name) {
				toolName = "web_search" // Use original Claude tool name
			}
			assistantContent = append(assistantContent, map[string]any{
				"type":  "tool_use",
				"id":    tc.ID,
				"name":  toolName,
				"input": tc.Arguments,
			})
		}

		// Build follow-up request
		currentReq = BuildFollowUpRequest(currentReq, assistantContent, toolResults)
	}

	// Max iterations reached
	log.Printf("%s max iterations reached (%d), returning final response", prefix, MaxWebSearchIterations)
	reqBody, _ := json.Marshal(currentReq)
	return s.Forward(ctx, c, account, reqBody)
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
			log.Printf("%s switching to streaming after %d iterations", prefix, iteration)
			// Re-execute with streaming enabled
			streamReq := *currentReq
			streamReq.Stream = true
			streamBody, _ := json.Marshal(streamReq)
			return s.Forward(ctx, c, account, streamBody)
		}

		// Execute web_search
		toolResults := ExecuteWebSearch(ctx, webSearchCalls)
		if len(toolResults) == 0 {
			log.Printf("%s web_search failed, switching to streaming", prefix)
			streamReq := *currentReq
			streamReq.Stream = true
			streamBody, _ := json.Marshal(streamReq)
			return s.Forward(ctx, c, account, streamBody)
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
			// Map Kiro tool name back to Claude tool name
			toolName := tc.Name
			if IsWebSearchTool(tc.Name) {
				toolName = "web_search" // Use original Claude tool name
			}
			assistantContent = append(assistantContent, map[string]any{
				"type":  "tool_use",
				"id":    tc.ID,
				"name":  toolName,
				"input": tc.Arguments,
			})
		}

		// Build follow-up request
		currentReq = BuildFollowUpRequest(currentReq, assistantContent, toolResults)
	}

	// Max iterations reached, return streaming response
	log.Printf("%s max iterations reached, returning streaming response", prefix)
	streamReq := *currentReq
	streamReq.Stream = true
	streamBody, _ := json.Marshal(streamReq)
	return s.Forward(ctx, c, account, streamBody)
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
		return nil, fmt.Errorf("upstream returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return resp, nil
}
