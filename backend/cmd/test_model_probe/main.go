package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

type endpoint struct {
	Name      string
	URL       string
	Host      string
	AmzTarget string // empty for AWSQ
}

func main() {
	clientID := "FT_8xZJ0VkIHN0VfjD_9hnVzLWVhc3QtMQ"
	clientSecret := os.Getenv("KIRO_CLIENT_SECRET")
	refreshToken := os.Getenv("KIRO_REFRESH_TOKEN")

	// Allow env override
	if v := os.Getenv("KIRO_CLIENT_SECRET"); v != "" {
		clientSecret = v
	}
	if v := os.Getenv("KIRO_REFRESH_TOKEN"); v != "" {
		refreshToken = v
	}

	proxyStr := os.Getenv("HTTPS_PROXY")
	if proxyStr == "" {
		proxyStr = "http://127.0.0.1:7890"
	}
	proxyURL, _ := url.Parse(proxyStr)
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
	}

	// Step 1: Refresh token
	fmt.Println("=== Refreshing token ===")
	tokenURL := "https://oidc.us-east-1.amazonaws.com/token"
	tokenBody, _ := json.Marshal(map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"grantType":    "refresh_token",
		"refreshToken": refreshToken,
	})

	tokenReq, _ := http.NewRequest("POST", tokenURL, bytes.NewReader(tokenBody))
	tokenReq.Header.Set("Content-Type", "application/json")
	tokenResp, err := client.Do(tokenReq)
	if err != nil {
		fmt.Printf("Token refresh error: %v\n", err)
		os.Exit(1)
	}
	tokenRespBody, _ := io.ReadAll(tokenResp.Body)
	tokenResp.Body.Close()

	if tokenResp.StatusCode != 200 {
		fmt.Printf("Token refresh failed: %d %s\n", tokenResp.StatusCode, string(tokenRespBody))
		os.Exit(1)
	}

	var tr struct {
		AccessToken string `json:"accessToken"`
	}
	json.Unmarshal(tokenRespBody, &tr)
	fmt.Printf("Token OK, len=%d\n\n", len(tr.AccessToken))

	// Step 2: Get profileArn
	fmt.Println("=== Getting profileArn ===")
	profileReq, _ := http.NewRequest("GET",
		"https://q.us-east-1.amazonaws.com/getUsageLimits?isEmailRequired=true&origin=AI_EDITOR&resourceType=AGENTIC_REQUEST",
		nil)
	profileReq.Header.Set("Authorization", "Bearer "+tr.AccessToken)
	profileReq.Header.Set("User-Agent", "aws-sdk-js/1.0.27 ua/2.1 os/linux lang/js md/nodejs#22.12.0 api/codewhispererruntime#1.0.27 m/N,E KiroIDE-0.11.107-probe")
	profileReq.Header.Set("x-amz-user-agent", "aws-sdk-js/1.0.27 KiroIDE-0.11.107-probe")
	profileReq.Header.Set("Host", "q.us-east-1.amazonaws.com")

	profileResp, err := client.Do(profileReq)
	if err != nil {
		fmt.Printf("getUsageLimits error: %v\n", err)
		os.Exit(1)
	}
	profileBody, _ := io.ReadAll(profileResp.Body)
	profileResp.Body.Close()

	var usageResp map[string]any
	json.Unmarshal(profileBody, &usageResp)

	profileArn := ""
	if arn, ok := usageResp["profileArn"].(string); ok {
		profileArn = arn
	}
	fmt.Printf("profileArn from API: %q\n", profileArn)

	// Fallback to hardcoded if API didn't return one
	if profileArn == "" {
		profileArn = "arn:aws:codewhisperer:us-east-1:699475941385:profile/EHGA3GRVQMUK"
		fmt.Printf("Using fallback profileArn: %s\n", profileArn)
	}
	fmt.Println()

	// Step 3: Probe endpoints × models
	endpoints := []endpoint{
		{
			Name: "AWSQ",
			URL:  "https://q.us-east-1.amazonaws.com/generateAssistantResponse",
			Host: "q.us-east-1.amazonaws.com",
		},
		{
			Name:      "CodeWhisperer",
			URL:       "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse",
			Host:      "codewhisperer.us-east-1.amazonaws.com",
			AmzTarget: "AmazonCodeWhispererStreamingService.GenerateAssistantResponse",
		},
	}

	modelsToTest := []string{
		"claude-opus-4.6",     // known working baseline
		"claude-opus-4.7",     // candidate: dot format
		"claude-opus-4-7",     // candidate: dash format (unlikely for CW)
		"claude-opus-4.7[1m]", // candidate: 1m variant
	}

	fmt.Println("=== Probing endpoints × models ===")
	fmt.Println()

	for _, ep := range endpoints {
		fmt.Printf("━━━ %s (%s) ━━━\n", ep.Name, ep.URL)
		for _, modelID := range modelsToTest {
			fmt.Printf("  [%s] modelId=%s ... ", ep.Name, modelID)

			cwReq := map[string]any{
				"conversationState": map[string]any{
					"currentMessage": map[string]any{
						"userInputMessage": map[string]any{
							"content": "hi",
							"modelId": modelID,
							"origin":  "IDE",
						},
					},
					"chatTriggerType": "MANUAL",
					"conversationId": fmt.Sprintf("probe-%d", time.Now().UnixMilli()),
				},
			}
			if profileArn != "" {
				cwReq["profileArn"] = profileArn
			}

			body, _ := json.Marshal(cwReq)

			req, _ := http.NewRequest("POST", ep.URL, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+tr.AccessToken)
			req.Header.Set("Accept", "text/event-stream")
			req.Header.Set("User-Agent", "aws-sdk-js/1.0.27 ua/2.1 os/linux lang/js md/nodejs#22.12.0 api/codewhispererstreaming#1.0.27 m/E KiroIDE-0.11.107-probe")
			req.Header.Set("x-amz-user-agent", "aws-sdk-js/1.0.27 KiroIDE-0.11.107-probe")
			req.Header.Set("x-amzn-kiro-agent-mode", "vibe")
			req.Header.Set("x-amzn-codewhisperer-optout", "true")
			req.Header.Set("Host", ep.Host)
			if ep.AmzTarget != "" {
				req.Header.Set("X-Amz-Target", ep.AmzTarget)
			}

			resp, err := client.Do(req)
			if err != nil {
				fmt.Printf("ERROR: %v\n", err)
				continue
			}
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			resp.Body.Close()

			if resp.StatusCode == 200 {
				fmt.Printf("✓ OK (200) body_len=%d\n", len(respBody))
			} else {
				// Truncate error body for readability
				errStr := string(respBody)
				if len(errStr) > 200 {
					errStr = errStr[:200] + "..."
				}
				fmt.Printf("✗ FAIL (%d) %s\n", resp.StatusCode, errStr)
			}

			time.Sleep(500 * time.Millisecond) // gentle rate limiting
		}
		fmt.Println()
	}

	fmt.Println("=== Probe complete ===")
}
