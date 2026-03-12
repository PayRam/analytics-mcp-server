package tools

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/payram/payram-analytics-mcp-server/internal/protocol"
)

// tokenProvider returns a valid bearer token for API calls.
type tokenProvider interface {
	GetAccessToken(ctx context.Context) (string, error)
}

// pkgTokenProvider is configured at startup when login credentials are provided.
// It is used as a fallback when no per-call token is supplied.
var pkgTokenProvider tokenProvider

// SetTokenProvider registers a server-wide token provider (login-based auth).
// Call this before serving requests when USER_EMAIL + USER_PASSWORD are available.
func SetTokenProvider(tp tokenProvider) {
	pkgTokenProvider = tp
}

// resolveAnalyticsAuth resolves the bearer token and base URL for a tool call.
//
// Token priority: per-call arg → token provider (auto-login/refresh) → PAYRAM_ANALYTICS_TOKEN env.
// Base URL priority: per-call arg → PAYRAM_ANALYTICS_BASE_URL env.
func resolveAnalyticsAuth(ctx context.Context, tokenArg, baseArg string) (string, string, *protocol.ResponseError) {
	token := strings.TrimSpace(tokenArg)

	if token == "" && pkgTokenProvider != nil {
		var err error
		token, err = pkgTokenProvider.GetAccessToken(ctx)
		if err != nil {
			return "", "", &protocol.ResponseError{Code: -32000, Message: fmt.Sprintf("auth: %v", err)}
		}
	}

	if token == "" {
		token = strings.TrimSpace(os.Getenv("PAYRAM_ANALYTICS_TOKEN"))
	}

	if token == "" {
		return "", "", &protocol.ResponseError{Code: -32000, Message: "Missing token: set PAYRAM_ANALYTICS_TOKEN env, set USER_EMAIL+USER_PASSWORD env for auto-login, or pass token in arguments"}
	}

	base := strings.TrimSpace(baseArg)
	if base == "" {
		base = strings.TrimSpace(os.Getenv("PAYRAM_ANALYTICS_BASE_URL"))
	}
	base = strings.TrimSuffix(base, "/")

	if base == "" {
		return "", "", &protocol.ResponseError{Code: -32000, Message: "Missing base_url: set PAYRAM_ANALYTICS_BASE_URL env or pass base_url"}
	}

	return token, base, nil
}
