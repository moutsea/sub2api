package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
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

func padText(targetChars int) string {
	unit := "The quick brown fox jumps over the lazy dog. "
	repeats := targetChars / len(unit)
	if repeats < 1 {
		repeats = 1
	}
	return strings.Repeat(unit, repeats)
}

// buildTools generates N fake tool definitions to simulate real Claude Code usage
func buildTools(count int) []map[string]any {
	tools := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		tools = append(tools, map[string]any{
			"toolSpecification": map[string]any{
				"name":        fmt.Sprintf("tool_%d", i),
				"description": fmt.Sprintf("This is tool number %d that performs various operations on the codebase including reading files, writing files, and executing commands. It supports multiple parameters and has complex input validation logic.", i),
				"inputSchema": map[string]any{
					"json": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"path":    map[string]any{"type": "string", "description": "The file path to operate on"},
							"content": map[string]any{"type": "string", "description": "The content to write or search for"},
							"options": map[string]any{"type": "object", "description": "Additional options for the operation"},
						},
						"required": []string{"path"},
					},
				},
			},
		})
	}
	return tools
}

// buildHistory generates alternating user/assistant history entries (no tool_use to avoid pairing issues)
func buildHistory(rounds int, charsPerMsg int) []map[string]any {
	history := make([]map[string]any, 0, rounds*2)
	for i := 0; i < rounds; i++ {
		// User message
		history = append(history, map[string]any{
			"messageId": fmt.Sprintf("msg-%03d", i*2),
			"userInputMessage": map[string]any{
				"content": fmt.Sprintf("User message %d. %s", i, padText(charsPerMsg)),
				"modelId": "claude-opus-4.6",
				"origin":  "AI_EDITOR",
			},
		})
		// Assistant message (text only, no tool_use)
		history = append(history, map[string]any{
			"messageId": fmt.Sprintf("msg-%03d", i*2+1),
			"assistantResponseMessage": map[string]any{
				"content": fmt.Sprintf("Assistant response %d. %s", i, padText(charsPerMsg)),
			},
		})
	}
	return history
}

// Test 1: Pure text (already done, just higher sizes)
func buildPureTextRequest(targetTokens int) ([]byte, int) {
	contentChars := targetTokens*4 - 500
	if contentChars < 100 {
		contentChars = 100
	}
	content := fmt.Sprintf("Say OK. Ignore padding:\n%s", padText(contentChars))

	body := map[string]any{
		"conversationState": map[string]any{
			"chatTriggerType": "MANUAL",
			"currentMessage": map[string]any{
				"userInputMessage": map[string]any{
					"content": content,
					"modelId": "claude-opus-4.6",
					"origin":  "AI_EDITOR",
				},
			},
			"conversationId": fmt.Sprintf("test-pure-%d", targetTokens),
		},
	}
	data, _ := json.Marshal(body)
	return data, len(data)
}

// Test 2: Realistic request with tools + history
func buildRealisticRequest(targetTokens int) ([]byte, int) {
	// Allocate budget: ~30% tools, ~60% history, ~10% current message
	toolTokens := targetTokens * 30 / 100
	historyTokens := targetTokens * 60 / 100
	currentTokens := targetTokens * 10 / 100

	// Tools: ~25 tools (like Claude Code)
	numTools := 25
	tools := buildTools(numTools)

	// History: distribute across rounds
	// Each round ~3 messages (user + assistant + tool_result), ~2k chars each
	charsPerMsg := 2000
	msgsPerRound := 3
	charsPerRound := charsPerMsg * msgsPerRound
	tokensPerRound := charsPerRound / 4
	numRounds := historyTokens / tokensPerRound
	if numRounds < 1 {
		numRounds = 1
	}
	// Adjust chars per msg to hit target
	if numRounds > 0 {
		charsPerMsg = (historyTokens * 4) / (numRounds * msgsPerRound)
	}

	history := buildHistory(numRounds, charsPerMsg)

	// Current message
	currentContent := fmt.Sprintf("Please help me fix this bug. %s", padText(currentTokens*4))

	// Build tools list for current message context
	toolItems := make([]map[string]any, 0, numTools)
	for _, t := range tools {
		toolItems = append(toolItems, t)
	}

	body := map[string]any{
		"conversationState": map[string]any{
			"chatTriggerType": "MANUAL",
			"currentMessage": map[string]any{
				"userInputMessage": map[string]any{
					"content": currentContent,
					"modelId": "claude-opus-4.6",
					"origin":  "AI_EDITOR",
					"userInputMessageContext": map[string]any{
						"tools": toolItems,
					},
				},
			},
			"conversationId": fmt.Sprintf("test-real-%d", targetTokens),
			"history":        history,
		},
	}

	data, _ := json.Marshal(body)
	_ = toolTokens // used in budget calculation
	return data, len(data)
}

func testEndpoint(ep endpoint, payload []byte) (int, string) {
	req, err := http.NewRequest("POST", ep.URL, strings.NewReader(string(payload)))
	if err != nil {
		return 0, fmt.Sprintf("request error: %v", err)
	}

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
	if len(respStr) > 400 {
		respStr = respStr[:400] + "..."
	}

	return resp.StatusCode, respStr
}

func main() {
	fmt.Println("=== Kiro Context Limit Test v2 ===")
	fmt.Printf("Token: %s...%s\n", accessToken[:20], accessToken[len(accessToken)-10:])
	fmt.Println()

	// Phase 1: Pure text - push beyond 200k
	fmt.Println("== Phase 1: Pure Text - find exact body size limit ==")
	for _, tokens := range []int{200, 202, 204, 205, 206, 208, 210} {
		targetTokens := tokens * 1000
		payload, payloadSize := buildPureTextRequest(targetTokens)
		fmt.Printf("--- %dk tokens | %d bytes (%.3f MB) ---\n", tokens, payloadSize, float64(payloadSize)/1024/1024)
		// Only test AWSQ (both endpoints have same limit)
		status, _ := testEndpoint(endpoints[0], payload)
		icon := "✓"
		if status >= 400 {
			icon = "✗"
		}
		fmt.Printf("  %s [AWSQ] HTTP %d\n", icon, status)
		time.Sleep(500 * time.Millisecond)
	}
	fmt.Println()

	fmt.Println("== Phase 2: Realistic - find exact body size limit ==")
	for _, tokens := range []int{385, 390, 392, 394, 396, 398, 400} {
		targetTokens := tokens * 1000
		payload, payloadSize := buildRealisticRequest(targetTokens)
		fmt.Printf("--- %dk tokens | %d bytes (%.3f MB) ---\n", tokens, payloadSize, float64(payloadSize)/1024/1024)
		status, _ := testEndpoint(endpoints[0], payload)
		icon := "✓"
		if status >= 400 {
			icon = "✗"
		}
		fmt.Printf("  %s [AWSQ] HTTP %d\n", icon, status)
		time.Sleep(500 * time.Millisecond)
	}
}
