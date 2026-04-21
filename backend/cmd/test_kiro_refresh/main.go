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

const (
	refreshToken = `aorAAAAAGpEwu0gB6221EyjsOcuescIVn2oZbJV2RRBLSmhW47iYV93hb1obRTdBJCR-WdvhYaJKUxJsbXkOAvAwYBkc0:MGUCMQCP0/eY6aJ9ZJKCS2/78s8rh/r8wlEreWdhsXToA3Cv0vMMtnAe/a8Qg/i6iViQiMsCMG2eXQ9a0i9JAPpQ7KwSSl+rw5mDhaM0gjCdzwUgJDrDEHnRS+rsjX5GnSEY+32dwA`
	clientID     = `r1w6mfYNtYI7zNxjO-ZQPXVzLWVhc3QtMQ`
)

// clientSecret via os.Args[1] or hardcoded below — too long for const
var clientSecret string

func main() {
	// Allow override via arg, otherwise use hardcoded
	if len(os.Args) > 1 {
		clientSecret = os.Args[1]
	}
	if clientSecret == "" {
		// Paste the full clientSecret here
		clientSecret = "eyJraWQiOiJrZXktMTU2NDAyODA5OSIsImFsZyI6IkhTMzg0In0.eyJzZXJpYWxpemVkIjoie1wiY2xpZW50SWRcIjp7XCJ2YWx1ZVwiOlwicjF3Nm1mWU50WUk3ek54ak8tWlFQWFZ6TFdWaGMzUXRNUVwifSxcImlkZW1wb3RlbnRLZXlcIjpudWxsLFwidGVuYW50SWRcIjpudWxsLFwiY2xpZW50TmFtZVwiOlwiQW1hem9uIFEgRGV2ZWxvcGVyIGZvciBjb21tYW5kIGxpbmVcIixcImJhY2tmaWxsVmVyc2lvblwiOm51bGwsXCJjbGllbnRUeXBlXCI6XCJQVUJMSUNcIixcInRlbXBsYXRlQXJuXCI6bnVsbCxcInRlbXBsYXRlQ29udGV4dFwiOm51bGwsXCJleHBpcmF0aW9uVGltZXN0YW1wXCI6MTc4Mjg5MTE5My4wNzk5MjI1MTksXCJjcmVhdGVkVGltZXN0YW1wXCI6MTc3NTExNTE5My4wNzk5MjI1MTksXCJ1cGRhdGVkVGltZXN0YW1wXCI6MTc3NTExNTE5My4wNzk5MjI1MTksXCJjcmVhdGVkQnlcIjpudWxsLFwidXBkYXRlZEJ5XCI6bnVsbCxcInN0YXR1c1wiOm51bGwsXCJpbml0aWF0ZUxvZ2luVXJpXCI6bnVsbCxcImVudGl0bGVkUmVzb3VyY2VJZFwiOm51bGwsXCJlbnRpdGxlZFJlc291cmNlQ29udGFpbmVySWRcIjpudWxsLFwiZXh0ZXJuYWxJZFwiOm51bGwsXCJzb2Z0d2FyZUlkXCI6bnVsbCxcInNjb3Blc1wiOlt7XCJmdWxsU2NvcGVcIjpcImNvZGV3aGlzcGVyZXI6Y29tcGxldGlvbnNcIixcInN0YXR1c1wiOlwiSU5JVElBTFwiLFwiYXBwbGljYXRpb25Bcm5cIjpudWxsLFwiZnJpZW5kbHlJZFwiOlwiY29kZXdoaXNwZXJlclwiLFwidXNlQ2FzZUFjdGlvblwiOlwiY29tcGxldGlvbnNcIixcInNjb3BlVHlwZVwiOlwiQUNDRVNTX1NDT1BFXCIsXCJ0eXBlXCI6XCJJbW11dGFibGVBY2Nlc3NTY29wZVwifSx7XCJmdWxsU2NvcGVcIjpcImNvZGV3aGlzcGVyZXI6YW5hbHlzaXNcIixcInN0YXR1c1wiOlwiSU5JVElBTFwiLFwiYXBwbGljYXRpb25Bcm5cIjpudWxsLFwiZnJpZW5kbHlJZFwiOlwiY29kZXdoaXNwZXJlclwiLFwidXNlQ2FzZUFjdGlvblwiOlwiYW5hbHlzaXNcIixcInNjb3BlVHlwZVwiOlwiQUNDRVNTX1NDT1BFXCIsXCJ0eXBlXCI6XCJJbW11dGFibGVBY2Nlc3NTY29wZVwifSx7XCJmdWxsU2NvcGVcIjpcImNvZGV3aGlzcGVyZXI6Y29udmVyc2F0aW9uc1wiLFwic3RhdHVzXCI6XCJJTklUSUFMXCIsXCJhcHBsaWNhdGlvbkFyblwiOm51bGwsXCJmcmllbmRseUlkXCI6XCJjb2Rld2hpc3BlcmVyXCIsXCJ1c2VDYXNlQWN0aW9uXCI6XCJjb252ZXJzYXRpb25zXCIsXCJzY29wZVR5cGVcIjpcIkFDQ0VTU19TQ09QRVwiLFwidHlwZVwiOlwiSW1tdXRhYmxlQWNjZXNzU2NvcGVcIn1dLFwiYXV0aGVudGljYXRpb25Db25maWd1cmF0aW9uXCI6bnVsbCxcInNoYWRvd0F1dGhlbnRpY2F0aW9uQ29uZmlndXJhdGlvblwiOm51bGwsXCJlbmFibGVkR3JhbnRzXCI6bnVsbCxcImVuZm9yY2VBdXRoTkNvbmZpZ3VyYXRpb25cIjpudWxsLFwib3duZXJBY2NvdW50SWRcIjpudWxsLFwic3NvSW5zdGFuY2VBY2NvdW50SWRcIjpudWxsLFwidXNlckNvbnNlbnRcIjpudWxsLFwibm9uSW50ZXJhY3RpdmVTZXNzaW9uc0VuYWJsZWRcIjpudWxsLFwiYXNzb2NpYXRlZEluc3RhbmNlQXJuXCI6bnVsbCxcImdyb3VwU2NvcGVzQnlGcmllbmRseUlkXCI6e1wiY29kZXdoaXNwZXJlclwiOlt7XCJmdWxsU2NvcGVcIjpcImNvZGV3aGlzcGVyZXI6Y29udmVyc2F0aW9uc1wiLFwic3RhdHVzXCI6XCJJTklUSUFMXCIsXCJhcHBsaWNhdGlvbkFyblwiOm51bGwsXCJmcmllbmRseUlkXCI6XCJjb2Rld2hpc3BlcmVyXCIsXCJ1c2VDYXNlQWN0aW9uXCI6XCJjb252ZXJzYXRpb25zXCIsXCJzY29wZVR5cGVcIjpcIkFDQ0VTU19TQ09QRVwiLFwidHlwZVwiOlwiSW1tdXRhYmxlQWNjZXNzU2NvcGVcIn0se1wiZnVsbFNjb3BlXCI6XCJjb2Rld2hpc3BlcmVyOmFuYWx5c2lzXCIsXCJzdGF0dXNcIjpcIklOSVRJQUxcIixcImFwcGxpY2F0aW9uQXJuXCI6bnVsbCxcImZyaWVuZGx5SWRcIjpcImNvZGV3aGlzcGVyZXJcIixcInVzZUNhc2VBY3Rpb25cIjpcImFuYWx5c2lzXCIsXCJzY29wZVR5cGVcIjpcIkFDQ0VTU19TQ09QRVwiLFwidHlwZVwiOlwiSW1tdXRhYmxlQWNjZXNzU2NvcGVcIn0se1wiZnVsbFNjb3BlXCI6XCJjb2Rld2hpc3BlcmVyOmNvbXBsZXRpb25zXCIsXCJzdGF0dXNcIjpcIklOSVRJQUxcIixcImFwcGxpY2F0aW9uQXJuXCI6bnVsbCxcImZyaWVuZGx5SWRcIjpcImNvZGV3aGlzcGVyZXJcIixcInVzZUNhc2VBY3Rpb25cIjpcImNvbXBsZXRpb25zXCIsXCJzY29wZVR5cGVcIjpcIkFDQ0VTU19TQ09QRVwiLFwidHlwZVwiOlwiSW1tdXRhYmxlQWNjZXNzU2NvcGVcIn1dfSxcInNob3VsZEdldFZhbHVlRnJvbVRlbXBsYXRlXCI6dHJ1ZSxcImhhc1JlcXVlc3RlZFNjb3Blc1wiOmZhbHNlLFwiY29udGFpbnNPbmx5U3NvU2NvcGVzXCI6ZmFsc2UsXCJzc29TY29wZXNcIjpbXSxcImlzVjFCYWNrZmlsbGVkXCI6ZmFsc2UsXCJpc1YyQmFja2ZpbGxlZFwiOmZhbHNlLFwiaXNWM0JhY2tmaWxsZWRcIjpmYWxzZSxcImlzVjRCYWNrZmlsbGVkXCI6ZmFsc2UsXCJpc0V4cGlyZWRcIjpmYWxzZSxcImlzQmFja2ZpbGxlZFwiOmZhbHNlLFwiaGFzSW5pdGlhbFNjb3Blc1wiOnRydWUsXCJhcmVBbGxTY29wZXNDb25zZW50ZWRUb1wiOmZhbHNlfSJ9.UU3F4WirHliVUhFNOstHTArrGSqUK3aLv_-xHo7S-bFoD7bJ9wYddfld5R_AgpAa"
	}

	fmt.Printf("clientId len=%d, clientSecret len=%d, refreshToken len=%d\n", len(clientID), len(clientSecret), len(refreshToken))

	tokenRegion := "us-east-1"
	proxyStr := "http://127.0.0.1:7890"
	if len(os.Args) > 1 {
		proxyStr = os.Args[1]
	}
	proxyURL, _ := url.Parse(proxyStr)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	client := &http.Client{Timeout: 30 * time.Second, Transport: transport}
	directClient := &http.Client{Timeout: 30 * time.Second}

	fmt.Printf("Proxy: %s\n", proxyStr)

	// ========== Step 1: Refresh token ==========
	fmt.Println("\n========== Step 1: Refresh token ==========")
	tokenURL := fmt.Sprintf("https://oidc.%s.amazonaws.com/token", tokenRegion)

	tokenBody := map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"grantType":    "refresh_token",
		"refreshToken": refreshToken,
	}

	jsonBody, _ := json.Marshal(tokenBody)
	req, _ := http.NewRequest("POST", tokenURL, bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Host = fmt.Sprintf("oidc.%s.amazonaws.com", tokenRegion)

	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("Token refresh ERROR: %v\n", err)
		os.Exit(1)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 200 {
		fmt.Printf("Token refresh FAILED: %d\n%s\n", resp.StatusCode, string(respBody))
		os.Exit(1)
	}

	var tokenResp struct {
		AccessToken string `json:"accessToken"`
	}
	json.Unmarshal(respBody, &tokenResp)
	fmt.Printf("Token refresh OK! accessToken length=%d\n", len(tokenResp.AccessToken))

	machineID := "test-machine-id"
	kiroVersion := "0.11.107"

	// ========== Step 2: Test getUsageLimits with different UA fingerprints ==========
	fmt.Println("\n========== Step 2: getUsageLimits UA comparison ==========")

	type uaProfile struct {
		name string
		ua   string
		xua  string
		max  string
	}

	uaProfiles := []uaProfile{
		{
			name: "sub2api (current)",
			ua:   fmt.Sprintf("aws-sdk-js/1.0.0 ua/2.1 os/darwin#24.6.0 lang/js md/nodejs#22.21.1 api/codewhispererruntime#1.0.0 m/N,E KiroIDE-0.11.107-%s", machineID),
			xua:  fmt.Sprintf("aws-sdk-js/1.0.0 KiroIDE-0.11.107-%s", machineID),
			max:  "attempt=1; max=3",
		},
		{
			name: "AIClient-2-API style",
			ua:   fmt.Sprintf("aws-sdk-js/1.0.34 ua/2.1 os/macos#24.6.0 lang/js md/nodejs#22.22.0 api/codewhispererstreaming#1.0.34 m/E KiroIDE-0.11.107-%s", machineID),
			xua:  fmt.Sprintf("aws-sdk-js/1.0.34 KiroIDE-0.11.107-%s", machineID),
			max:  "attempt=1; max=1",
		},
	}

	usageLimitsURL := "https://q.us-east-1.amazonaws.com/getUsageLimits?isEmailRequired=true&origin=AI_EDITOR&resourceType=AGENTIC_REQUEST"

	type clientMode struct {
		name   string
		client *http.Client
	}
	clientModes := []clientMode{
		{"DIRECT", directClient},
		{"PROXY", client},
	}

	for _, profile := range uaProfiles {
		for _, cm := range clientModes {
			fmt.Printf("\n--- %s + %s ---\n", profile.name, cm.name)
			fmt.Printf("  UA: %s\n", profile.ua)

			ulReq, _ := http.NewRequest("GET", usageLimitsURL, nil)
			ulReq.Header.Set("Authorization", "Bearer "+tokenResp.AccessToken)
			ulReq.Header.Set("User-Agent", profile.ua)
			ulReq.Header.Set("x-amz-user-agent", profile.xua)
			ulReq.Header.Set("Host", "q.us-east-1.amazonaws.com")
			ulReq.Header.Set("Connection", "close")
			ulReq.Header.Set("amz-sdk-invocation-id", "test-usage-"+profile.name+"-"+cm.name)
			ulReq.Header.Set("amz-sdk-request", profile.max)

			ulResp, err := cm.client.Do(ulReq)
			if err != nil {
				fmt.Printf("  ERROR: %v\n", err)
				continue
			}
			ulBody, _ := io.ReadAll(ulResp.Body)
			ulResp.Body.Close()

			fmt.Printf("  Status: %d\n", ulResp.StatusCode)
			var prettyUL bytes.Buffer
			if json.Indent(&prettyUL, ulBody, "  ", "  ") == nil {
				// Truncate if too long
				s := prettyUL.String()
				if len(s) > 2000 {
					s = s[:2000] + "\n  ... (truncated)"
				}
				fmt.Printf("  Response:\n  %s\n", s)
			} else {
				raw := string(ulBody)
				if len(raw) > 1000 {
					raw = raw[:1000] + "\n... (truncated)"
				}
				fmt.Printf("  Response:\n%s\n", raw)
			}
		}
	}

	// ========== Step 3: Try multiple endpoints ==========
	type endpoint struct {
		name    string
		url     string
		host    string
		target  string // X-Amz-Target header (empty for AWSQ)
	}

	endpoints := []endpoint{
		{"AWSQ us-east-1", "https://q.us-east-1.amazonaws.com/generateAssistantResponse", "q.us-east-1.amazonaws.com", ""},
		{"CW us-east-1", "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse", "codewhisperer.us-east-1.amazonaws.com", "AmazonCodeWhispererStreamingService.GenerateAssistantResponse"},
	}

	cwReq := map[string]any{
		"conversationState": map[string]any{
			"currentMessage": map[string]any{
				"userInputMessage": map[string]any{
					"content": "Say hello in one word.",
				},
			},
			"chatTriggerType": "MANUAL",
		},
	}
	cwBody, _ := json.Marshal(cwReq)

	for _, ep := range endpoints {
		fmt.Printf("\n========== %s ==========\n", ep.name)

		req2, _ := http.NewRequest("POST", ep.url, bytes.NewReader(cwBody))
		req2.Header.Set("Content-Type", "application/json")
		req2.Header.Set("Authorization", "Bearer "+tokenResp.AccessToken)
		req2.Header.Set("Accept", "text/event-stream")
		req2.Header.Set("User-Agent", fmt.Sprintf("aws-sdk-js/1.0.27 ua/2.1 os/linux lang/js md/nodejs#22.12.0 api/codewhispererstreaming#1.0.27 m/E KiroIDE-%s-%s", kiroVersion, machineID))
		req2.Header.Set("x-amz-user-agent", fmt.Sprintf("aws-sdk-js/1.0.27 KiroIDE-%s-%s", kiroVersion, machineID))
		req2.Header.Set("x-amzn-kiro-agent-mode", "vibe")
		req2.Header.Set("x-amzn-codewhisperer-optout", "true")
		req2.Header.Set("Host", ep.host)
		req2.Header.Set("Connection", "close")
		req2.Header.Set("amz-sdk-invocation-id", "test-invocation-id")
		req2.Header.Set("amz-sdk-request", "attempt=1; max=3")
		if ep.target != "" {
			req2.Header.Set("X-Amz-Target", ep.target)
		}

		resp2, err := client.Do(req2)
		if err != nil {
			fmt.Printf("  ERROR: %v\n", err)
			continue
		}
		respBody2, _ := io.ReadAll(resp2.Body)
		resp2.Body.Close()

		fmt.Printf("  Status: %d\n", resp2.StatusCode)
		var prettyJSON bytes.Buffer
		if json.Indent(&prettyJSON, respBody2, "  ", "  ") == nil {
			fmt.Printf("  Response:\n  %s\n", prettyJSON.String())
		} else {
			raw := string(respBody2)
			if len(raw) > 1000 {
				raw = raw[:1000] + "\n... (truncated)"
			}
			fmt.Printf("  Response:\n%s\n", raw)
		}
	}
}
