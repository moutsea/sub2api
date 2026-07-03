package kiro

import (
	"bytes"
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

// profileFetchOSName returns the OS identifier for OIDC User-Agent headers,
// matching the Rust SDK fingerprint used by AmazonQ-For-CLI (mirrors
// kiroOIDCOSName in the service layer).
func profileFetchOSName() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "windows":
		return "windows"
	default:
		return "linux"
	}
}

// idcRefreshRequest is the IdC (AWS SSO OIDC) token refresh request body.
// Uses camelCase to match the AWS OIDC endpoint contract.
type idcRefreshRequest struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	GrantType    string `json:"grantType"`
	RefreshToken string `json:"refreshToken"`
}

// idcRefreshResponse is the IdC token refresh response. Note: the OIDC
// /token endpoint does NOT return profileArn — it must be fetched separately
// via ListAvailableProfiles.
type idcRefreshResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ExpiresIn    int    `json:"expiresIn"`
	TokenType    string `json:"tokenType"`
}

// IdCAccessTokenInfo is the subset of AWS SSO OIDC refresh response needed
// while creating a Kiro IdC account before it has been persisted.
type IdCAccessTokenInfo struct {
	AccessToken  string
	RefreshToken string
}

// listProfilesResponse is the ListAvailableProfiles response.
type listProfilesResponse struct {
	Profiles []struct {
		Arn         string `json:"arn"`
		ProfileName string `json:"profileName"`
	} `json:"profiles"`
}

// httpClientWithProxy builds an HTTP client honoring an optional proxy URL.
func httpClientWithProxy(proxyURL string, timeout time.Duration) *http.Client {
	if proxyURL == "" {
		return &http.Client{Timeout: timeout}
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return &http.Client{Timeout: timeout}
	}
	return &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(parsed)},
		Timeout:   timeout,
	}
}

// RefreshIdCAccessToken exchanges IdC credentials for a fresh access token via
// the AWS SSO OIDC /token endpoint. It is stateless (does not depend on a
// persisted Account) so it can be used during account creation before the
// account is stored.
//
// The OIDC response does not include profileArn — pair this with
// FetchAvailableProfileArn to obtain the real profileArn for an IdC account.
func RefreshIdCAccessToken(ctx context.Context, clientID, clientSecret, refreshToken, region, proxyURL string) (*IdCAccessTokenInfo, error) {
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("client_id or client_secret is empty for IdC auth")
	}
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is empty")
	}
	if region == "" {
		region = DefaultRegion
	}

	refreshURL := fmt.Sprintf("https://oidc.%s.amazonaws.com/token", region)
	hostHeader := fmt.Sprintf("oidc.%s.amazonaws.com", region)
	return refreshIdCAccessTokenAt(ctx, refreshURL, hostHeader, clientID, clientSecret, refreshToken, proxyURL)
}

// refreshIdCAccessTokenAt is the URL-explicit core of RefreshIdCAccessToken,
// separated so tests can point it at an httptest server.
func refreshIdCAccessTokenAt(ctx context.Context, refreshURL, hostHeader, clientID, clientSecret, refreshToken, proxyURL string) (*IdCAccessTokenInfo, error) {
	reqBody, err := json.Marshal(idcRefreshRequest{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		GrantType:    "refresh_token",
		RefreshToken: refreshToken,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request failed: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", refreshURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create request failed: %w", err)
	}

	// Match the Rust SDK UA fingerprint (AmazonQ-For-CLI) used by the service-layer refresh.
	osName := profileFetchOSName()
	req.Header.Set("Content-Type", "application/json")
	if hostHeader != "" {
		req.Header.Set("Host", hostHeader)
	}
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("x-amz-user-agent", fmt.Sprintf("aws-sdk-rust/1.3.9 ua/2.1 api/ssooidc/1.88.0 os/%s lang/rust/1.87.0 m/E app/AmazonQ-For-CLI", osName))
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", fmt.Sprintf("aws-sdk-rust/1.3.9 os/%s lang/rust/1.87.0", osName))
	req.Header.Set("Accept-Encoding", "gzip, compress, deflate, br")

	client := httpClientWithProxy(proxyURL, 30*time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("IdC refresh failed: status %d, response: %s", resp.StatusCode, string(body))
	}

	var idcResp idcRefreshResponse
	if err := json.Unmarshal(body, &idcResp); err != nil {
		return nil, fmt.Errorf("parse response failed: %w", err)
	}
	if idcResp.AccessToken == "" {
		return nil, fmt.Errorf("response missing accessToken: %s", string(body))
	}

	return &IdCAccessTokenInfo{
		AccessToken:  idcResp.AccessToken,
		RefreshToken: idcResp.RefreshToken,
	}, nil
}

// FetchAvailableProfileArn calls the CodeWhisperer ListAvailableProfiles API and
// returns the first profile's ARN. This is how IdC accounts obtain their
// profileArn, since the OIDC refresh response never includes it.
//
// Endpoint family selection mirrors the usage-limits calls:
//   - ServiceEndpointFamilyKiro   -> POST management.<region>.kiro.dev/
//   - ServiceEndpointFamilyLegacy -> POST q.<region>.amazonaws.com/
func FetchAvailableProfileArn(ctx context.Context, accessToken, region, proxyURL string, family ServiceEndpointFamily) (string, error) {
	if accessToken == "" {
		return "", fmt.Errorf("access token is empty")
	}
	if region == "" {
		region = DefaultRegion
	}
	family = resolveFamily(family)

	// ListAvailableProfiles uses POST to the management root path, same as SetUserPreference.
	requestURL := SetUserPreferenceURL(region, family)
	hostHeader := ManagementHost(region, family)
	return fetchProfileArnAt(ctx, requestURL, hostHeader, accessToken, proxyURL)
}

// fetchProfileArnAt is the URL-explicit core of FetchAvailableProfileArn,
// separated so tests can point it at an httptest server.
func fetchProfileArnAt(ctx context.Context, requestURL, hostHeader, accessToken, proxyURL string) (string, error) {
	reqBody, err := json.Marshal(map[string]any{"maxResults": 10})
	if err != nil {
		return "", fmt.Errorf("marshal request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", requestURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	// Headers match the Smithy-generated client (same shape as SetOverageStatus).
	osName := usageLimitsOSName()
	kiroVersion := "0.11.107"
	machineID := GenerateRandomMachineID()
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonCodeWhispererService.ListAvailableProfiles")
	req.Header.Set("User-Agent", fmt.Sprintf("aws-sdk-js/1.0.0 ua/2.1 os/%s lang/js md/nodejs#22.21.1 api/codewhispererruntime#1.0.0 m/N,E KiroIDE-%s-%s", osName, kiroVersion, machineID))
	req.Header.Set("x-amz-user-agent", fmt.Sprintf("aws-sdk-js/1.0.0 KiroIDE-%s-%s", kiroVersion, machineID))
	if hostHeader != "" {
		req.Header.Set("Host", hostHeader)
	}
	req.Header.Set("amz-sdk-invocation-id", uuid.New().String())
	req.Header.Set("amz-sdk-request", "attempt=1; max=3")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	client := httpClientWithProxy(proxyURL, 30*time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ListAvailableProfiles returned %d: %s", resp.StatusCode, string(body))
	}

	var listResp listProfilesResponse
	if err := json.Unmarshal(body, &listResp); err != nil {
		return "", fmt.Errorf("parse response failed: %w", err)
	}

	for _, p := range listResp.Profiles {
		if p.Arn != "" {
			return p.Arn, nil
		}
	}

	return "", fmt.Errorf("no profile arn returned: %s", string(body))
}
