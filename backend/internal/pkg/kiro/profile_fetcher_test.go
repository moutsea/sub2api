package kiro

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRefreshIdCAccessTokenAt_Success(t *testing.T) {
	var gotBody idcRefreshRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accessToken":"acc-token-xyz","expiresIn":3600,"tokenType":"Bearer"}`))
	}))
	defer srv.Close()

	tokenInfo, err := refreshIdCAccessTokenAt(context.Background(), srv.URL, "", "cid", "csecret", "rtoken", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokenInfo.AccessToken != "acc-token-xyz" {
		t.Fatalf("access token = %q, want acc-token-xyz", tokenInfo.AccessToken)
	}
	// Verify the request body carried the IdC credentials in camelCase.
	if gotBody.ClientID != "cid" || gotBody.ClientSecret != "csecret" ||
		gotBody.RefreshToken != "rtoken" || gotBody.GrantType != "refresh_token" {
		t.Fatalf("unexpected request body: %+v", gotBody)
	}
}

func TestRefreshIdCAccessTokenAt_ReturnsRotatedRefreshToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accessToken":"acc-token-xyz","refreshToken":"new-refresh-token","expiresIn":3600,"tokenType":"Bearer"}`))
	}))
	defer srv.Close()

	tokenInfo, err := refreshIdCAccessTokenAt(context.Background(), srv.URL, "", "cid", "csecret", "old-refresh-token", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokenInfo.AccessToken != "acc-token-xyz" {
		t.Fatalf("access token = %q, want acc-token-xyz", tokenInfo.AccessToken)
	}
	if tokenInfo.RefreshToken != "new-refresh-token" {
		t.Fatalf("refresh token = %q, want new-refresh-token", tokenInfo.RefreshToken)
	}
}

func TestRefreshIdCAccessTokenAt_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Invalid refresh token provided"}`))
	}))
	defer srv.Close()

	_, err := refreshIdCAccessTokenAt(context.Background(), srv.URL, "", "cid", "csecret", "rtoken", "")
	if err == nil {
		t.Fatal("expected error on non-200 response")
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("error %q should surface upstream body", err)
	}
}

func TestRefreshIdCAccessTokenAt_MissingAccessToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"expiresIn":3600,"tokenType":"Bearer"}`))
	}))
	defer srv.Close()

	_, err := refreshIdCAccessTokenAt(context.Background(), srv.URL, "", "cid", "csecret", "rtoken", "")
	if err == nil {
		t.Fatal("expected error when accessToken missing")
	}
}

func TestRefreshIdCAccessToken_ValidatesArgs(t *testing.T) {
	if _, err := RefreshIdCAccessToken(context.Background(), "", "s", "r", "us-east-1", ""); err == nil {
		t.Fatal("expected error for empty client_id")
	}
	if _, err := RefreshIdCAccessToken(context.Background(), "c", "s", "", "us-east-1", ""); err == nil {
		t.Fatal("expected error for empty refresh_token")
	}
}

func TestFetchProfileArnAt_Success(t *testing.T) {
	var gotTarget, gotAuth, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTarget = r.Header.Get("X-Amz-Target")
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		_, _ = w.Write([]byte(`{"profiles":[{"arn":"arn:aws:codewhisperer:us-east-1:844641714200:profile/GRC7UMADYXVK","profileName":"KiroProfile-us-east-1"}]}`))
	}))
	defer srv.Close()

	arn, err := fetchProfileArnAt(context.Background(), srv.URL, "", "acc-token", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "arn:aws:codewhisperer:us-east-1:844641714200:profile/GRC7UMADYXVK"
	if arn != want {
		t.Fatalf("arn = %q, want %q", arn, want)
	}
	if gotTarget != "AmazonCodeWhispererService.ListAvailableProfiles" {
		t.Fatalf("X-Amz-Target = %q", gotTarget)
	}
	if gotAuth != "Bearer acc-token" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotCT != "application/x-amz-json-1.0" {
		t.Fatalf("Content-Type = %q", gotCT)
	}
}

func TestFetchProfileArnAt_EmptyProfiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"profiles":[]}`))
	}))
	defer srv.Close()

	_, err := fetchProfileArnAt(context.Background(), srv.URL, "", "acc-token", "")
	if err == nil {
		t.Fatal("expected error when profiles is empty")
	}
	if !strings.Contains(err.Error(), "no profile arn") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFetchProfileArnAt_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"__type":"AccessDeniedException"}`))
	}))
	defer srv.Close()

	_, err := fetchProfileArnAt(context.Background(), srv.URL, "", "acc-token", "")
	if err == nil {
		t.Fatal("expected error on 403")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("error %q should include status code", err)
	}
}

func TestFetchProfileArnAt_SkipsEmptyArn(t *testing.T) {
	// First profile has empty arn; the fetcher should return the first non-empty one.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"profiles":[{"arn":"","profileName":"empty"},{"arn":"arn:aws:codewhisperer:us-east-1:1:profile/X","profileName":"real"}]}`))
	}))
	defer srv.Close()

	arn, err := fetchProfileArnAt(context.Background(), srv.URL, "", "acc-token", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if arn != "arn:aws:codewhisperer:us-east-1:1:profile/X" {
		t.Fatalf("arn = %q, want the first non-empty arn", arn)
	}
}

func TestFetchAvailableProfileArn_ValidatesArgs(t *testing.T) {
	if _, err := FetchAvailableProfileArn(context.Background(), "", "us-east-1", "", ""); err == nil {
		t.Fatal("expected error for empty access token")
	}
}
