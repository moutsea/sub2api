// test_usage 测试 Kiro OAuth (IdC) 账号的用量刷新逻辑
// 步骤：1. 用 refreshToken 刷新 accessToken  2. 用 accessToken 查询用量
// 使用方法：go run ./cmd/test_usage/main.go
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

const (
	proxyAddr   = "http://127.0.0.1:7890" // 本地代理地址，按需修改
	accountFile = "/Users/liang/Downloads/account_a445ded9-b066-4c60-bd31-1dc28ec1d144.json"
)

// AccountJSON 账号 JSON 结构
type AccountJSON struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	Region       string `json:"region"`
	AuthMethod   string `json:"authMethod"`
	ProfileArn   string `json:"profileArn"`
	Email        string `json:"email"`
}

// IdCRefreshRequest OIDC 刷新请求
type IdCRefreshRequest struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	GrantType    string `json:"grantType"`
	RefreshToken string `json:"refreshToken"`
}

// IdCRefreshResponse OIDC 刷新响应
type IdCRefreshResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int    `json:"expiresIn"`
	TokenType    string `json:"tokenType"`
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

// refreshIdCToken 使用 OIDC 刷新 access token
func refreshIdCToken(ctx context.Context, client *http.Client, account *AccountJSON) (*IdCRefreshResponse, error) {
	refreshURL := fmt.Sprintf("https://oidc.%s.amazonaws.com/token", account.Region)
	hostHeader := fmt.Sprintf("oidc.%s.amazonaws.com", account.Region)

	reqBody, _ := json.Marshal(IdCRefreshRequest{
		ClientID:     account.ClientID,
		ClientSecret: account.ClientSecret,
		GrantType:    "refresh_token",
		RefreshToken: account.RefreshToken,
	})

	req, err := http.NewRequestWithContext(ctx, "POST", refreshURL, strings.NewReader(string(reqBody)))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Host", hostHeader)
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("x-amz-user-agent", "aws-sdk-js/3.738.0 ua/2.1 os/other lang/js md/browser#unknown_unknown api/sso-oidc#3.738.0 m/E KiroIDE")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "node")

	fmt.Printf("  → POST %s\n", refreshURL)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("  ← Status: %d, Body: %s\n", resp.StatusCode, truncate(string(body), 200))

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh failed: status %d, body: %s", resp.StatusCode, string(body))
	}

	var idcResp IdCRefreshResponse
	if err := json.Unmarshal(body, &idcResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &idcResp, nil
}

// fetchUsageLimitsEx 查询用量（支持自定义 host 和 X-Amz-Target）
func fetchUsageLimitsEx(ctx context.Context, client *http.Client, accessToken, region, hostOverride, amzTarget string) (json.RawMessage, error) {
	host := fmt.Sprintf("q.%s.amazonaws.com", region)
	if hostOverride != "" {
		host = hostOverride
	}

	baseURL := fmt.Sprintf("https://%s/getUsageLimits", host)
	params := url.Values{}
	params.Add("isEmailRequired", "true")
	params.Add("origin", "AI_EDITOR")
	params.Add("resourceType", "AGENTIC_REQUEST")
	requestURL := fmt.Sprintf("%s?%s", baseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("x-amz-user-agent", "aws-sdk-js/1.0.27 KiroGateway")
	req.Header.Set("User-Agent", "aws-sdk-js/1.0.27 ua/2.1 os/linux lang/js md/nodejs#22.12.0 api/codewhispererstreaming#1.0.27 m/E KiroGateway")
	req.Header.Set("Host", host)
	req.Header.Set("Connection", "close")
	req.Header.Set("amz-sdk-invocation-id", uuid.New().String())
	req.Header.Set("amz-sdk-request", "attempt=1; max=3")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if amzTarget != "" {
		req.Header.Set("X-Amz-Target", amzTarget)
	}

	fmt.Printf("  → GET https://%s/getUsageLimits\n", host)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	fmt.Printf("  ← Status: %d, Body: %s\n", resp.StatusCode, truncate(string(body), 500))

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, string(body))
	}
	return json.RawMessage(body), nil
}

// fetchUsageLimits 查询用量（原始版本保留兼容）
func fetchUsageLimits(ctx context.Context, client *http.Client, accessToken, region string) (json.RawMessage, error) {
	baseURL := fmt.Sprintf("https://q.%s.amazonaws.com/getUsageLimits", region)
	params := url.Values{}
	params.Add("isEmailRequired", "true")
	params.Add("origin", "AI_EDITOR")
	params.Add("resourceType", "AGENTIC_REQUEST")
	requestURL := fmt.Sprintf("%s?%s", baseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("x-amz-user-agent", "aws-sdk-js/1.0.27 KiroGateway")
	req.Header.Set("User-Agent", "aws-sdk-js/1.0.27 ua/2.1 os/linux lang/js md/nodejs#22.12.0 api/codewhispererstreaming#1.0.27 m/E KiroGateway")
	req.Header.Set("Host", fmt.Sprintf("q.%s.amazonaws.com", region))
	req.Header.Set("Connection", "close")
	req.Header.Set("amz-sdk-invocation-id", uuid.New().String())
	req.Header.Set("amz-sdk-request", "attempt=1; max=3")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	fmt.Printf("  → GET %s\n", baseURL)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	fmt.Printf("  ← Status: %d, Body: %s\n", resp.StatusCode, truncate(string(body), 500))

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, string(body))
	}
	return json.RawMessage(body), nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func main() {
	// 读取账号文件
	data, err := os.ReadFile(accountFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ 读取账号文件失败: %v\n", err)
		os.Exit(1)
	}

	var account AccountJSON
	if err := json.Unmarshal(data, &account); err != nil {
		fmt.Fprintf(os.Stderr, "❌ 解析账号 JSON 失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("═══════════════════════════════════════════════════")
	fmt.Println("  Kiro OAuth (IdC) 用量刷新测试")
	fmt.Println("═══════════════════════════════════════════════════")
	fmt.Printf("  Email:      %s\n", account.Email)
	fmt.Printf("  AuthMethod: %s\n", account.AuthMethod)
	fmt.Printf("  Region:     %s\n", account.Region)
	fmt.Printf("  ProfileArn: %s\n", account.ProfileArn)
	fmt.Printf("  Proxy:      %s\n", proxyAddr)
	fmt.Println("───────────────────────────────────────────────────")

	ctx := context.Background()
	client := newProxyClient(proxyAddr)

	// ═══ Step 1: 刷新 Token ═══
	fmt.Println("\n[Step 1] 刷新 IdC Access Token (region: " + account.Region + ")")
	tokenResp, err := refreshIdCToken(ctx, client, &account)
	var accessToken string
	if err != nil {
		fmt.Printf("  ⚠ IdC Token 刷新失败: %v\n", err)
		fmt.Println("  → 使用账号文件中的旧 accessToken 继续测试...")
		accessToken = account.AccessToken
	} else {
		accessToken = tokenResp.AccessToken
		fmt.Printf("  ✓ Token 刷新成功! ExpiresIn=%d, TokenType=%s\n", tokenResp.ExpiresIn, tokenResp.TokenType)
		fmt.Printf("  ✓ New AccessToken: %s...\n", truncate(accessToken, 50))
	}

	// ═══ Step 2: 穷举所有 AWS region ═══
	allRegions := []string{
		// North America
		"us-east-1", "us-east-2", "us-west-1", "us-west-2",
		"ca-central-1", "ca-west-1",
		// Europe
		"eu-west-1", "eu-west-2", "eu-west-3",
		"eu-central-1", "eu-central-2",
		"eu-north-1", "eu-south-1", "eu-south-2",
		// Asia Pacific
		"ap-southeast-1", "ap-southeast-2", "ap-southeast-3", "ap-southeast-4", "ap-southeast-5",
		"ap-northeast-1", "ap-northeast-2", "ap-northeast-3",
		"ap-south-1", "ap-south-2", "ap-east-1",
		// South America
		"sa-east-1",
		// Middle East / Africa
		"me-south-1", "me-central-1",
		"af-south-1",
		// Israel
		"il-central-1",
	}

	fmt.Printf("\n[Step 2] 穷举 %d 个 AWS region 的 q.{region}.amazonaws.com/getUsageLimits\n", len(allRegions))
	fmt.Println("───────────────────────────────────────────────────")

	type regionResult struct {
		region string
		status int
		body   string
		err    error
	}

	results := make(chan regionResult, len(allRegions))

	for _, region := range allRegions {
		go func(r string) {
			testCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			host := fmt.Sprintf("q.%s.amazonaws.com", r)
			params := url.Values{}
			params.Add("isEmailRequired", "true")
			params.Add("origin", "AI_EDITOR")
			params.Add("resourceType", "AGENTIC_REQUEST")
			requestURL := fmt.Sprintf("https://%s/getUsageLimits?%s", host, params.Encode())

			req, _ := http.NewRequestWithContext(testCtx, "GET", requestURL, nil)
			req.Header.Set("x-amz-user-agent", "aws-sdk-js/1.0.27 KiroGateway")
			req.Header.Set("User-Agent", "aws-sdk-js/1.0.27 ua/2.1 os/linux lang/js md/nodejs#22.12.0 api/codewhispererstreaming#1.0.27 m/E KiroGateway")
			req.Header.Set("Host", host)
			req.Header.Set("Connection", "close")
			req.Header.Set("amz-sdk-invocation-id", uuid.New().String())
			req.Header.Set("amz-sdk-request", "attempt=1; max=3")
			req.Header.Set("Authorization", "Bearer "+accessToken)

			resp, err := client.Do(req)
			if err != nil {
				results <- regionResult{r, 0, "", err}
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			results <- regionResult{r, resp.StatusCode, string(body), nil}
		}(region)
	}

	// 收集结果
	var successRegions []string
	var reachableRegions []regionResult

	for i := 0; i < len(allRegions); i++ {
		r := <-results
		if r.err != nil {
			// DNS/连接失败 — 端点不存在
			fmt.Printf("  %-20s ✗ %v\n", r.region, truncate(r.err.Error(), 60))
		} else {
			reachableRegions = append(reachableRegions, r)
			if r.status == 200 {
				successRegions = append(successRegions, r.region)
				fmt.Printf("  %-20s ✓ 200 OK — %s\n", r.region, truncate(r.body, 120))
			} else {
				fmt.Printf("  %-20s ✗ %d — %s\n", r.region, r.status, truncate(r.body, 80))
			}
		}
	}

	fmt.Println("\n═══════════════════════════════════════════════════")
	fmt.Printf("  可达 region: %d / %d\n", len(reachableRegions), len(allRegions))
	if len(successRegions) > 0 {
		fmt.Printf("  ✓ 成功 region: %v\n", successRegions)
	} else {
		fmt.Println("  ✗ 无任何 region 返回 200")
	}

	// 对可达但非200的region打印详情
	if len(successRegions) == 0 && len(reachableRegions) > 0 {
		fmt.Println("\n  可达 region 详情:")
		for _, r := range reachableRegions {
			fmt.Printf("    %-20s status=%d body=%s\n", r.region, r.status, truncate(r.body, 120))
		}
	}
	fmt.Println("═══════════════════════════════════════════════════")
}
