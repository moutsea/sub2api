// +build ignore

// probe.go — 探查 Kiro 账号的 getUsageLimits 返回值
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

const proxyAddr = "http://127.0.0.1:7890"

func main() {
	region := "us-east-1"
	clientID := "0EiiEUbQKRHXVTzFDLxYVHVzLWVhc3QtMQ"
	clientSecret := `eyJraWQiOiJrZXktMTU2NDAyODA5OSIsImFsZyI6IkhTMzg0In0.eyJzZXJpYWxpemVkIjoie1wiY2xpZW50SWRcIjp7XCJ2YWx1ZVwiOlwiMEVpaUVVYlFLUkhYVlR6RkRMeFlWSFZ6TFdWaGMzUXRNUVwifSxcImlkZW1wb3RlbnRLZXlcIjpudWxsLFwidGVuYW50SWRcIjpudWxsLFwiY2xpZW50TmFtZVwiOlwiQW1hem9uIFEgRGV2ZWxvcGVyIGZvciBjb21tYW5kIGxpbmVcIixcImJhY2tmaWxsVmVyc2lvblwiOm51bGwsXCJjbGllbnRUeXBlXCI6XCJQVUJMSUNcIixcInRlbXBsYXRlQXJuXCI6bnVsbCxcInRlbXBsYXRlQ29udGV4dFwiOm51bGwsXCJleHBpcmF0aW9uVGltZXN0YW1wXCI6MTc4MDc2NDA0OC42MDU5MzQ4NjMsXCJjcmVhdGVkVGltZXN0YW1wXCI6MTc3Mjk4ODA0OC42MDU5MzQ4NjMsXCJ1cGRhdGVkVGltZXN0YW1wXCI6MTc3Mjk4ODA0OC42MDU5MzQ4NjMsXCJjcmVhdGVkQnlcIjpudWxsLFwidXBkYXRlZEJ5XCI6bnVsbCxcInN0YXR1c1wiOm51bGwsXCJpbml0aWF0ZUxvZ2luVXJpXCI6bnVsbCxcImVudGl0bGVkUmVzb3VyY2VJZFwiOm51bGwsXCJlbnRpdGxlZFJlc291cmNlQ29udGFpbmVySWRcIjpudWxsLFwiZXh0ZXJuYWxJZFwiOm51bGwsXCJzb2Z0d2FyZUlkXCI6bnVsbCxcInNjb3Blc1wiOlt7XCJmdWxsU2NvcGVcIjpcImNvZGV3aGlzcGVyZXI6Y29tcGxldGlvbnNcIixcInN0YXR1c1wiOlwiSU5JVElBTFwiLFwiYXBwbGljYXRpb25Bcm5cIjpudWxsLFwiZnJpZW5kbHlJZFwiOlwiY29kZXdoaXNwZXJlclwiLFwidXNlQ2FzZUFjdGlvblwiOlwiY29tcGxldGlvbnNcIixcInNjb3BlVHlwZVwiOlwiQUNDRVNTX1NDT1BFXCIsXCJ0eXBlXCI6XCJJbW11dGFibGVBY2Nlc3NTY29wZVwifSx7XCJmdWxsU2NvcGVcIjpcImNvZGV3aGlzcGVyZXI6YW5hbHlzaXNcIixcInN0YXR1c1wiOlwiSU5JVElBTFwiLFwiYXBwbGljYXRpb25Bcm5cIjpudWxsLFwiZnJpZW5kbHlJZFwiOlwiY29kZXdoaXNwZXJlclwiLFwidXNlQ2FzZUFjdGlvblwiOlwiYW5hbHlzaXNcIixcInNjb3BlVHlwZVwiOlwiQUNDRVNTX1NDT1BFXCIsXCJ0eXBlXCI6XCJJbW11dGFibGVBY2Nlc3NTY29wZVwifSx7XCJmdWxsU2NvcGVcIjpcImNvZGV3aGlzcGVyZXI6Y29udmVyc2F0aW9uc1wiLFwic3RhdHVzXCI6XCJJTklUSUFMXCIsXCJhcHBsaWNhdGlvbkFyblwiOm51bGwsXCJmcmllbmRseUlkXCI6XCJjb2Rld2hpc3BlcmVyXCIsXCJ1c2VDYXNlQWN0aW9uXCI6XCJjb252ZXJzYXRpb25zXCIsXCJzY29wZVR5cGVcIjpcIkFDQ0VTU19TQ09QRVwiLFwidHlwZVwiOlwiSW1tdXRhYmxlQWNjZXNzU2NvcGVcIn1dLFwiYXV0aGVudGljYXRpb25Db25maWd1cmF0aW9uXCI6bnVsbCxcInNoYWRvd0F1dGhlbnRpY2F0aW9uQ29uZmlndXJhdGlvblwiOm51bGwsXCJlbmFibGVkR3JhbnRzXCI6bnVsbCxcImVuZm9yY2VBdXRoTkNvbmZpZ3VyYXRpb25cIjpudWxsLFwib3duZXJBY2NvdW50SWRcIjpudWxsLFwic3NvSW5zdGFuY2VBY2NvdW50SWRcIjpudWxsLFwidXNlckNvbnNlbnRcIjpudWxsLFwibm9uSW50ZXJhY3RpdmVTZXNzaW9uc0VuYWJsZWRcIjpudWxsLFwiYXNzb2NpYXRlZEluc3RhbmNlQXJuXCI6bnVsbCxcInNzb1Njb3Blc1wiOltdLFwiZ3JvdXBTY29wZXNCeUZyaWVuZGx5SWRcIjp7XCJjb2Rld2hpc3BlcmVyXCI6W3tcImZ1bGxTY29wZVwiOlwiY29kZXdoaXNwZXJlcjpjb252ZXJzYXRpb25zXCIsXCJzdGF0dXNcIjpcIklOSVRJQUxcIixcImFwcGxpY2F0aW9uQXJuXCI6bnVsbCxcImZyaWVuZGx5SWRcIjpcImNvZGV3aGlzcGVyZXJcIixcInVzZUNhc2VBY3Rpb25cIjpcImNvbnZlcnNhdGlvbnNcIixcInNjb3BlVHlwZVwiOlwiQUNDRVNTX1NDT1BFXCIsXCJ0eXBlXCI6XCJJbW11dGFibGVBY2Nlc3NTY29wZVwifSx7XCJmdWxsU2NvcGVcIjpcImNvZGV3aGlzcGVyZXI6Y29tcGxldGlvbnNcIixcInN0YXR1c1wiOlwiSU5JVElBTFwiLFwiYXBwbGljYXRpb25Bcm5cIjpudWxsLFwiZnJpZW5kbHlJZFwiOlwiY29kZXdoaXNwZXJlclwiLFwidXNlQ2FzZUFjdGlvblwiOlwiY29tcGxldGlvbnNcIixcInNjb3BlVHlwZVwiOlwiQUNDRVNTX1NDT1BFXCIsXCJ0eXBlXCI6XCJJbW11dGFibGVBY2Nlc3NTY29wZVwifSx7XCJmdWxsU2NvcGVcIjpcImNvZGV3aGlzcGVyZXI6YW5hbHlzaXNcIixcInN0YXR1c1wiOlwiSU5JVElBTFwiLFwiYXBwbGljYXRpb25Bcm5cIjpudWxsLFwiZnJpZW5kbHlJZFwiOlwiY29kZXdoaXNwZXJlclwiLFwidXNlQ2FzZUFjdGlvblwiOlwiYW5hbHlzaXNcIixcInNjb3BlVHlwZVwiOlwiQUNDRVNTX1NDT1BFXCIsXCJ0eXBlXCI6XCJJbW11dGFibGVBY2Nlc3NTY29wZVwifV19LFwic2hvdWxkR2V0VmFsdWVGcm9tVGVtcGxhdGVcIjp0cnVlLFwiaGFzUmVxdWVzdGVkU2NvcGVzXCI6ZmFsc2UsXCJjb250YWluc09ubHlTc29TY29wZXNcIjpmYWxzZSxcImlzVjFCYWNrZmlsbGVkXCI6ZmFsc2UsXCJpc1YyQmFja2ZpbGxlZFwiOmZhbHNlLFwiaXNWM0JhY2tmaWxsZWRcIjpmYWxzZSxcImlzVjRCYWNrZmlsbGVkXCI6ZmFsc2UsXCJpc0V4cGlyZWRcIjpmYWxzZSxcImlzQmFja2ZpbGxlZFwiOmZhbHNlLFwiaGFzSW5pdGlhbFNjb3Blc1wiOnRydWUsXCJhcmVBbGxTY29wZXNDb25zZW50ZWRUb1wiOmZhbHNlfSJ9.XNlhY4zoKzC4zKbU-Nr1W1n3iSQV8EC7H4fQscjodLnK5GkA7-U0f6BqSfKSitz3`
	refreshToken := "aorAAAAAGokS_sSz6nRsIdgLguxKin9OIJvPQ2-KiDpEJK3XhgSorUCP7nCKs0pZZLDkb2h1wVdG_nV1i2IRBAKOUBkc0:MGYCMQChsE9QF6ZNK0anel/ehALFoJ/tqtIFnIkkjRUUDWRptgBMNx9sWQNpVHtRkKiLfwACMQCDNmjhmV/ngOaopDO8Pi+VwRW8hmi+fhS0JTYoFSUVyz1ML7lvpaMs4s77VjTUl+I"

	ctx := context.Background()
	client := newProxyClient(proxyAddr)

	fmt.Println("═══════════════════════════════════════════════════")
	fmt.Println("  Kiro Pro 账号用量探查")
	fmt.Println("═══════════════════════════════════════════════════")
	fmt.Printf("  Region: %s\n", region)

	// Step 1: 刷新 token
	fmt.Println("\n[Step 1] 刷新 Token...")
	reqBody, _ := json.Marshal(map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"grantType":    "refresh_token",
		"refreshToken": refreshToken,
	})

	refreshURL := fmt.Sprintf("https://oidc.%s.amazonaws.com/token", region)
	req, _ := http.NewRequestWithContext(ctx, "POST", refreshURL, strings.NewReader(string(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Host", fmt.Sprintf("oidc.%s.amazonaws.com", region))
	req.Header.Set("User-Agent", "node")

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Token 刷新失败: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "Token 刷新失败: status=%d body=%s\n", resp.StatusCode, string(body))
		os.Exit(1)
	}

	var tokenResp struct {
		AccessToken string `json:"accessToken"`
	}
	json.Unmarshal(body, &tokenResp)
	fmt.Printf("  ✓ Token 刷新成功! AccessToken: %s...\n", tokenResp.AccessToken[:50])

	// Step 2: 查询用量（尝试多种 resourceType）
	resourceTypes := []string{"AGENTIC_REQUEST", "CREDIT", ""}
	for _, rt := range resourceTypes {
		fmt.Printf("\n[Step 2] getUsageLimits (resourceType=%q)...\n", rt)

		params := url.Values{}
		params.Add("isEmailRequired", "true")
		params.Add("origin", "AI_EDITOR")
		if rt != "" {
			params.Add("resourceType", rt)
		}
		requestURL := fmt.Sprintf("https://q.%s.amazonaws.com/getUsageLimits?%s", region, params.Encode())

		req, _ := http.NewRequestWithContext(ctx, "GET", requestURL, nil)
		req.Header.Set("x-amz-user-agent", "aws-sdk-js/1.0.27 KiroGateway")
		req.Header.Set("User-Agent", "aws-sdk-js/1.0.27 ua/2.1 os/linux lang/js md/nodejs#22.12.0 api/codewhispererstreaming#1.0.27 m/E KiroGateway")
		req.Header.Set("Host", fmt.Sprintf("q.%s.amazonaws.com", region))
		req.Header.Set("Connection", "close")
		req.Header.Set("amz-sdk-invocation-id", uuid.New().String())
		req.Header.Set("amz-sdk-request", "attempt=1; max=3")
		req.Header.Set("Authorization", "Bearer "+tokenResp.AccessToken)

		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("  ✗ 请求失败: %v\n", err)
			continue
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

		fmt.Printf("  Status: %d\n", resp.StatusCode)

		if resp.StatusCode == 200 {
			// Pretty print JSON
			var prettyJSON map[string]any
			if err := json.Unmarshal(body, &prettyJSON); err == nil {
				pretty, _ := json.MarshalIndent(prettyJSON, "  ", "  ")
				fmt.Printf("  Response:\n  %s\n", string(pretty))
			} else {
				fmt.Printf("  Body: %s\n", string(body))
			}
		} else {
			fmt.Printf("  Body: %s\n", string(body))
		}
	}
}

func newProxyClient(proxy string) *http.Client {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
	}
	if proxy != "" {
		proxyURL, err := url.Parse(proxy)
		if err == nil {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second}
}
