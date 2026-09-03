package service

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

// GrokOAuthClient abstracts the xAI authorization-code and refresh endpoints.
type GrokOAuthClient interface {
	ExchangeCode(ctx context.Context, code, codeVerifier, codeChallenge, redirectURI, proxyURL, clientID string) (*xai.TokenResponse, error)
	RefreshToken(ctx context.Context, refreshToken, proxyURL, clientID string) (*xai.TokenResponse, error)
}

const grokDefaultAccessTokenTTL = 6 * time.Hour

type GrokOAuthService struct {
	sessionStore *xai.SessionStore
	proxyRepo    ProxyRepository
	oauthClient  GrokOAuthClient
}

func NewGrokOAuthService(proxyRepo ProxyRepository, oauthClient GrokOAuthClient) *GrokOAuthService {
	return &GrokOAuthService{sessionStore: xai.NewSessionStore(), proxyRepo: proxyRepo, oauthClient: oauthClient}
}

type GrokAuthURLResult struct {
	AuthURL   string `json:"auth_url"`
	SessionID string `json:"session_id"`
	State     string `json:"state"`
}

func (s *GrokOAuthService) GenerateAuthURL(ctx context.Context, proxyID *int64, redirectURI string) (*GrokAuthURLResult, error) {
	if s == nil || s.sessionStore == nil {
		return nil, errors.New("grok oauth service is not configured")
	}
	state, err := xai.GenerateState()
	if err != nil {
		return nil, fmt.Errorf("generate oauth state: %w", err)
	}
	nonce, err := xai.GenerateNonce()
	if err != nil {
		return nil, fmt.Errorf("generate oauth nonce: %w", err)
	}
	verifier, err := xai.GenerateCodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("generate oauth verifier: %w", err)
	}
	sessionID, err := xai.GenerateSessionID()
	if err != nil {
		return nil, fmt.Errorf("generate oauth session: %w", err)
	}
	proxyURL, err := s.proxyURL(ctx, proxyID)
	if err != nil {
		return nil, err
	}
	redirectURI = xai.EffectiveRedirectURI(redirectURI)
	challenge := xai.GenerateCodeChallenge(verifier)
	s.sessionStore.Set(sessionID, &xai.OAuthSession{
		State: state, Nonce: nonce, CodeVerifier: verifier, CodeChallenge: challenge,
		ClientID: xai.EffectiveClientID(), Scope: xai.EffectiveScope(),
		ProxyURL: proxyURL, RedirectURI: redirectURI, CreatedAt: time.Now(),
	})
	return &GrokAuthURLResult{AuthURL: xai.BuildAuthorizationURL(state, challenge, redirectURI, nonce), SessionID: sessionID, State: state}, nil
}

type GrokExchangeCodeInput struct {
	SessionID   string
	Code        string
	State       string
	RedirectURI string
	ProxyID     *int64
}

type GrokTokenInfo struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int64  `json:"expires_in"`
	ExpiresAt    int64  `json:"expires_at"`
	ClientID     string `json:"client_id,omitempty"`
	Scope        string `json:"scope,omitempty"`
	Email        string `json:"email,omitempty"`
}

func (s *GrokOAuthService) ExchangeCode(ctx context.Context, input *GrokExchangeCodeInput) (*GrokTokenInfo, error) {
	if input == nil || strings.TrimSpace(input.SessionID) == "" {
		return nil, errors.New("session_id is required")
	}
	if s == nil || s.sessionStore == nil {
		return nil, errors.New("grok oauth service is not configured")
	}
	if s.oauthClient == nil {
		return nil, errors.New("grok oauth client is not configured")
	}

	session, ok := s.sessionStore.Get(input.SessionID)
	if !ok {
		return nil, errors.New("session not found or expired")
	}
	parsed := xai.ParseAuthorizationInput(input.Code)
	code := strings.TrimSpace(parsed.Code)
	if code == "" {
		return nil, errors.New("authorization code is required")
	}
	state := strings.TrimSpace(input.State)
	if state == "" {
		state = strings.TrimSpace(parsed.State)
	}
	if state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(session.State)) != 1 {
		return nil, errors.New("invalid oauth state")
	}
	if input.RedirectURI != "" && input.RedirectURI != session.RedirectURI {
		return nil, errors.New("redirect_uri does not match oauth session")
	}
	proxyURL := session.ProxyURL
	if input.ProxyID != nil {
		var err error
		proxyURL, err = s.proxyURL(ctx, input.ProxyID)
		if err != nil {
			return nil, err
		}
	}
	var consumed bool
	session, consumed = s.sessionStore.ConsumeSession(input.SessionID)
	if !consumed {
		return nil, errors.New("session not found or already consumed")
	}
	tokenResp, err := s.oauthClient.ExchangeCode(ctx, code, session.CodeVerifier, session.CodeChallenge, session.RedirectURI, proxyURL, session.ClientID)
	if err != nil {
		return nil, err
	}
	if tokenResp == nil {
		return nil, errors.New("grok oauth code exchange returned an empty response")
	}
	if session.Nonce != "" {
		if nonce := parseGrokJWTClaim(tokenResp.IDToken, "nonce"); nonce == "" || subtle.ConstantTimeCompare([]byte(nonce), []byte(session.Nonce)) != 1 {
			return nil, errors.New("invalid oauth nonce")
		}
	}
	return s.tokenInfoFromResponse(tokenResp, session.ClientID, nil), nil
}

func (s *GrokOAuthService) RefreshToken(ctx context.Context, refreshToken, proxyURL, clientID string) (*GrokTokenInfo, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, errors.New("refresh_token is required")
	}
	if s == nil || s.oauthClient == nil {
		return nil, errors.New("grok oauth client is not configured")
	}
	resp, err := s.oauthClient.RefreshToken(ctx, refreshToken, proxyURL, clientID)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errors.New("grok oauth token refresh returned an empty response")
	}
	info := s.tokenInfoFromResponse(resp, clientID, nil)
	if info.RefreshToken == "" {
		info.RefreshToken = refreshToken
	}
	return info, nil
}

func (s *GrokOAuthService) RefreshAccountToken(ctx context.Context, account *Account) (*GrokTokenInfo, error) {
	if account == nil || !account.IsGrokOAuth() {
		return nil, errors.New("account is not a Grok OAuth account")
	}
	proxyURL, err := s.proxyURL(ctx, account.ProxyID)
	if err != nil {
		return nil, err
	}
	return s.RefreshToken(ctx, account.GetGrokRefreshToken(), proxyURL, account.GetCredential("client_id"))
}

func (s *GrokOAuthService) BuildAccountCredentials(info *GrokTokenInfo) map[string]any {
	if info == nil {
		return nil
	}
	creds := map[string]any{
		"access_token": info.AccessToken,
		"expires_at":   time.Unix(info.ExpiresAt, 0).UTC().Format(time.RFC3339),
	}
	if info.RefreshToken != "" {
		creds["refresh_token"] = info.RefreshToken
	}
	if info.TokenType != "" {
		creds["token_type"] = info.TokenType
	}
	if info.IDToken != "" {
		creds["id_token"] = info.IDToken
	}
	if info.ClientID != "" {
		creds["client_id"] = info.ClientID
	}
	if info.Scope != "" {
		creds["scope"] = info.Scope
	}
	if info.Email != "" {
		creds["email"] = info.Email
	}
	return creds
}

func (s *GrokOAuthService) Stop() {
	if s != nil && s.sessionStore != nil {
		s.sessionStore.Stop()
	}
}

func (s *GrokOAuthService) tokenInfoFromResponse(resp *xai.TokenResponse, clientID string, existing map[string]any) *GrokTokenInfo {
	if resp == nil {
		return nil
	}
	expiresIn := resp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = int64(grokDefaultAccessTokenTTL.Seconds())
	}
	info := &GrokTokenInfo{AccessToken: resp.AccessToken, RefreshToken: resp.RefreshToken, IDToken: resp.IDToken, TokenType: resp.TokenType, ExpiresIn: expiresIn, ExpiresAt: time.Now().Add(time.Duration(expiresIn) * time.Second).Unix(), ClientID: strings.TrimSpace(clientID), Scope: resp.Scope}
	if info.TokenType == "" {
		info.TokenType = "Bearer"
	}
	if info.ClientID == "" {
		info.ClientID = xai.EffectiveClientID()
	}
	info.Email = parseGrokJWTEmail(resp.IDToken)
	if info.Email == "" && existing != nil {
		info.Email, _ = existing["email"].(string)
	}
	return info
}

func parseGrokJWTEmail(token string) string {
	return parseGrokJWTClaim(token, "email")
}

func parseGrokJWTClaim(token, claimName string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 || strings.TrimSpace(claimName) == "" {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	value, _ := claims[claimName].(string)
	return strings.TrimSpace(value)
}

func (s *GrokOAuthService) proxyURL(ctx context.Context, proxyID *int64) (string, error) {
	if proxyID == nil {
		return "", nil
	}
	if s == nil || s.proxyRepo == nil {
		return "", errors.New("proxy repository is not available")
	}
	proxy, err := s.proxyRepo.GetByID(ctx, *proxyID)
	if err != nil {
		return "", fmt.Errorf("proxy not found: %w", err)
	}
	if proxy == nil {
		return "", errors.New("proxy not found")
	}
	return proxy.URL(), nil
}
