// Package kiro provides Kiro/CodeWhisperer API integration utilities.
package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"time"

	"github.com/google/uuid"
)

// UsageLimits represents the CodeWhisperer getUsageLimits API response
type UsageLimits struct {
	Limits               []any            `json:"limits"`
	UsageBreakdownList   []UsageBreakdown `json:"usageBreakdownList"`
	UserInfo             UserInfo         `json:"userInfo"`
	DaysUntilReset       int              `json:"daysUntilReset"`
	OverageConfiguration OverageConfig    `json:"overageConfiguration"`
	NextDateReset        float64          `json:"nextDateReset"`
	SubscriptionInfo     SubscriptionInfo `json:"subscriptionInfo"`
	UsageBreakdown       any              `json:"usageBreakdown"`
}

// UsageBreakdown represents detailed usage information for a resource type
type UsageBreakdown struct {
	NextDateReset                float64        `json:"nextDateReset"`
	OverageCharges               float64        `json:"overageCharges"`
	ResourceType                 string         `json:"resourceType"`
	Unit                         string         `json:"unit"`
	UsageLimit                   int            `json:"usageLimit"`
	UsageLimitWithPrecision      float64        `json:"usageLimitWithPrecision"`
	OverageRate                  float64        `json:"overageRate"`
	CurrentUsage                 int            `json:"currentUsage"`
	CurrentUsageWithPrecision    float64        `json:"currentUsageWithPrecision"`
	OverageCap                   int            `json:"overageCap"`
	OverageCapWithPrecision      float64        `json:"overageCapWithPrecision"`
	Currency                     string         `json:"currency"`
	CurrentOverages              int            `json:"currentOverages"`
	CurrentOveragesWithPrecision float64        `json:"currentOveragesWithPrecision"`
	FreeTrialInfo                *FreeTrialInfo `json:"freeTrialInfo,omitempty"`
	DisplayName                  string         `json:"displayName"`
	DisplayNamePlural            string         `json:"displayNamePlural"`
}

// FreeTrialInfo represents free trial information
type FreeTrialInfo struct {
	FreeTrialExpiry           float64 `json:"freeTrialExpiry"`
	FreeTrialStatus           string  `json:"freeTrialStatus"`
	UsageLimit                int     `json:"usageLimit"`
	UsageLimitWithPrecision   float64 `json:"usageLimitWithPrecision"`
	CurrentUsage              int     `json:"currentUsage"`
	CurrentUsageWithPrecision float64 `json:"currentUsageWithPrecision"`
}

// UserInfo represents user information
type UserInfo struct {
	Email  string `json:"email"`
	UserID string `json:"userId"`
}

// OverageConfig represents overage configuration
type OverageConfig struct {
	OverageStatus string `json:"overageStatus"`
}

// SubscriptionInfo represents subscription information
type SubscriptionInfo struct {
	SubscriptionManagementTarget string `json:"subscriptionManagementTarget"`
	OverageCapability            string `json:"overageCapability"`
	SubscriptionTitle            string `json:"subscriptionTitle"`
	Type                         string `json:"type"`
	UpgradeCapability            string `json:"upgradeCapability"`
}

// KiroCreditsInfo represents the credits information for a Kiro account
type KiroCreditsInfo struct {
	AvailableCredits float64    `json:"available_credits"` // Available credits balance
	UsedCredits      float64    `json:"used_credits"`      // Used credits
	TotalCredits     float64    `json:"total_credits"`     // Total credits limit
	DaysUntilReset   int        `json:"days_until_reset"`  // Days until reset
	NextResetAt      *time.Time `json:"next_reset_at"`     // Next reset time
	UserEmail        string     `json:"user_email"`        // User email
	SubscriptionType string     `json:"subscription_type"` // Subscription type
}

// CalculateAvailableCredits calculates available credits from UsageLimits
// This follows the same logic as kiro4api's CalculateAvailableCount
func CalculateAvailableCredits(limits *UsageLimits) float64 {
	if limits == nil {
		return 0
	}

	for _, breakdown := range limits.UsageBreakdownList {
		if breakdown.ResourceType == "CREDIT" {
			var total float64

			// Base available credits
			total += breakdown.UsageLimitWithPrecision - breakdown.CurrentUsageWithPrecision

			// Add free trial credits if active
			if breakdown.FreeTrialInfo != nil && breakdown.FreeTrialInfo.FreeTrialStatus == "ACTIVE" {
				total += breakdown.FreeTrialInfo.UsageLimitWithPrecision - breakdown.FreeTrialInfo.CurrentUsageWithPrecision
			}

			if total < 0 {
				return 0
			}
			return total
		}
	}

	return 0
}

// ExtractCreditsInfo extracts KiroCreditsInfo from UsageLimits
func ExtractCreditsInfo(limits *UsageLimits) *KiroCreditsInfo {
	if limits == nil {
		return nil
	}

	info := &KiroCreditsInfo{
		DaysUntilReset:   limits.DaysUntilReset,
		UserEmail:        limits.UserInfo.Email,
		SubscriptionType: limits.SubscriptionInfo.Type,
	}

	// Calculate next reset time
	if limits.NextDateReset > 0 {
		resetTime := time.UnixMilli(int64(limits.NextDateReset))
		info.NextResetAt = &resetTime
	}

	// Find CREDIT breakdown
	for _, breakdown := range limits.UsageBreakdownList {
		if breakdown.ResourceType == "CREDIT" {
			info.TotalCredits = breakdown.UsageLimitWithPrecision
			info.UsedCredits = breakdown.CurrentUsageWithPrecision

			// Add free trial credits if active
			if breakdown.FreeTrialInfo != nil && breakdown.FreeTrialInfo.FreeTrialStatus == "ACTIVE" {
				info.TotalCredits += breakdown.FreeTrialInfo.UsageLimitWithPrecision
				info.UsedCredits += breakdown.FreeTrialInfo.CurrentUsageWithPrecision
			}

			info.AvailableCredits = info.TotalCredits - info.UsedCredits
			if info.AvailableCredits < 0 {
				info.AvailableCredits = 0
			}
			break
		}
	}

	return info
}

// usageLimitsOSName returns the OS identifier for usage limits User-Agent headers.
func usageLimitsOSName() string {
	switch runtime.GOOS {
	case "darwin":
		return "darwin#24.6.0"
	case "windows":
		return "windows#10.0"
	default:
		return "linux#6.1.0"
	}
}

// UsageLimitsFetcher fetches usage limits from CodeWhisperer API
type UsageLimitsFetcher struct {
	httpClient *http.Client
}

// NewUsageLimitsFetcher creates a new UsageLimitsFetcher
func NewUsageLimitsFetcher(client *http.Client) *UsageLimitsFetcher {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &UsageLimitsFetcher{httpClient: client}
}

// FetchUsageLimits fetches usage limits from CodeWhisperer API
func (f *UsageLimitsFetcher) FetchUsageLimits(ctx context.Context, accessToken, region, proxyURL string) (*UsageLimits, error) {
	if region == "" {
		region = "us-east-1"
	}

	// Build request URL
	baseURL := fmt.Sprintf("https://q.%s.amazonaws.com/getUsageLimits", region)
	params := url.Values{}
	params.Add("isEmailRequired", "true")
	params.Add("origin", "AI_EDITOR")
	params.Add("resourceType", "AGENTIC_REQUEST")

	requestURL := fmt.Sprintf("%s?%s", baseURL, params.Encode())

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "GET", requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request failed: %w", err)
	}

	// Set headers — dynamically generate OS identifier from runtime
	osName := usageLimitsOSName()
	kiroVersion := "1.6.0"
	machineID := GenerateRandomMachineID()
	req.Header.Set("User-Agent", fmt.Sprintf("aws-sdk-js/1.0.0 ua/2.1 os/%s lang/js md/nodejs#22.21.1 api/codewhispererruntime#1.0.0 m/N,E KiroIDE-%s-%s", osName, kiroVersion, machineID))
	req.Header.Set("x-amz-user-agent", fmt.Sprintf("aws-sdk-js/1.0.0 KiroIDE-%s-%s", kiroVersion, machineID))
	req.Header.Set("Host", fmt.Sprintf("q.%s.amazonaws.com", region))
	req.Header.Set("Connection", "close")
	req.Header.Set("amz-sdk-invocation-id", uuid.New().String())
	req.Header.Set("amz-sdk-request", "attempt=1; max=3")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	// Use proxy if configured
	client := f.httpClient
	if proxyURL != "" {
		proxyURLParsed, err := url.Parse(proxyURL)
		if err == nil {
			transport := &http.Transport{
				Proxy: http.ProxyURL(proxyURLParsed),
			}
			client = &http.Client{
				Transport: transport,
				Timeout:   30 * time.Second,
			}
		}
	}

	// Send request
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return nil, fmt.Errorf("read response failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var limits UsageLimits
	if err := json.Unmarshal(body, &limits); err != nil {
		return nil, fmt.Errorf("parse response failed: %w", err)
	}

	return &limits, nil
}
