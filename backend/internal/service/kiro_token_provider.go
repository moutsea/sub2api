package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Kiro token status constants
type KiroTokenStatus string

const (
	KiroTokenStatusActive    KiroTokenStatus = "active"
	KiroTokenStatusCooldown  KiroTokenStatus = "cooldown"
	KiroTokenStatusExhausted KiroTokenStatus = "exhausted"
	KiroTokenStatusBanned    KiroTokenStatus = "banned"
)

// Kiro refresh error types
type KiroRefreshErrorType int

const (
	KiroRefreshErrorUnknown   KiroRefreshErrorType = iota // Unknown error, don't change status
	KiroRefreshErrorNetwork                               // Network error, don't change status
	KiroRefreshErrorBanned                                // Account banned, mark as banned
	KiroRefreshErrorSuspended                             // Account suspended, mark as banned
	KiroRefreshErrorExpired                               // Refresh token expired, mark as banned
	KiroRefreshErrorExhausted                             // Quota exhausted, mark as exhausted
	KiroRefreshErrorRateLimit                             // Rate limited, enter cooldown
	KiroRefreshErrorTemporary                             // Temporary error, enter cooldown
)

// Kiro configuration constants
const (
	kiroDefaultRegion         = "us-east-1"
	kiroDefaultCooldown       = 30 * time.Second
	kiroRefreshBackoffBase    = time.Minute
	kiroRefreshBackoffMax     = 30 * time.Minute
	kiroTokenRefreshBuffer    = 60 * time.Second // Refresh token 60s before expiry
)

// KiroTokenState represents the runtime state of a Kiro account token
type KiroTokenState struct {
	AccountID     int64
	AccessToken   string
	ExpiresAt     time.Time
	Status        KiroTokenStatus
	CooldownUntil time.Time
	LastRefreshed time.Time
	mu            sync.Mutex
}

// kiroRefreshBackoffState tracks backoff state for failed refreshes
type kiroRefreshBackoffState struct {
	failures    int
	nextAttempt time.Time
}

// KiroTokenInfo represents token information returned from refresh
type KiroTokenInfo struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Region       string
	ProfileArn   string
}

// KiroRefreshRequest for Social auth type
type KiroRefreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

// KiroRefreshResponse for Social auth type
type KiroRefreshResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ExpiresIn    int    `json:"expiresIn"`
	ProfileArn   string `json:"profileArn,omitempty"`
}

// KiroIdCRefreshRequest for IdC auth type (uses camelCase for AWS OIDC)
type KiroIdCRefreshRequest struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	GrantType    string `json:"grantType"`
	RefreshToken string `json:"refreshToken"`
}

// KiroIdCRefreshResponse for IdC auth type (uses camelCase for AWS OIDC)
type KiroIdCRefreshResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ExpiresIn    int    `json:"expiresIn"`
	TokenType    string `json:"tokenType"`
}

// KiroTokenProvider manages Kiro account tokens with cooldown/refresh logic
type KiroTokenProvider struct {
	accountRepo      AccountRepository
	cache            sync.Map // accountID -> *KiroTokenState
	refreshBackoff   sync.Map // accountID -> *kiroRefreshBackoffState
	cooldownDuration time.Duration
	httpUpstream     HTTPUpstream // Use HTTPUpstream for proxy support
	mu               sync.RWMutex
}

// NewKiroTokenProvider creates a new KiroTokenProvider
func NewKiroTokenProvider(accountRepo AccountRepository, httpUpstream HTTPUpstream) *KiroTokenProvider {
	return &KiroTokenProvider{
		accountRepo:      accountRepo,
		cooldownDuration: kiroDefaultCooldown,
		httpUpstream:     httpUpstream,
	}
}

// GetAccessToken returns a valid access token for the account, refreshing if needed
func (p *KiroTokenProvider) GetAccessToken(ctx context.Context, account *Account) (string, error) {
	if account == nil || !account.IsKiro() {
		return "", fmt.Errorf("invalid Kiro account")
	}

	// Get or create token state
	state := p.getOrCreateState(account.ID)

	state.mu.Lock()
	defer state.mu.Unlock()

	// Check if in cooldown
	if state.Status == KiroTokenStatusCooldown && time.Now().Before(state.CooldownUntil) {
		return "", fmt.Errorf("account %d is in cooldown until %v", account.ID, state.CooldownUntil)
	}

	// Check if banned or exhausted
	if state.Status == KiroTokenStatusBanned {
		return "", fmt.Errorf("account %d is banned", account.ID)
	}
	if state.Status == KiroTokenStatusExhausted {
		return "", fmt.Errorf("account %d is exhausted", account.ID)
	}

	// Check if token is valid and not expired
	if state.AccessToken != "" && time.Now().Add(kiroTokenRefreshBuffer).Before(state.ExpiresAt) {
		return state.AccessToken, nil
	}

	// Need to refresh token
	tokenInfo, err := p.refreshToken(ctx, account)
	if err != nil {
		errType := p.classifyRefreshError(err)
		p.handleRefreshError(account.ID, state, errType, err)
		return "", fmt.Errorf("refresh token failed: %w", err)
	}

	// Update state with new token
	state.AccessToken = tokenInfo.AccessToken
	state.ExpiresAt = tokenInfo.ExpiresAt
	state.LastRefreshed = time.Now()
	state.Status = KiroTokenStatusActive

	// Clear backoff on success
	p.refreshBackoff.Delete(account.ID)

	// Update account credentials in database (async)
	go p.updateAccountCredentials(account.ID, tokenInfo)

	return state.AccessToken, nil
}

// getOrCreateState gets or creates a token state for an account
func (p *KiroTokenProvider) getOrCreateState(accountID int64) *KiroTokenState {
	if state, ok := p.cache.Load(accountID); ok {
		return state.(*KiroTokenState)
	}

	state := &KiroTokenState{
		AccountID: accountID,
		Status:    KiroTokenStatusActive,
	}
	actual, _ := p.cache.LoadOrStore(accountID, state)
	return actual.(*KiroTokenState)
}

// refreshToken refreshes the access token based on auth type
func (p *KiroTokenProvider) refreshToken(ctx context.Context, account *Account) (*KiroTokenInfo, error) {
	authType := account.GetKiroAuthType()

	switch authType {
	case KiroAuthMethodIdC:
		return p.refreshIdCToken(ctx, account)
	default:
		// Default to Social auth
		return p.refreshSocialToken(ctx, account)
	}
}

// refreshSocialToken refreshes token for Social auth type
func (p *KiroTokenProvider) refreshSocialToken(ctx context.Context, account *Account) (*KiroTokenInfo, error) {
	refreshToken := account.GetKiroRefreshToken()
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is empty")
	}

	region := account.GetKiroRegion()
	refreshURL := fmt.Sprintf("https://prod.%s.auth.desktop.kiro.dev/refreshToken", region)

	reqBody, err := json.Marshal(KiroRefreshRequest{
		RefreshToken: refreshToken,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request failed: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", refreshURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create request failed: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "KiroGateway")

	// Get proxy URL from account
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	resp, err := p.httpUpstream.Do(req, proxyURL, account.ID, 1)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh failed: status %d, response: %s", resp.StatusCode, string(body))
	}

	var refreshResp KiroRefreshResponse
	if err := json.Unmarshal(body, &refreshResp); err != nil {
		return nil, fmt.Errorf("parse response failed: %w", err)
	}

	if refreshResp.AccessToken == "" {
		return nil, fmt.Errorf("response missing accessToken: %s", string(body))
	}

	expiresIn := refreshResp.ExpiresIn
	if expiresIn == 0 {
		expiresIn = 3600
	}
	expiresAt := time.Now().Add(time.Duration(max(expiresIn-60, 0)) * time.Second)

	newRefreshToken := refreshToken
	if refreshResp.RefreshToken != "" {
		newRefreshToken = refreshResp.RefreshToken
	}

	return &KiroTokenInfo{
		AccessToken:  refreshResp.AccessToken,
		RefreshToken: newRefreshToken,
		ExpiresAt:    expiresAt,
		Region:       region,
		ProfileArn:   refreshResp.ProfileArn,
	}, nil
}

// refreshIdCToken refreshes token for IdC auth type via AWS OIDC
func (p *KiroTokenProvider) refreshIdCToken(ctx context.Context, account *Account) (*KiroTokenInfo, error) {
	refreshToken := account.GetKiroRefreshToken()
	clientID := account.GetKiroClientID()
	clientSecret := account.GetKiroClientSecret()
	region := account.GetKiroRegion()

	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is empty")
	}
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("client_id or client_secret is empty for IdC auth")
	}

	refreshURL := fmt.Sprintf("https://oidc.%s.amazonaws.com/token", region)
	hostHeader := fmt.Sprintf("oidc.%s.amazonaws.com", region)

	reqBody, err := json.Marshal(KiroIdCRefreshRequest{
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

	// Set IdC specific headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Host", hostHeader)
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("x-amz-user-agent", "aws-sdk-js/3.738.0 ua/2.1 os/other lang/js md/browser#unknown_unknown api/sso-oidc#3.738.0 m/E KiroIDE")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "*")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("User-Agent", "node")
	req.Header.Set("Accept-Encoding", "br, gzip, deflate")

	// Get proxy URL from account
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	resp, err := p.httpUpstream.Do(req, proxyURL, account.ID, 1)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("IdC refresh failed: status %d, response: %s", resp.StatusCode, string(body))
	}

	var idcResp KiroIdCRefreshResponse
	if err := json.Unmarshal(body, &idcResp); err != nil {
		return nil, fmt.Errorf("parse response failed: %w", err)
	}

	if idcResp.AccessToken == "" {
		return nil, fmt.Errorf("response missing access_token: %s", string(body))
	}

	expiresIn := idcResp.ExpiresIn
	if expiresIn == 0 {
		expiresIn = 3600
	}
	expiresAt := time.Now().Add(time.Duration(max(expiresIn-60, 0)) * time.Second)

	newRefreshToken := refreshToken
	if idcResp.RefreshToken != "" {
		newRefreshToken = idcResp.RefreshToken
	}

	return &KiroTokenInfo{
		AccessToken:  idcResp.AccessToken,
		RefreshToken: newRefreshToken,
		ExpiresAt:    expiresAt,
		Region:       region,
	}, nil
}

// classifyRefreshError classifies the refresh error type
func (p *KiroTokenProvider) classifyRefreshError(err error) KiroRefreshErrorType {
	if err == nil {
		return KiroRefreshErrorUnknown
	}

	errMsg := strings.ToLower(err.Error())

	// Network errors - don't change status
	if strings.Contains(errMsg, "timeout") ||
		strings.Contains(errMsg, "connection refused") ||
		strings.Contains(errMsg, "no such host") ||
		strings.Contains(errMsg, "network is unreachable") ||
		strings.Contains(errMsg, "i/o timeout") {
		return KiroRefreshErrorNetwork
	}

	// Account banned - 401 + Bad credentials
	if strings.Contains(errMsg, "401") && strings.Contains(errMsg, "bad credentials") {
		return KiroRefreshErrorBanned
	}

	// Account suspended
	if strings.Contains(errMsg, "temporarily_suspended") || strings.Contains(errMsg, "suspended") {
		return KiroRefreshErrorSuspended
	}

	// Refresh token expired or invalid
	if strings.Contains(errMsg, "invalid_grant") ||
		(strings.Contains(errMsg, "refresh token") && strings.Contains(errMsg, "expired")) {
		return KiroRefreshErrorExpired
	}

	// Rate limit - 429
	if strings.Contains(errMsg, "429") || strings.Contains(errMsg, "rate limit") || strings.Contains(errMsg, "too many requests") {
		return KiroRefreshErrorRateLimit
	}

	// Server errors - 5xx, temporary
	if strings.Contains(errMsg, "status 5") || strings.Contains(errMsg, "500") ||
		strings.Contains(errMsg, "502") || strings.Contains(errMsg, "503") || strings.Contains(errMsg, "504") {
		return KiroRefreshErrorTemporary
	}

	// 403 needs further judgment
	if strings.Contains(errMsg, "403") {
		if strings.Contains(errMsg, "forbidden") && strings.Contains(errMsg, "access denied") {
			return KiroRefreshErrorBanned
		}
		return KiroRefreshErrorTemporary
	}

	return KiroRefreshErrorUnknown
}

// handleRefreshError handles refresh errors by updating token state
func (p *KiroTokenProvider) handleRefreshError(accountID int64, state *KiroTokenState, errType KiroRefreshErrorType, err error) {
	switch errType {
	case KiroRefreshErrorBanned, KiroRefreshErrorSuspended, KiroRefreshErrorExpired:
		state.Status = KiroTokenStatusBanned
		log.Printf("[KiroToken] Account %d marked as banned: %v", accountID, err)

	case KiroRefreshErrorExhausted:
		state.Status = KiroTokenStatusExhausted
		log.Printf("[KiroToken] Account %d marked as exhausted: %v", accountID, err)

	case KiroRefreshErrorRateLimit, KiroRefreshErrorTemporary:
		state.Status = KiroTokenStatusCooldown
		state.CooldownUntil = time.Now().Add(p.cooldownDuration)
		log.Printf("[KiroToken] Account %d entered cooldown until %v: %v", accountID, state.CooldownUntil, err)

	case KiroRefreshErrorNetwork, KiroRefreshErrorUnknown:
		// Don't change status, but apply backoff for background refresh
		p.scheduleRefreshBackoff(accountID)
		log.Printf("[KiroToken] Account %d refresh failed (network/unknown), backoff applied: %v", accountID, err)
	}
}

// scheduleRefreshBackoff schedules exponential backoff for failed refreshes
func (p *KiroTokenProvider) scheduleRefreshBackoff(accountID int64) {
	var st *kiroRefreshBackoffState
	if existing, ok := p.refreshBackoff.Load(accountID); ok {
		st = existing.(*kiroRefreshBackoffState)
	} else {
		st = &kiroRefreshBackoffState{}
		p.refreshBackoff.Store(accountID, st)
	}

	st.failures++
	if st.failures > 16 {
		st.failures = 16
	}

	delay := time.Duration(float64(kiroRefreshBackoffBase) * math.Pow(2, float64(st.failures-1)))
	if delay > kiroRefreshBackoffMax {
		delay = kiroRefreshBackoffMax
	}
	st.nextAttempt = time.Now().Add(delay)
}

// MarkCooldown puts an account into cooldown state
func (p *KiroTokenProvider) MarkCooldown(accountID int64, duration time.Duration) {
	state := p.getOrCreateState(accountID)
	state.mu.Lock()
	defer state.mu.Unlock()

	if duration <= 0 {
		duration = p.cooldownDuration
	}

	state.Status = KiroTokenStatusCooldown
	state.CooldownUntil = time.Now().Add(duration)
	log.Printf("[KiroToken] Account %d marked cooldown for %v", accountID, duration)
}

// MarkExhausted marks an account as exhausted
func (p *KiroTokenProvider) MarkExhausted(accountID int64) {
	state := p.getOrCreateState(accountID)
	state.mu.Lock()
	defer state.mu.Unlock()

	state.Status = KiroTokenStatusExhausted
	log.Printf("[KiroToken] Account %d marked as exhausted", accountID)
}

// MarkBanned marks an account as banned
func (p *KiroTokenProvider) MarkBanned(accountID int64, reason string) {
	state := p.getOrCreateState(accountID)
	state.mu.Lock()
	defer state.mu.Unlock()

	state.Status = KiroTokenStatusBanned
	log.Printf("[KiroToken] Account %d marked as banned: %s", accountID, reason)
}

// MarkAvailable recovers an account to available state
func (p *KiroTokenProvider) MarkAvailable(accountID int64) {
	state := p.getOrCreateState(accountID)
	state.mu.Lock()
	defer state.mu.Unlock()

	state.Status = KiroTokenStatusActive
	p.refreshBackoff.Delete(accountID)
	log.Printf("[KiroToken] Account %d recovered to available", accountID)
}

// IsAvailable checks if an account is available for use
func (p *KiroTokenProvider) IsAvailable(accountID int64) bool {
	if state, ok := p.cache.Load(accountID); ok {
		s := state.(*KiroTokenState)
		s.mu.Lock()
		defer s.mu.Unlock()

		switch s.Status {
		case KiroTokenStatusBanned, KiroTokenStatusExhausted:
			return false
		case KiroTokenStatusCooldown:
			return time.Now().After(s.CooldownUntil)
		default:
			return true
		}
	}
	return true // No state means available
}

// GetTokenState returns the current token state for an account
func (p *KiroTokenProvider) GetTokenState(accountID int64) *KiroTokenState {
	if state, ok := p.cache.Load(accountID); ok {
		return state.(*KiroTokenState)
	}
	return nil
}

// updateAccountCredentials updates account credentials in database
func (p *KiroTokenProvider) updateAccountCredentials(accountID int64, tokenInfo *KiroTokenInfo) {
	if p.accountRepo == nil || tokenInfo == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	account, err := p.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		log.Printf("[KiroToken] Failed to get account %d for credential update: %v", accountID, err)
		return
	}

	// Update credentials
	if account.Credentials == nil {
		account.Credentials = make(map[string]any)
	}
	account.Credentials["access_token"] = tokenInfo.AccessToken
	account.Credentials["expires_at"] = tokenInfo.ExpiresAt.Format(time.RFC3339)
	if tokenInfo.RefreshToken != "" {
		account.Credentials["refresh_token"] = tokenInfo.RefreshToken
	}
	if tokenInfo.ProfileArn != "" {
		account.Credentials["profile_arn"] = tokenInfo.ProfileArn
	}

	if err := p.accountRepo.Update(ctx, account); err != nil {
		log.Printf("[KiroToken] Failed to update account %d credentials: %v", accountID, err)
	}
}

// SetCooldownDuration sets the default cooldown duration
func (p *KiroTokenProvider) SetCooldownDuration(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if d > 0 {
		p.cooldownDuration = d
	}
}

// ClearState clears the cached state for an account
func (p *KiroTokenProvider) ClearState(accountID int64) {
	p.cache.Delete(accountID)
	p.refreshBackoff.Delete(accountID)
}
