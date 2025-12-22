package admin

import (
	"net"
	"net/http"
	"os"
	"strings"
)

func NewAdminMiddleware(token, allowlist string) func(http.Handler) http.Handler {
	guard := &adminMiddleware{token: token, allowed: parseAllowlist(allowlist)}
	return guard.wrap
}

func NewAdminMiddlewareFromEnv() func(http.Handler) http.Handler {
	token := os.Getenv("PAYRAM_AGENT_ADMIN_TOKEN")
	allowlist := os.Getenv("PAYRAM_AGENT_ADMIN_ALLOWLIST")
	return NewAdminMiddleware(token, allowlist)
}

type adminMiddleware struct {
	token   string
	allowed []*net.IPNet
}

const adminKeyHeader = "X-MCP-Key"

func (m *adminMiddleware) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := parseRemoteIP(r.RemoteAddr)
		if !m.isAllowed(ip) {
			RespondError(w, http.StatusForbidden, "FORBIDDEN_IP", "request IP not allowed")
			return
		}

		// Allowlisted IPs bypass the token entirely.
		next.ServeHTTP(w, r)
	})
}

func (m *adminMiddleware) isAllowed(ip net.IP) bool {
	if ip == nil {
		return false
	}

	if ip.IsLoopback() {
		return true
	}

	for _, network := range m.allowed {
		if network.Contains(ip) {
			return true
		}
	}

	return false
}

func parseRemoteIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		return net.ParseIP(host)
	}

	return net.ParseIP(remoteAddr)
}

func parseAllowlist(raw string) []*net.IPNet {
	var networks []*net.IPNet

	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			continue
		}
		networks = append(networks, network)
	}

	return networks
}
