package tools

import (
	"context"
	"fmt"
	"log"
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

// baseURLEnv is the only place the PayRam URL comes from. Tool calls cannot
// change it, so the server's own token (and any token a caller passes) is only
// ever sent to the merchant's own PayRam server.
const baseURLEnv = "PAYRAM_ANALYTICS_BASE_URL"

// errBaseURLNotAccepted answers a tool call that tried to choose where the
// server sends its PayRam token.
var errBaseURLNotAccepted = &protocol.ResponseError{
	Code:    -32602,
	Message: "base_url is not accepted: this server only calls the PayRam URL set in its own " + baseURLEnv + " setting",
}

// resolveAnalyticsAuth resolves the bearer token and base URL for a tool call.
//
// baseURLArg is the call's base_url argument. Any non-empty value is refused
// before a token is looked up: the base URL comes only from PAYRAM_ANALYTICS_BASE_URL.
// Token priority: per-call arg → token provider (auto-login/refresh) → PAYRAM_ANALYTICS_TOKEN env.
func resolveAnalyticsAuth(ctx context.Context, tokenArg, baseURLArg string) (string, string, *protocol.ResponseError) {
	if strings.TrimSpace(baseURLArg) != "" {
		log.Printf("tools -> resolveAnalyticsAuth: refused a tool call that set base_url")
		return "", "", errBaseURLNotAccepted
	}

	base := strings.TrimSuffix(strings.TrimSpace(os.Getenv(baseURLEnv)), "/")
	if base == "" {
		log.Printf("tools -> resolveAnalyticsAuth: %s is not set", baseURLEnv)
		return "", "", &protocol.ResponseError{Code: -32000, Message: "PayRam URL not configured: set " + baseURLEnv + " in the server's environment"}
	}

	token := strings.TrimSpace(tokenArg)

	if token == "" && pkgTokenProvider != nil {
		var err error
		token, err = pkgTokenProvider.GetAccessToken(ctx)
		if err != nil {
			log.Printf("tools -> resolveAnalyticsAuth: auto-login failed: %v", err)
			return "", "", &protocol.ResponseError{Code: -32000, Message: fmt.Sprintf("auth: %v", err)}
		}
	}

	if token == "" {
		token = strings.TrimSpace(os.Getenv("PAYRAM_ANALYTICS_TOKEN"))
	}

	if token == "" {
		log.Printf("tools -> resolveAnalyticsAuth: no token available")
		return "", "", &protocol.ResponseError{Code: -32000, Message: "Missing token: set PAYRAM_ANALYTICS_TOKEN env, set USER_EMAIL+USER_PASSWORD env for auto-login, or pass token in arguments"}
	}

	return token, base, nil
}
