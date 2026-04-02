package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const (
	refreshToken = `aorAAAAAGoPPC0oFEZIGtjxBKS7-ISTPIpcyw2fXrFbv7monOXA6ssdTyG577SD8J6hTDWNw6jiP7Y3nNZH8euQRkBea0:MGYCMQCjcUxrAD6vFdCMqkUJsKZjPlw4FRX8YQIoIhLPdcErdvOWx+YD4QhOU5tKUE8J5FkCMQCSuPS/Yg7YzDvqA6O/jO6RaUzD7v8llv39UyPD5Mudessw9xkod/ef4YRIgVhQlnE`
	clientID     = `c2Cuvp_WRKuaWCjG8kNjAWV1LW5vcnRoLTE`
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
		clientSecret = "eyJraWQiOiJrZXktMTU4ODcwMjU2NSIsImFsZyI6IkhTMzg0In0.eyJzZXJpYWxpemVkIjoie1wiY2xpZW50SWRcIjp7XCJ2YWx1ZVwiOlwiYzJDdXZwX1dSS3VhV0NqRzhrTmpBV1YxTFc1dmNuUm9MVEVcIn0sXCJpZGVtcG90ZW50S2V5XCI6bnVsbCxcInRlbmFudElkXCI6bnVsbCxcImNsaWVudE5hbWVcIjpcIktpcm8gQWNjb3VudCBNYW5hZ2VyXCIsXCJiYWNrZmlsbFZlcnNpb25cIjpudWxsLFwiY2xpZW50VHlwZVwiOlwiUFVCTElDXCIsXCJ0ZW1wbGF0ZUFyblwiOm51bGwsXCJ0ZW1wbGF0ZUNvbnRleHRcIjpudWxsLFwiZXhwaXJhdGlvblRpbWVzdGFtcFwiOjE3NzkzODMyOTkuNzkyMTA5Nzg2LFwiY3JlYXRlZFRpbWVzdGFtcFwiOjE3NzE2MDcyOTkuNzkyMTA5Nzg2LFwidXBkYXRlZFRpbWVzdGFtcFwiOjE3NzE2MDcyOTkuNzkyMTA5Nzg2LFwiY3JlYXRlZEJ5XCI6bnVsbCxcInVwZGF0ZWRCeVwiOm51bGwsXCJzdGF0dXNcIjpudWxsLFwiaW5pdGlhdGVMb2dpblVyaVwiOlwiaHR0cHM6Ly9kLWMzNjc2NDBiM2YuYXdzYXBwcy5jb20vc3RhcnQvXCIsXCJlbnRpdGxlZFJlc291cmNlSWRcIjpudWxsLFwiZW50aXRsZWRSZXNvdXJjZUNvbnRhaW5lcklkXCI6bnVsbCxcImV4dGVybmFsSWRcIjpudWxsLFwic29mdHdhcmVJZFwiOm51bGwsXCJzY29wZXNcIjpbe1wiZnVsbFNjb3BlXCI6XCJjb2Rld2hpc3BlcmVyOmNvbXBsZXRpb25zXCIsXCJzdGF0dXNcIjpcIklOSVRJQUxcIixcImFwcGxpY2F0aW9uQXJuXCI6bnVsbCxcImZyaWVuZGx5SWRcIjpcImNvZGV3aGlzcGVyZXJcIixcInVzZUNhc2VBY3Rpb25cIjpcImNvbXBsZXRpb25zXCIsXCJ0eXBlXCI6XCJJbW11dGFibGVBY2Nlc3NTY29wZVwiLFwic2NvcGVUeXBlXCI6XCJBQ0NFU1NfU0NPUEVcIn0se1wiZnVsbFNjb3BlXCI6XCJjb2Rld2hpc3BlcmVyOmFuYWx5c2lzXCIsXCJzdGF0dXNcIjpcIklOSVRJQUxcIixcImFwcGxpY2F0aW9uQXJuXCI6bnVsbCxcImZyaWVuZGx5SWRcIjpcImNvZGV3aGlzcGVyZXJcIixcInVzZUNhc2VBY3Rpb25cIjpcImFuYWx5c2lzXCIsXCJ0eXBlXCI6XCJJbW11dGFibGVBY2Nlc3NTY29wZVwiLFwic2NvcGVUeXBlXCI6XCJBQ0NFU1NfU0NPUEVcIn0se1wiZnVsbFNjb3BlXCI6XCJjb2Rld2hpc3BlcmVyOmNvbnZlcnNhdGlvbnNcIixcInN0YXR1c1wiOlwiSU5JVElBTFwiLFwiYXBwbGljYXRpb25Bcm5cIjpudWxsLFwiZnJpZW5kbHlJZFwiOlwiY29kZXdoaXNwZXJlclwiLFwidXNlQ2FzZUFjdGlvblwiOlwiY29udmVyc2F0aW9uc1wiLFwidHlwZVwiOlwiSW1tdXRhYmxlQWNjZXNzU2NvcGVcIixcInNjb3BlVHlwZVwiOlwiQUNDRVNTX1NDT1BFXCJ9LHtcImZ1bGxTY29wZVwiOlwiY29kZXdoaXNwZXJlcjp0cmFuc2Zvcm1hdGlvbnNcIixcInN0YXR1c1wiOlwiSU5JVElBTFwiLFwiYXBwbGljYXRpb25Bcm5cIjpudWxsLFwiZnJpZW5kbHlJZFwiOlwiY29kZXdoaXNwZXJlclwiLFwidXNlQ2FzZUFjdGlvblwiOlwidHJhbnNmb3JtYXRpb25zXCIsXCJ0eXBlXCI6XCJJbW11dGFibGVBY2Nlc3NTY29wZVwiLFwic2NvcGVUeXBlXCI6XCJBQ0NFU1NfU0NPUEVcIn0se1wiZnVsbFNjb3BlXCI6XCJjb2Rld2hpc3BlcmVyOnRhc2thc3Npc3RcIixcInN0YXR1c1wiOlwiSU5JVElBTFwiLFwiYXBwbGljYXRpb25Bcm5cIjpudWxsLFwiZnJpZW5kbHlJZFwiOlwiY29kZXdoaXNwZXJlclwiLFwidXNlQ2FzZUFjdGlvblwiOlwidGFza2Fzc2lzdFwiLFwidHlwZVwiOlwiSW1tdXRhYmxlQWNjZXNzU2NvcGVcIixcInNjb3BlVHlwZVwiOlwiQUNDRVNTX1NDT1BFXCJ9XSxcImF1dGhlbnRpY2F0aW9uQ29uZmlndXJhdGlvblwiOm51bGwsXCJzaGFkb3dBdXRoZW50aWNhdGlvbkNvbmZpZ3VyYXRpb25cIjpudWxsLFwiZW5hYmxlZEdyYW50c1wiOntcIkFVVEhfQ09ERVwiOntcInR5cGVcIjpcIkltbXV0YWJsZUF1dGhvcml6YXRpb25Db2RlR3JhbnRPcHRpb25zXCIsXCJyZWRpcmVjdFVyaXNcIjpbXCJodHRwOi8vMTI3LjAuMC4xL29hdXRoL2NhbGxiYWNrXCJdfSxcIlJFRlJFU0hfVE9LRU5cIjp7XCJ0eXBlXCI6XCJJbW11dGFibGVSZWZyZXNoVG9rZW5HcmFudE9wdGlvbnNcIn19LFwiZW5mb3JjZUF1dGhOQ29uZmlndXJhdGlvblwiOm51bGwsXCJvd25lckFjY291bnRJZFwiOm51bGwsXCJzc29JbnN0YW5jZUFjY291bnRJZFwiOm51bGwsXCJ1c2VyQ29uc2VudFwiOm51bGwsXCJub25JbnRlcmFjdGl2ZVNlc3Npb25zRW5hYmxlZFwiOm51bGwsXCJhc3NvY2lhdGVkSW5zdGFuY2VBcm5cIjpudWxsLFwiaXNFeHBpcmVkXCI6ZmFsc2UsXCJpc0JhY2tmaWxsZWRcIjpmYWxzZSxcImhhc0luaXRpYWxTY29wZXNcIjp0cnVlLFwiYXJlQWxsU2NvcGVzQ29uc2VudGVkVG9cIjpmYWxzZSxcInNob3VsZEdldFZhbHVlRnJvbVRlbXBsYXRlXCI6ZmFsc2UsXCJoYXNSZXF1ZXN0ZWRTY29wZXNcIjpmYWxzZSxcImNvbnRhaW5zT25seVNzb1Njb3Blc1wiOmZhbHNlLFwic3NvU2NvcGVzXCI6W10sXCJpc1YxQmFja2ZpbGxlZFwiOmZhbHNlLFwiaXNWMkJhY2tmaWxsZWRcIjpmYWxzZSxcImlzVjNCYWNrZmlsbGVkXCI6ZmFsc2UsXCJpc1Y0QmFja2ZpbGxlZFwiOmZhbHNlLFwiZ3JvdXBTY29wZXNCeUZyaWVuZGx5SWRcIjp7XCJjb2Rld2hpc3BlcmVyXCI6W3tcImZ1bGxTY29wZVwiOlwiY29kZXdoaXNwZXJlcjp0YXNrYXNzaXN0XCIsXCJzdGF0dXNcIjpcIklOSVRJQUxcIixcImFwcGxpY2F0aW9uQXJuXCI6bnVsbCxcImZyaWVuZGx5SWRcIjpcImNvZGV3aGlzcGVyZXJcIixcInVzZUNhc2VBY3Rpb25cIjpcInRhc2thc3Npc3RcIixcInR5cGVcIjpcIkltbXV0YWJsZUFjY2Vzc1Njb3BlXCIsXCJzY29wZVR5cGVcIjpcIkFDQ0VTU19TQ09QRVwifSx7XCJmdWxsU2NvcGVcIjpcImNvZGV3aGlzcGVyZXI6Y29tcGxldGlvbnNcIixcInN0YXR1c1wiOlwiSU5JVElBTFwiLFwiYXBwbGljYXRpb25Bcm5cIjpudWxsLFwiZnJpZW5kbHlJZFwiOlwiY29kZXdoaXNwZXJlclwiLFwidXNlQ2FzZUFjdGlvblwiOlwiY29tcGxldGlvbnNcIixcInR5cGVcIjpcIkltbXV0YWJsZUFjY2Vzc1Njb3BlXCIsXCJzY29wZVR5cGVcIjpcIkFDQ0VTU19TQ09QRVwifSx7XCJmdWxsU2NvcGVcIjpcImNvZGV3aGlzcGVyZXI6Y29udmVyc2F0aW9uc1wiLFwic3RhdHVzXCI6XCJJTklUSUFMXCIsXCJhcHBsaWNhdGlvbkFyblwiOm51bGwsXCJmcmllbmRseUlkXCI6XCJjb2Rld2hpc3BlcmVyXCIsXCJ1c2VDYXNlQWN0aW9uXCI6XCJjb252ZXJzYXRpb25zXCIsXCJ0eXBlXCI6XCJJbW11dGFibGVBY2Nlc3NTY29wZVwiLFwic2NvcGVUeXBlXCI6XCJBQ0NFU1NfU0NPUEVcIn0se1wiZnVsbFNjb3BlXCI6XCJjb2Rld2hpc3BlcmVyOmFuYWx5c2lzXCIsXCJzdGF0dXNcIjpcIklOSVRJQUxcIixcImFwcGxpY2F0aW9uQXJuXCI6bnVsbCxcImZyaWVuZGx5SWRcIjpcImNvZGV3aGlzcGVyZXJcIixcInVzZUNhc2VBY3Rpb25cIjpcImFuYWx5c2lzXCIsXCJ0eXBlXCI6XCJJbW11dGFibGVBY2Nlc3NTY29wZVwiLFwic2NvcGVUeXBlXCI6XCJBQ0NFU1NfU0NPUEVcIn0se1wiZnVsbFNjb3BlXCI6XCJjb2Rld2hpc3BlcmVyOnRyYW5zZm9ybWF0aW9uc1wiLFwic3RhdHVzXCI6XCJJTklUSUFMXCIsXCJhcHBsaWNhdGlvbkFyblwiOm51bGwsXCJmcmllbmRseUlkXCI6XCJjb2Rld2hpc3BlcmVyXCIsXCJ1c2VDYXNlQWN0aW9uXCI6XCJ0cmFuc2Zvcm1hdGlvbnNcIixcInR5cGVcIjpcIkltbXV0YWJsZUFjY2Vzc1Njb3BlXCIsXCJzY29wZVR5cGVcIjpcIkFDQ0VTU19TQ09QRVwifV19fSJ9.A8hLktsJIoTfAEaSee6K0mXS_iGLYULx-0LozcQaBEjd8_3U4I54ZSM4nSFBPPkD"
	}

	fmt.Printf("clientId len=%d, clientSecret len=%d, refreshToken len=%d\n", len(clientID), len(clientSecret), len(refreshToken))

	tokenRegion := "eu-north-1"
	client := &http.Client{Timeout: 30 * time.Second}

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

	// ========== Step 2: Try multiple endpoints ==========
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

	machineID := "test-machine-id"
	kiroVersion := "0.11.107"

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
