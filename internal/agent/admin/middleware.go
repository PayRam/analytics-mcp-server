package admin

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
)

// KeyHeader carries the admin token on every /admin request.
const KeyHeader = "X-MCP-Key"

// TokenEnv names the setting that holds the admin token.
const TokenEnv = "PAYRAM_AGENT_ADMIN_TOKEN"

// ResolveAdminToken returns the admin token from PAYRAM_AGENT_ADMIN_TOKEN.
// When it is not set, it returns a random token that exists only in this
// process (isGenerated is true): /admin then answers the agent's own update
// loop and nobody else.
func ResolveAdminToken() (token string, isGenerated bool, err error) {
	if v := strings.TrimSpace(os.Getenv(TokenEnv)); v != "" {
		return v, false, nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		log.Printf("admin -> ResolveAdminToken: generate token: %v", err)
		return "", false, fmt.Errorf("generate admin token: %w", err)
	}
	return hex.EncodeToString(buf), true, nil
}

// NewAdminMiddleware guards /admin: every request must carry the admin token
// in X-MCP-Key. The caller's address is never trusted. Docker hands host
// loopback traffic to the container from 172.17.0.1, and every container on
// a bridge network sits in 172.16.0.0/12, so an address rule lets them all in.
// An empty token refuses every request.
func NewAdminMiddleware(token string) func(http.Handler) http.Handler {
	guard := &adminMiddleware{token: strings.TrimSpace(token)}
	return guard.wrap
}

type adminMiddleware struct {
	token string
}

func (m *adminMiddleware) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.token == "" {
			m.refuse(w, r, "ADMIN_TOKEN_MISSING", "admin token not configured")
			return
		}

		key := strings.TrimSpace(r.Header.Get(KeyHeader))
		if key == "" {
			m.refuse(w, r, "UNAUTHORIZED", "missing admin key")
			return
		}

		if subtle.ConstantTimeCompare([]byte(key), []byte(m.token)) != 1 {
			m.refuse(w, r, "UNAUTHORIZED", "invalid admin key")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (m *adminMiddleware) refuse(w http.ResponseWriter, r *http.Request, code, message string) {
	log.Printf("adminMiddleware -> wrap: refused %s %s from %s: %s", r.Method, r.URL.Path, r.RemoteAddr, message)
	RespondError(w, http.StatusUnauthorized, code, message)
}
