package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

const accessToken = "aoaAAAAAGmXHnssOe0u1_SVJsTKqPpj3kHvyrEiEKggiL3zlf4N0ZS66sGUh2RkOMa7AeLmny4qPlR9nvEGrvCYRgBkc0:MGUCMQCre3UF1lZi4HsH9U1v0p40o2Vw8mJFNvXrcDZboS4qZK0+KDxNu6/zpzE6bvSJVecCMF1bH60c2gXrTcCiFIO0AQnqdJUuHp/KqmAMcCwyjDHIsN0X1s/qVaiiP+WmBYmLMQ"

type endpoint struct {
	Name      string
	URL       string
	AmzTarget string
}

var endpoints = []endpoint{
	{
		Name: "AWSQ",
		URL:  "https://q.us-east-1.amazonaws.com/generateAssistantResponse",
	},
	{
		Name:      "CW",
		URL:       "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse",
		AmzTarget: "AmazonCodeWhispererStreamingService.GenerateAssistantResponse",
	},
}

func padText(n int) string {
	unit := "The quick brown fox jumps over the lazy dog. "
	repeats := n/len(unit) + 1
	return strings.Repeat(unit, repeats)[:n]
}

func sendRequest(ep endpoint, payload []byte) (int, string) {
	req, _ := http.NewRequest("POST", ep.URL, strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", "aws-sdk-js/1.0.27 ua/2.1 os/linux lang/js md/nodejs#22.12.0 api/codewhispererstreaming#1.0.27 m/E KiroIDE-1.6.0-test")
	req.Header.Set("x-amz-user-agent", "aws-sdk-js/1.0.27 KiroIDE-1.6.0-test")
	req.Header.Set("x-amzn-kiro-agent-mode", "vibe")
	req.Header.Set("x-amzn-codewhisperer-optout", "true")
	if ep.AmzTarget != "" {
		req.Header.Set("X-Amz-Target", ep.AmzTarget)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Sprintf("http error: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	respStr := string(bodyBytes)
	if len(respStr) > 500 {
		respStr = respStr[:500] + "..."
	}
	return resp.StatusCode, respStr
}

func main() {
	fmt.Println("=== Test: Truncated Request E2E ===")
	fmt.Println()

	// Build a large Claude request (40 turns, ~10KB per message)
	// This will produce a CW body > 800KB
	messages := make([]kiro.ClaudeMessage, 0)
	for i := 0; i < 40; i++ {
		messages = append(messages, kiro.ClaudeMessage{
			Role:    "user",
			Content: fmt.Sprintf("User turn %d: %s", i, padText(10000)),
		})
		messages = append(messages, kiro.ClaudeMessage{
			Role:    "assistant",
			Content: fmt.Sprintf("Assistant turn %d: %s", i, padText(10000)),
		})
	}
	// Final user message
	messages = append(messages, kiro.ClaudeMessage{
		Role:    "user",
		Content: "Say OK if you can read this.",
	})

	claudeReq := &kiro.ClaudeRequest{
		Model:    "claude-opus-4-6",
		Messages: messages,
		Tools: []kiro.ClaudeTool{
			{
				Name:        "read_file",
				Description: "Read a file from the filesystem",
				InputSchema: map[string]any{
					"type":       "object",
					"properties": map[string]any{"path": map[string]any{"type": "string"}},
					"required":   []string{"path"},
				},
			},
		},
	}

	// Transform without truncation
	cwOriginal, err := kiro.TransformClaudeToCodeWhisperer(claudeReq, "", nil)
	if err != nil {
		fmt.Printf("Transform failed: %v\n", err)
		return
	}
	originalBody, _ := json.Marshal(cwOriginal)
	fmt.Printf("Original CW body: %d bytes (%.1f KB)\n", len(originalBody), float64(len(originalBody))/1024)
	fmt.Printf("Original history entries: %d\n", len(cwOriginal.ConversationState.History))

	// Test 1: Send original (oversized) to see the 400
	fmt.Println("\n--- Test 1: Send ORIGINAL (oversized) ---")
	for _, ep := range endpoints {
		status, resp := sendRequest(ep, originalBody)
		icon := "✓"
		if status >= 400 {
			icon = "✗"
		}
		fmt.Printf("  %s [%s] HTTP %d | %s\n", icon, ep.Name, status, resp)
		time.Sleep(500 * time.Millisecond)
	}

	// Truncate to fit 800KB
	const maxBodySize = 800 * 1024
	fmt.Printf("\n--- Test 2: Truncate to fit %d bytes ---\n", maxBodySize)

	cwTruncated, truncatedBody, err := kiro.TruncateToFitBodySize(claudeReq, "", nil, maxBodySize)
	if err != nil {
		fmt.Printf("Truncation failed: %v\n", err)
		return
	}
	fmt.Printf("Truncated CW body: %d bytes (%.1f KB)\n", len(truncatedBody), float64(len(truncatedBody))/1024)
	fmt.Printf("Truncated history entries: %d\n", len(cwTruncated.ConversationState.History))

	// Test 2: Send truncated
	fmt.Println("\n--- Test 2: Send TRUNCATED ---")
	for _, ep := range endpoints {
		status, resp := sendRequest(ep, truncatedBody)
		icon := "✓"
		if status >= 400 {
			icon = "✗"
		}
		fmt.Printf("  %s [%s] HTTP %d | %s\n", icon, ep.Name, status, resp)
		time.Sleep(1 * time.Second)
	}
}
