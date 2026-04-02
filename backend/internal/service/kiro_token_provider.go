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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
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
	KiroRefreshErrorExpired                               // Refresh token expired (currently unused — 401/invalid_grant treated as Temporary)
	KiroRefreshErrorExhausted                             // Quota exhausted, mark as exhausted
	KiroRefreshErrorRateLimit                             // Rate limited, enter cooldown
	KiroRefreshErrorTemporary                             // Temporary error, enter cooldown
)

// Kiro configuration constants
const (
	kiroDefaultRegion            = "us-east-1"
	kiroDefaultCooldown          = 30 * time.Second
	kiroRefreshBackoffBase       = time.Minute
	kiroRefreshBackoffMax        = 30 * time.Minute
	kiroTokenRefreshBuffer       = 60 * time.Second  // Refresh token 60s before expiry
	kiroCooldownRecoveryInterval = 30 * time.Second  // Check cooldown accounts every 30s
	kiroBannedRecoveryInterval   = 10 * time.Minute  // Check banned accounts every 10min
	kiroDBErrorRecoveryInterval  = 15 * time.Minute  // Check DB error accounts every 15min
	kiroMaxRefreshFailures       = 3                 // Mark as banned after 3 consecutive failures
)

// KiroTokenState represents the runtime state of a Kiro account token
type KiroTokenState struct {
	AccountID       int64
	AccessToken     string
	ProfileArn      string // Cached profile ARN from token refresh (avoids async DB write race)
	ExpiresAt       time.Time
	Status          KiroTokenStatus
	CooldownUntil   time.Time
	LastRefreshed   time.Time
	RefreshFailures int    // Track consecutive refresh failures
	ErrorMsg        string // Last error message
	mu              sync.Mutex
}

// kiroRefreshBackoffState tracks backoff state for failed refreshes
type kiroRefreshBackoffState struct {
	failures    int
	nextAttempt time.Time
	mu          sync.Mutex
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

	// apikey accounts use static api_key, no refresh needed
	if account.IsKiroApiKey() {
		apiKey := account.GetKiroApiKey()
		if apiKey == "" {
			return "", fmt.Errorf("api_key is empty for apikey account %d", account.ID)
		}
		return apiKey, nil
	}

	// Get or create token state
	state := p.getOrCreateState(account.ID)

	state.mu.Lock()
	defer state.mu.Unlock()

	// Initialize state from account if this is the first access
	if state.AccessToken == "" && state.ExpiresAt.IsZero() {
		p.initializeStateFromAccount(state, account)
	}

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

	// Need to refresh token — reload account from DB to get latest refresh_token.
	// The account object passed in may hold a stale refresh_token if it was rotated
	// by a previous successful refresh (written to DB async via updateAccountCredentials).
	freshAccount, reloadErr := p.accountRepo.GetByID(ctx, account.ID)
	if reloadErr != nil {
		log.Printf("[KiroToken] Account %d: failed to reload from DB, using original account: %v", account.ID, reloadErr)
		freshAccount = account
	}

	tokenInfo, err := p.refreshToken(ctx, freshAccount)
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
	state.RefreshFailures = 0
	state.ErrorMsg = ""
	if tokenInfo.ProfileArn != "" {
		state.ProfileArn = tokenInfo.ProfileArn
	}

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

// initializeStateFromAccount initializes token state from account credentials and status
func (p *KiroTokenProvider) initializeStateFromAccount(state *KiroTokenState, account *Account) {
	// Load access token from database if available
	if accessToken := account.GetKiroAccessToken(); accessToken != "" {
		state.AccessToken = accessToken
	}

	// Load profile ARN from database if available
	if profileArn := account.GetKiroProfileArn(); profileArn != "" {
		state.ProfileArn = profileArn
	}

	// Load expires_at from database if available
	if expiresAt := account.GetKiroTokenExpiresAt(); expiresAt != nil {
		state.ExpiresAt = *expiresAt
	}

	// Load status from database - map database status to internal status
	// Database uses "error" while internal state uses "banned"
	switch account.Status {
	case StatusError:
		state.Status = KiroTokenStatusBanned
		state.ErrorMsg = account.ErrorMessage
	case StatusDisabled:
		// Treat disabled as banned
		state.Status = KiroTokenStatusBanned
		state.ErrorMsg = account.ErrorMessage
	case StatusActive:
		state.Status = KiroTokenStatusActive
	default:
		// Default to active for unknown statuses
		state.Status = KiroTokenStatusActive
	}
}

// refreshToken refreshes the access token based on auth type
func (p *KiroTokenProvider) refreshToken(ctx context.Context, account *Account) (*KiroTokenInfo, error) {
	authType := account.GetKiroAuthType()

	switch authType {
	case KiroAuthMethodAPIKey:
		return nil, fmt.Errorf("apikey accounts do not support token refresh")
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
// kiroOIDCOSName returns the OS identifier for OIDC User-Agent headers,
// matching the Rust SDK fingerprint used by AmazonQ-For-CLI.
func kiroOIDCOSName() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "windows":
		return "windows"
	default:
		return "linux"
	}
}

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

	// Set IdC specific headers — use Rust SDK UA (AmazonQ-For-CLI fingerprint)
	// to match the latest upstream client behavior
	osName := kiroOIDCOSName()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Host", hostHeader)
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("x-amz-user-agent", fmt.Sprintf("aws-sdk-rust/1.3.9 ua/2.1 api/ssooidc/1.88.0 os/%s lang/rust/1.87.0 m/E app/AmazonQ-For-CLI", osName))
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", fmt.Sprintf("aws-sdk-rust/1.3.9 os/%s lang/rust/1.87.0", osName))
	req.Header.Set("Accept-Encoding", "gzip, compress, deflate, br")

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

	// Refresh endpoint 401 — refresh token may be invalid or expired.
	// "bad credentials" means permanently banned.
	// Other 401s (e.g. token rotation race, transient auth issue) are treated as
	// temporary so the account enters cooldown instead of being permanently disabled.
	// The bannedRecoveryLoop will retry with the latest refresh_token from DB.
	if strings.Contains(errMsg, "status 401") || (strings.Contains(errMsg, "401") && strings.Contains(errMsg, "unauthorized")) {
		if strings.Contains(errMsg, "bad credentials") {
			return KiroRefreshErrorBanned
		}
		return KiroRefreshErrorTemporary
	}

	// Account suspended
	if strings.Contains(errMsg, "temporarily_suspended") || strings.Contains(errMsg, "suspended") {
		return KiroRefreshErrorSuspended
	}

	// Refresh token expired or invalid — treat as temporary to allow recovery.
	// invalid_grant often means the refresh token was rotated by a concurrent request
	// and the DB already has the new one; bannedRecoveryLoop will pick it up.
	if strings.Contains(errMsg, "invalid_grant") ||
		(strings.Contains(errMsg, "refresh token") && strings.Contains(errMsg, "expired")) {
		return KiroRefreshErrorTemporary
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
		state.ErrorMsg = err.Error()
		log.Printf("[KiroToken] Account %d marked as banned: %v", accountID, err)
		// Sync status to database
		go p.updateAccountStatus(accountID, KiroTokenStatusBanned, err.Error())

	case KiroRefreshErrorExhausted:
		state.Status = KiroTokenStatusExhausted
		state.ErrorMsg = err.Error()
		log.Printf("[KiroToken] Account %d marked as exhausted: %v", accountID, err)
		// Sync status to database
		go p.updateAccountStatus(accountID, KiroTokenStatusExhausted, err.Error())

	case KiroRefreshErrorRateLimit, KiroRefreshErrorTemporary:
		if errType == KiroRefreshErrorTemporary {
			state.RefreshFailures++
		}
		// If consecutive temporary failures exceed threshold, escalate to banned.
		// This prevents zombie accounts that loop in cooldown forever when the
		// refresh_token is truly invalid (not just a transient rotation race).
		if errType == KiroRefreshErrorTemporary && state.RefreshFailures >= kiroMaxRefreshFailures {
			state.Status = KiroTokenStatusBanned
			state.ErrorMsg = fmt.Sprintf("escalated to banned after %d consecutive refresh failures: %s", state.RefreshFailures, err.Error())
			log.Printf("[KiroToken] Account %d escalated to banned after %d consecutive failures: %v", accountID, state.RefreshFailures, err)
			go p.updateAccountStatus(accountID, KiroTokenStatusBanned, state.ErrorMsg)
			return
		}
		state.Status = KiroTokenStatusCooldown
		state.CooldownUntil = time.Now().Add(p.cooldownDuration)
		state.ErrorMsg = err.Error()
		log.Printf("[KiroToken] Account %d entered cooldown until %v (failure %d/%d): %v", accountID, state.CooldownUntil, state.RefreshFailures, kiroMaxRefreshFailures, err)
		// Sync status to database (cooldown maps to active in DB)
		go p.updateAccountStatus(accountID, KiroTokenStatusCooldown, err.Error())

	case KiroRefreshErrorNetwork, KiroRefreshErrorUnknown:
		// Don't change status, but apply backoff for background refresh
		// Keep existing error message if any
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
	state.ErrorMsg = "Rate limited"
	log.Printf("[KiroToken] Account %d marked cooldown for %v", accountID, duration)

	// Sync status to database (cooldown maps to active in DB)
	go p.updateAccountStatus(accountID, KiroTokenStatusCooldown, "Rate limited")
}

// MarkExhausted marks an account as exhausted
func (p *KiroTokenProvider) MarkExhausted(accountID int64) {
	state := p.getOrCreateState(accountID)
	state.mu.Lock()
	defer state.mu.Unlock()

	state.Status = KiroTokenStatusExhausted
	state.ErrorMsg = "Quota exhausted"
	log.Printf("[KiroToken] Account %d marked as exhausted", accountID)

	// Sync status to database
	go p.updateAccountStatus(accountID, KiroTokenStatusExhausted, "Quota exhausted")
}

// MarkBanned marks an account as banned
func (p *KiroTokenProvider) MarkBanned(accountID int64, reason string) {
	state := p.getOrCreateState(accountID)
	state.mu.Lock()
	defer state.mu.Unlock()

	state.Status = KiroTokenStatusBanned
	state.ErrorMsg = reason
	log.Printf("[KiroToken] Account %d marked as banned: %s", accountID, reason)

	// Sync status to database
	go p.updateAccountStatus(accountID, KiroTokenStatusBanned, reason)
}

// InvalidateToken clears the cached access token for an account without changing its status.
// This forces the next GetAccessToken call to trigger a refresh.
// Used when upstream returns 401 (token expired) — not a ban, just a stale token.
func (p *KiroTokenProvider) InvalidateToken(accountID int64) {
	state := p.getOrCreateState(accountID)
	state.mu.Lock()
	defer state.mu.Unlock()
	state.AccessToken = ""
	// Set ExpiresAt to past (not zero) to avoid initializeStateFromAccount reloading the stale token from DB.
	// GetAccessToken checks (AccessToken=="" && ExpiresAt.IsZero()) to decide whether to re-init from DB;
	// a non-zero past time skips that branch and falls through to the expiry check, triggering a refresh.
	state.ExpiresAt = time.Unix(0, 0)
	log.Printf("[KiroToken] Account %d token invalidated (will refresh on next use)", accountID)
}

// MarkAvailable recovers an account to available state
func (p *KiroTokenProvider) MarkAvailable(accountID int64) {
	state := p.getOrCreateState(accountID)
	state.mu.Lock()
	defer state.mu.Unlock()

	state.Status = KiroTokenStatusActive
	state.RefreshFailures = 0
	state.ErrorMsg = ""
	p.refreshBackoff.Delete(accountID)
	log.Printf("[KiroToken] Account %d recovered to available", accountID)

	// Sync status to database
	go p.updateAccountStatus(accountID, KiroTokenStatusActive, "")
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

// GetProfileArn returns the cached profile ARN for an account.
// This avoids the race condition where async DB write from token refresh
// hasn't completed yet when the request reads profileArn.
func (p *KiroTokenProvider) GetProfileArn(accountID int64) string {
	if state, ok := p.cache.Load(accountID); ok {
		s := state.(*KiroTokenState)
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.ProfileArn
	}
	return ""
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

	// Set token version to prevent cache race condition
	account.Credentials[TokenVersionKey] = strconv.FormatInt(time.Now().UnixMilli(), 10)

	// Update status to active and clear error message on successful refresh
	account.Status = StatusActive
	account.ErrorMessage = ""

	if err := p.accountRepo.Update(ctx, account); err != nil {
		log.Printf("[KiroToken] Failed to update account %d credentials: %v", accountID, err)
	}
}

// updateAccountStatus updates account status in database
func (p *KiroTokenProvider) updateAccountStatus(accountID int64, status KiroTokenStatus, errorMsg string) {
	if p.accountRepo == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	account, err := p.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		log.Printf("[KiroToken] Failed to get account %d for status update: %v", accountID, err)
		return
	}

	// apikey accounts: status is always active, never update to error
	if account.IsKiroApiKey() {
		log.Printf("[KiroToken] Account %d is apikey type, skipping status update (status=%s)", accountID, status)
		return
	}

	// Map internal status to database status
	switch status {
	case KiroTokenStatusBanned:
		account.Status = StatusError
		account.ErrorMessage = errorMsg
	case KiroTokenStatusExhausted:
		account.Status = StatusError
		account.ErrorMessage = errorMsg
	case KiroTokenStatusCooldown:
		// Cooldown is a temporary state, keep as active in database
		account.Status = StatusActive
		// Clear error message for cooldown (it's temporary, not an error)
		account.ErrorMessage = ""
	case KiroTokenStatusActive:
		account.Status = StatusActive
		account.ErrorMessage = ""
	}

	if err := p.accountRepo.Update(ctx, account); err != nil {
		log.Printf("[KiroToken] Failed to update account %d status: %v", accountID, err)
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

// ForceRefreshWithRetry attempts to recover at least one account from a list of unavailable Kiro accounts.
// It tries each account up to maxRetries times to force-refresh the token.
// Returns the first successfully recovered account, or an error if all attempts fail.
func (p *KiroTokenProvider) ForceRefreshWithRetry(ctx context.Context, accounts []Account, maxRetries int) (*Account, error) {
	if len(accounts) == 0 {
		return nil, fmt.Errorf("no accounts to retry")
	}
	if maxRetries <= 0 {
		maxRetries = 3
	}

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		for i := range accounts {
			acc := &accounts[i]
			if !acc.IsKiro() || acc.IsKiroApiKey() {
				continue
			}

			state := p.getOrCreateState(acc.ID)
			state.mu.Lock()

			// Skip permanently banned accounts (bad credentials / suspended / expired grant)
			if state.Status == KiroTokenStatusBanned {
				state.mu.Unlock()
				continue
			}

			// Reload account from DB to get latest refresh_token
			freshAcc, reloadErr := p.accountRepo.GetByID(ctx, acc.ID)
			if reloadErr != nil {
				log.Printf("[KiroToken] Force retry: failed to reload account %d from DB: %v", acc.ID, reloadErr)
				freshAcc = acc
			}

			// Attempt refresh regardless of cooldown/exhausted status
			tokenInfo, err := p.refreshToken(ctx, freshAcc)
			if err != nil {
				lastErr = err
				log.Printf("[KiroToken] Force retry attempt %d/%d for account %d failed: %v", attempt, maxRetries, acc.ID, err)
				state.mu.Unlock()
				continue
			}

			// Success - recover the account
			state.AccessToken = tokenInfo.AccessToken
			state.ExpiresAt = tokenInfo.ExpiresAt
			state.LastRefreshed = time.Now()
			state.Status = KiroTokenStatusActive
			state.RefreshFailures = 0
			state.ErrorMsg = ""
			state.mu.Unlock()

			p.refreshBackoff.Delete(acc.ID)
			go p.updateAccountCredentials(acc.ID, tokenInfo)

			log.Printf("[KiroToken] Account %d recovered via force retry (attempt %d/%d)", acc.ID, attempt, maxRetries)
			return acc, nil
		}
	}

	if lastErr == nil {
		return nil, fmt.Errorf("all %d accounts are permanently banned, no recovery possible", len(accounts))
	}
	return nil, fmt.Errorf("all %d retry attempts failed for %d accounts: %w", maxRetries, len(accounts), lastErr)
}

// ForceRefreshToken forces a token refresh regardless of current state
// This is used for manual refresh operations from admin panel
func (p *KiroTokenProvider) ForceRefreshToken(ctx context.Context, account *Account) error {
	if account == nil || !account.IsKiro() {
		return fmt.Errorf("invalid Kiro account")
	}

	// apikey accounts use static key, no refresh needed
	// But if account is in error state, recover it to active
	if account.IsKiroApiKey() {
		if account.Status == StatusError {
			account.Status = StatusActive
			account.ErrorMessage = ""
			if err := p.accountRepo.Update(ctx, account); err != nil {
				return fmt.Errorf("failed to recover apikey account status: %w", err)
			}
			// Update in-memory cache
			state := p.getOrCreateState(account.ID)
			state.mu.Lock()
			state.Status = KiroTokenStatusActive
			state.ErrorMsg = ""
			state.mu.Unlock()
			log.Printf("[KiroToken] API Key account %d manually recovered from error state", account.ID)
		}
		return nil
	}

	// Get or create token state
	state := p.getOrCreateState(account.ID)

	state.mu.Lock()
	defer state.mu.Unlock()

	// Attempt refresh regardless of current status
	tokenInfo, err := p.refreshToken(ctx, account)
	if err != nil {
		errType := p.classifyRefreshError(err)
		p.handleRefreshError(account.ID, state, errType, err)
		return fmt.Errorf("refresh token failed: %w", err)
	}

	// Update state with new token
	state.AccessToken = tokenInfo.AccessToken
	state.ExpiresAt = tokenInfo.ExpiresAt
	state.LastRefreshed = time.Now()
	state.Status = KiroTokenStatusActive
	state.RefreshFailures = 0
	state.ErrorMsg = ""
	if tokenInfo.ProfileArn != "" {
		state.ProfileArn = tokenInfo.ProfileArn
	}

	// Clear backoff on success
	p.refreshBackoff.Delete(account.ID)

	// Update account credentials in database synchronously — the admin is waiting
	// for the result, and async update can cause split-brain (memory active, DB error).
	p.updateAccountCredentials(account.ID, tokenInfo)

	return nil
}

// Start starts background recovery tasks
func (p *KiroTokenProvider) Start() {
	log.Println("[KiroToken] Starting background recovery tasks...")
	go p.cooldownRecoveryLoop()
	go p.bannedRecoveryLoop()
	go p.dbDeletedRecoveryLoop()
	log.Println("[KiroToken] Background recovery tasks started")
}

// Stop stops background recovery tasks
func (p *KiroTokenProvider) Stop() {
	// TODO: Implement graceful shutdown using context.Context
	// Currently no cleanup needed as goroutines will exit when program exits
	log.Println("[KiroToken] Stopping background recovery tasks...")
}

// cooldownRecoveryLoop periodically checks cooldown accounts and attempts to recover them
func (p *KiroTokenProvider) cooldownRecoveryLoop() {
	ticker := time.NewTicker(kiroCooldownRecoveryInterval)
	defer ticker.Stop()

	for range ticker.C {
		var cooldownAccounts []int64

		// Collect cooldown accounts whose cooldown period has expired
		p.cache.Range(func(key, value any) bool {
			accountID := key.(int64)
			state := value.(*KiroTokenState)
			state.mu.Lock()
			if state.Status == KiroTokenStatusCooldown && time.Now().After(state.CooldownUntil) {
				cooldownAccounts = append(cooldownAccounts, accountID)
			}
			state.mu.Unlock()
			return true
		})

		if len(cooldownAccounts) > 0 {
			log.Printf("[KiroToken] Attempting to recover %d cooldown accounts", len(cooldownAccounts))
			for _, accountID := range cooldownAccounts {
				p.attemptRecovery(accountID)
			}
		}
	}
}

// bannedRecoveryLoop periodically checks banned accounts (in-memory) and error accounts (DB) and attempts to recover them.
func (p *KiroTokenProvider) bannedRecoveryLoop() {
	ticker := time.NewTicker(kiroBannedRecoveryInterval)
	defer ticker.Stop()

	for range ticker.C {
		// 1. Collect in-memory banned accounts (excluding exhausted - those need manual intervention)
		var bannedAccounts []int64
		p.cache.Range(func(key, value any) bool {
			accountID := key.(int64)
			state := value.(*KiroTokenState)
			state.mu.Lock()
			if state.Status == KiroTokenStatusBanned {
				bannedAccounts = append(bannedAccounts, accountID)
			}
			state.mu.Unlock()
			return true
		})

		if len(bannedAccounts) > 0 {
			log.Printf("[KiroToken] Attempting to recover %d in-memory banned accounts", len(bannedAccounts))
			for _, accountID := range bannedAccounts {
				p.attemptRecovery(accountID)
			}
		}

		// 2. Query DB for error Kiro accounts that may not be in memory cache
		//    (e.g. after process restart, or accounts that were never loaded).
		p.recoverDBErrorAccounts()
	}
}

// recoverDBErrorAccounts queries error (non-deleted) Kiro accounts from database and attempts to recover them.
func (p *KiroTokenProvider) recoverDBErrorAccounts() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	errorAccounts, err := p.accountRepo.ListErrorByPlatform(ctx, PlatformKiro)
	if err != nil {
		log.Printf("[KiroToken] Failed to query error accounts from database: %v", err)
		return
	}
	if len(errorAccounts) == 0 {
		return
	}

	log.Printf("[KiroToken] Found %d error Kiro accounts in database, attempting recovery...", len(errorAccounts))
	recovered := 0
	failed := 0

	for i := range errorAccounts {
		account := &errorAccounts[i]

		// apikey accounts: directly recover to active (no token refresh needed)
		if account.IsKiroApiKey() {
			account.Status = StatusActive
			account.ErrorMessage = ""
			if err := p.accountRepo.Update(ctx, account); err != nil {
				log.Printf("[KiroToken] Failed to recover apikey account %d (%s): %v", account.ID, account.Name, err)
				failed++
				continue
			}
			// Update in-memory cache
			if state, ok := p.cache.Load(account.ID); ok {
				s := state.(*KiroTokenState)
				s.mu.Lock()
				s.Status = KiroTokenStatusActive
				s.ErrorMsg = ""
				s.mu.Unlock()
			}
			recovered++
			log.Printf("[KiroToken] API Key account %d (%s) recovered successfully from error state", account.ID, account.Name)
			continue
		}

		tokenInfo, err := p.refreshToken(ctx, account)
		if err != nil {
			errType := p.classifyRefreshError(err)
			log.Printf("[KiroToken] Account %d (%s) recovery failed (type=%d): %v", account.ID, account.Name, errType, err)
			failed++
			continue
		}

		p.updateAccountCredentials(account.ID, tokenInfo)
		p.updateCacheOnRecovery(account.ID, tokenInfo)
		recovered++
		log.Printf("[KiroToken] Account %d (%s) recovered successfully from error state", account.ID, account.Name)
	}

	if recovered > 0 || failed > 0 {
		log.Printf("[KiroToken] DB error recovery completed: %d recovered, %d failed out of %d", recovered, failed, len(errorAccounts))
	}
}

// attemptRecovery attempts to refresh token for an account and recover it
func (p *KiroTokenProvider) attemptRecovery(accountID int64) {
	// Check backoff state
	if existing, ok := p.refreshBackoff.Load(accountID); ok {
		st := existing.(*kiroRefreshBackoffState)
		st.mu.Lock()
		shouldSkip := time.Now().Before(st.nextAttempt)
		st.mu.Unlock()
		if shouldSkip {
			return // Still in backoff
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get account from database
	account, err := p.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		log.Printf("[KiroToken] Failed to get account %d for recovery: %v", accountID, err)
		return
	}

	if account == nil || !account.IsKiro() {
		// Account no longer exists or is not Kiro, remove from cache
		p.cache.Delete(accountID)
		p.refreshBackoff.Delete(accountID)
		return
	}

	// Get state
	stateVal, ok := p.cache.Load(accountID)
	if !ok {
		return
	}
	state := stateVal.(*KiroTokenState)

	// Attempt refresh
	tokenInfo, err := p.refreshToken(ctx, account)
	if err != nil {
		errType := p.classifyRefreshError(err)
		state.mu.Lock()

		// Only count non-RateLimit errors toward the escalation threshold.
		// 429 is normal throttling and should not pollute the failure counter.
		if errType != KiroRefreshErrorRateLimit {
			state.RefreshFailures++
		}

		// Handle based on error type
		switch errType {
		case KiroRefreshErrorBanned, KiroRefreshErrorSuspended, KiroRefreshErrorExpired:
			// Permanent error, keep banned
			state.Status = KiroTokenStatusBanned
			state.ErrorMsg = err.Error()
			log.Printf("[KiroToken] Account %d recovery failed (permanent): %v", accountID, err)
			errMsg := state.ErrorMsg
			state.mu.Unlock()
			go p.updateAccountStatus(accountID, KiroTokenStatusBanned, errMsg)
			return
		case KiroRefreshErrorNetwork, KiroRefreshErrorUnknown:
			// Network/unknown error, apply backoff
			p.scheduleRefreshBackoff(accountID)
			log.Printf("[KiroToken] Account %d recovery attempt %d/%d failed (network/unknown): %v", accountID, state.RefreshFailures, kiroMaxRefreshFailures, err)
		default:
			// Rate limit or temporary, back to cooldown
			log.Printf("[KiroToken] Account %d back to cooldown (failure %d/%d): %v", accountID, state.RefreshFailures, kiroMaxRefreshFailures, err)
		}

		// Escalate to banned if consecutive failures exceed threshold
		if state.RefreshFailures >= kiroMaxRefreshFailures {
			state.Status = KiroTokenStatusBanned
			state.ErrorMsg = fmt.Sprintf("recovery failed %d times: %v", state.RefreshFailures, err)
			log.Printf("[KiroToken] Account %d escalated to banned after %d recovery failures", accountID, state.RefreshFailures)
			errMsg := state.ErrorMsg
			state.mu.Unlock()
			go p.updateAccountStatus(accountID, KiroTokenStatusBanned, errMsg)
			return
		}

		// Not yet escalated — enter cooldown
		state.Status = KiroTokenStatusCooldown
		state.CooldownUntil = time.Now().Add(p.cooldownDuration)
		errMsg := err.Error()
		state.mu.Unlock()
		go p.updateAccountStatus(accountID, KiroTokenStatusCooldown, errMsg)
		return
	}

	// Success! Update state
	state.mu.Lock()
	state.AccessToken = tokenInfo.AccessToken
	state.ExpiresAt = tokenInfo.ExpiresAt
	state.Status = KiroTokenStatusActive
	state.LastRefreshed = time.Now()
	state.RefreshFailures = 0
	state.ErrorMsg = ""
	state.mu.Unlock()

	// Clear backoff
	p.refreshBackoff.Delete(accountID)

	// Update database synchronously — attemptRecovery runs in a background goroutine,
	// so blocking is fine. Async update here caused DB status to remain "error" while
	// the in-memory state was already "active", leading to a persistent split-brain.
	p.updateAccountCredentials(accountID, tokenInfo)

	log.Printf("[KiroToken] Account %d recovered successfully", accountID)
}

// dbDeletedRecoveryLoop periodically checks soft-deleted Kiro accounts in database and attempts to recover them.
// Error accounts are handled by bannedRecoveryLoop instead.
func (p *KiroTokenProvider) dbDeletedRecoveryLoop() {
	ticker := time.NewTicker(kiroDBErrorRecoveryInterval)
	defer ticker.Stop()

	for range ticker.C {
		p.recoverDBDeletedAccounts()
	}
}

// recoverDBDeletedAccounts queries soft-deleted Kiro accounts from database and attempts to recover them.
// Refresh token, verify credits, restore from soft-delete, and reactivate (group bindings are preserved).
func (p *KiroTokenProvider) recoverDBDeletedAccounts() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	deletedAccounts, err := p.accountRepo.ListDeletedByPlatform(ctx, PlatformKiro)
	if err != nil {
		log.Printf("[KiroToken] Failed to query deleted accounts from database: %v", err)
		return
	}
	if len(deletedAccounts) == 0 {
		return
	}

	log.Printf("[KiroToken] Found %d deleted Kiro accounts in database, attempting recovery...", len(deletedAccounts))
	recovered := 0
	failed := 0

	for i := range deletedAccounts {
		account := &deletedAccounts[i]

		if account.IsKiroApiKey() {
			continue
		}

		tokenInfo, err := p.refreshToken(ctx, account)
		if err != nil {
			errType := p.classifyRefreshError(err)
			log.Printf("[KiroToken] Deleted account %d (%s) recovery failed (type=%d): %v", account.ID, account.Name, errType, err)
			failed++
			continue
		}

		// Verify usage limits before restoring — only revive accounts that have valid credits
		proxyURL := ""
		if account.ProxyID != nil && account.Proxy != nil {
			proxyURL = account.Proxy.URL()
		}
		fetcher := kiro.NewUsageLimitsFetcher(nil)
		limits, err := fetcher.FetchUsageLimits(ctx, tokenInfo.AccessToken, "us-east-1", proxyURL)
		if err != nil {
			log.Printf("[KiroToken] Deleted account %d (%s) usage fetch failed, skipping restore: %v", account.ID, account.Name, err)
			failed++
			continue
		}
		creditsInfo := kiro.ExtractCreditsInfo(limits)
		if creditsInfo == nil || creditsInfo.TotalCredits <= 0 || creditsInfo.AvailableCredits <= 0 {
			log.Printf("[KiroToken] Deleted account %d (%s) has no valid credits (total=%.2f, available=%.2f), skipping restore",
				account.ID, account.Name,
				func() float64 { if creditsInfo != nil { return creditsInfo.TotalCredits }; return 0 }(),
				func() float64 { if creditsInfo != nil { return creditsInfo.AvailableCredits }; return 0 }())
			failed++
			continue
		}

		// Restore from soft-delete (clears deleted_at, sets status=active)
		if err := p.accountRepo.RestoreAccount(ctx, account.ID); err != nil {
			log.Printf("[KiroToken] Failed to restore deleted account %d (%s): %v", account.ID, account.Name, err)
			failed++
			continue
		}

		// Update credentials after restore
		p.updateAccountCredentials(account.ID, tokenInfo)
		p.updateCacheOnRecovery(account.ID, tokenInfo)
		recovered++
		log.Printf("[KiroToken] Deleted account %d (%s) restored successfully (credits=%.2f/%.2f)", account.ID, account.Name, creditsInfo.AvailableCredits, creditsInfo.TotalCredits)
	}

	if recovered > 0 || failed > 0 {
		log.Printf("[KiroToken] DB deleted recovery completed: %d recovered, %d failed out of %d", recovered, failed, len(deletedAccounts))
	}
}

// updateCacheOnRecovery updates the in-memory cache after a successful token recovery.
// Uses getOrCreateState to ensure the cache entry exists — critical for restored
// (previously deleted) accounts whose cache entry may have been evicted.
func (p *KiroTokenProvider) updateCacheOnRecovery(accountID int64, tokenInfo *KiroTokenInfo) {
	s := p.getOrCreateState(accountID)
	s.mu.Lock()
	s.AccessToken = tokenInfo.AccessToken
	s.ExpiresAt = tokenInfo.ExpiresAt
	s.Status = KiroTokenStatusActive
	s.LastRefreshed = time.Now()
	s.RefreshFailures = 0
	s.ErrorMsg = ""
	s.mu.Unlock()
	p.refreshBackoff.Delete(accountID)
}
