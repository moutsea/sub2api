package service

import (
	"context"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/imroc/req/v3"
)

type openAIAccountInfoClientFactory func(proxyURL string) (*req.Client, error)

var chatGPTAccountsCheckURL = "https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27"

func defaultOpenAIAccountInfoClient(proxyURL string) (*req.Client, error) {
	client := req.C().
		SetTimeout(15 * time.Second).
		ImpersonateChrome()

	if proxyURL = strings.TrimSpace(proxyURL); proxyURL != "" {
		if err := validateOpenAIAccountInfoProxyURL(proxyURL); err != nil {
			return nil, fmt.Errorf("invalid proxy URL")
		}
		client.SetProxyURL(proxyURL)
	}

	return client, nil
}

func validateOpenAIAccountInfoProxyURL(proxyURL string) error {
	parsed, err := url.Parse(proxyURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid proxy URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return fmt.Errorf("unsupported proxy scheme")
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" || strings.EqualFold(host, "localhost") {
		return fmt.Errorf("disallowed proxy host")
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.IsLoopback() || addr.IsUnspecified() {
			return fmt.Errorf("disallowed proxy host")
		}
	}
	return nil
}

func fetchChatGPTAccountPlanType(ctx context.Context, clientFactory openAIAccountInfoClientFactory, accessToken, proxyURL, orgID string) string {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" || clientFactory == nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	client, err := clientFactory(proxyURL)
	if err != nil || client == nil {
		return ""
	}

	var result struct {
		Accounts map[string]any `json:"accounts"`
	}
	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+accessToken).
		SetHeader("Origin", "https://chatgpt.com").
		SetHeader("Referer", "https://chatgpt.com/").
		SetHeader("Accept", "application/json").
		SetSuccessResult(&result).
		Get(chatGPTAccountsCheckURL)

	if err != nil || resp == nil || !resp.IsSuccessState() {
		return ""
	}

	return selectChatGPTAccountPlanType(result.Accounts, orgID)
}

func selectChatGPTAccountPlanType(accounts map[string]any, orgID string) string {
	if len(accounts) == 0 {
		return ""
	}

	orgID = strings.TrimSpace(orgID)
	if orgID != "" {
		if account, ok := asStringAnyMap(accounts[orgID]); ok {
			if isUsableChatGPTAccountCandidate(account, time.Now()) {
				if planType := extractChatGPTAccountPlanType(account); planType != "" {
					return planType
				}
			}
		}
	}

	var defaultPlanType string
	var paidPlanType string
	var anyPlanType string

	for _, rawAccount := range accounts {
		account, ok := asStringAnyMap(rawAccount)
		if !ok {
			continue
		}
		if !isUsableChatGPTAccountCandidate(account, time.Now()) {
			continue
		}

		planType := extractChatGPTAccountPlanType(account)
		if planType == "" {
			continue
		}
		if anyPlanType == "" {
			anyPlanType = planType
		}
		if defaultPlanType == "" && isDefaultChatGPTAccount(account) {
			defaultPlanType = planType
		}
		if paidPlanType == "" && !strings.EqualFold(planType, "free") {
			paidPlanType = planType
		}
	}

	switch {
	case defaultPlanType != "":
		return defaultPlanType
	case paidPlanType != "":
		return paidPlanType
	default:
		return anyPlanType
	}
}

func isUsableChatGPTAccountCandidate(account map[string]any, now time.Time) bool {
	if account == nil || hasChatGPTAccountInactiveMarker(account) {
		return false
	}
	if accountInfo, ok := asStringAnyMap(account["account"]); ok && hasChatGPTAccountInactiveMarker(accountInfo) {
		return false
	}
	entitlement, ok := asStringAnyMap(account["entitlement"])
	if !ok {
		return true
	}
	expiresAt, _ := entitlement["expires_at"].(string)
	if strings.TrimSpace(expiresAt) == "" {
		return true
	}
	expiry, err := time.Parse(time.RFC3339, expiresAt)
	return err != nil || expiry.After(now)
}

func hasChatGPTAccountInactiveMarker(account map[string]any) bool {
	for _, key := range []string{"deactivated", "is_deactivated", "disabled", "is_disabled"} {
		if value, ok := account[key].(bool); ok && value {
			return true
		}
	}
	for _, key := range []string{"deactivated_at", "disabled_at", "deleted_at"} {
		if value, ok := account[key].(string); ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	for _, key := range []string{"status", "state"} {
		value, _ := account[key].(string)
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "deactivated", "disabled", "deleted", "inactive", "suspended":
			return true
		}
	}
	return false
}

func extractChatGPTAccountPlanType(account map[string]any) string {
	if accountInfo, ok := asStringAnyMap(account["account"]); ok {
		if planType, ok := accountInfo["plan_type"].(string); ok && strings.TrimSpace(planType) != "" {
			return strings.TrimSpace(planType)
		}
	}
	if entitlement, ok := asStringAnyMap(account["entitlement"]); ok {
		if planType, ok := entitlement["subscription_plan"].(string); ok && strings.TrimSpace(planType) != "" {
			return strings.TrimSpace(planType)
		}
	}
	return ""
}

func isDefaultChatGPTAccount(account map[string]any) bool {
	accountInfo, ok := asStringAnyMap(account["account"])
	if !ok {
		return false
	}
	isDefault, _ := accountInfo["is_default"].(bool)
	return isDefault
}

func asStringAnyMap(value any) (map[string]any, bool) {
	result, ok := value.(map[string]any)
	return result, ok
}
