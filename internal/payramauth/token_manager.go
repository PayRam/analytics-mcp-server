package payramauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// TokenManager fetches and refreshes dashboard access tokens for MCP calls.
type TokenManager struct {
	baseURL  string
	email    string
	password string

	mu           sync.Mutex
	accessToken  string
	refreshToken string
	expiresAt    time.Time
	lastUsed     time.Time

	httpClient *http.Client
}

func NewTokenManager(baseURL, email, password string) *TokenManager {
	return &TokenManager{
		baseURL:    strings.TrimRight(baseURL, "/"),
		email:      strings.TrimSpace(email),
		password:   password,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// GetAccessToken returns a valid access token, refreshing or logging in as needed.
func (tm *TokenManager) GetAccessToken(ctx context.Context) (string, error) {
	now := time.Now()

	tm.mu.Lock()
	// Expire tokens after 5 minutes of inactivity
	if !tm.lastUsed.IsZero() && now.Sub(tm.lastUsed) > 5*time.Minute {
		tm.accessToken = ""
		tm.refreshToken = ""
		tm.expiresAt = time.Time{}
	}
	access := tm.accessToken
	exp := tm.expiresAt
	refresh := tm.refreshToken
	tm.mu.Unlock()

	// If still valid (with small skew), return
	if access != "" && exp.After(now.Add(10*time.Second)) {
		tm.touch()
		return access, nil
	}

	// Try refresh first if available
	if refresh != "" {
		if tok, newRefresh, expiresAt, err := tm.refresh(ctx, refresh); err == nil {
			tm.setTokens(tok, newRefresh, expiresAt)
			return tok, nil
		}
	}

	// Fall back to login
	tok, newRefresh, expiresAt, err := tm.login(ctx)
	if err != nil {
		return "", err
	}
	tm.setTokens(tok, newRefresh, expiresAt)
	return tok, nil
}

func (tm *TokenManager) touch() {
	tm.mu.Lock()
	tm.lastUsed = time.Now()
	tm.mu.Unlock()
}

func (tm *TokenManager) setTokens(access, refresh string, exp time.Time) {
	tm.mu.Lock()
	tm.accessToken = access
	tm.refreshToken = refresh
	tm.expiresAt = exp
	tm.lastUsed = time.Now()
	tm.mu.Unlock()
}

func (tm *TokenManager) login(ctx context.Context) (string, string, time.Time, error) {
	if tm.email == "" || tm.password == "" || tm.baseURL == "" {
		return "", "", time.Time{}, fmt.Errorf("missing auth config")
	}

	payload := map[string]string{
		"email":    tm.email,
		"password": tm.password,
	}
	var resp tokenResponse
	if err := tm.post(ctx, tm.baseURL+"/api/v1/signin", payload, &resp); err != nil {
		return "", "", time.Time{}, fmt.Errorf("signin: %w", err)
	}
	return resp.AccessToken, resp.RefreshToken, expiryFromNow(resp.ExpiresIn), nil
}

func (tm *TokenManager) refresh(ctx context.Context, refreshToken string) (string, string, time.Time, error) {
	payload := map[string]string{
		"refreshToken": refreshToken,
	}
	var resp tokenResponse
	if err := tm.post(ctx, tm.baseURL+"/api/v1/refresh", payload, &resp); err != nil {
		return "", "", time.Time{}, fmt.Errorf("refresh: %w", err)
	}
	return resp.AccessToken, resp.RefreshToken, expiryFromNow(resp.ExpiresIn), nil
}

func (tm *TokenManager) post(ctx context.Context, url string, payload any, dest any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := tm.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return err
	}
	return nil
}

func expiryFromNow(seconds int64) time.Time {
	if seconds <= 0 {
		seconds = 120 // fallback 2 minutes
	}
	return time.Now().Add(time.Duration(seconds) * time.Second)
}

type tokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int64  `json:"expiresIn"`
}
