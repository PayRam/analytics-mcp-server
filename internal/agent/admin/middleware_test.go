package admin

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Callers that the old address rule let in without a token: host loopback,
// the Docker bridge gateway that host traffic arrives from (172.17.0.1), and
// other containers on Docker bridge networks.
var formerlyTrustedAddrs = []string{
	"127.0.0.1:1234",
	"[::1]:1234",
	"172.17.0.1:40000",
	"172.17.0.5:40000",
	"172.18.0.3:40000",
}

func guardedOKHandler(t *testing.T, token string, reached *bool) http.Handler {
	t.Helper()
	return NewAdminMiddleware(token)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		RespondOK(w, http.StatusOK, map[string]string{"value": "ok"})
	}))
}

func TestMiddlewareRefusesEveryAddressWithoutToken(t *testing.T) {
	for _, addr := range append(formerlyTrustedAddrs, "8.8.8.8:3333") {
		reached := false
		handler := guardedOKHandler(t, "secret", &reached)

		req := httptest.NewRequest(http.MethodPost, "/admin/update/apply", nil)
		req.RemoteAddr = addr
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s without token: expected %d, got %d", addr, http.StatusUnauthorized, rr.Code)
		}
		if code := errorCode(t, decodeBody(t, rr)); code != "UNAUTHORIZED" {
			t.Fatalf("%s without token: expected UNAUTHORIZED, got %s", addr, code)
		}
		if reached {
			t.Fatalf("%s without token reached the admin handler", addr)
		}
	}
}

func TestMiddlewareRefusesWrongToken(t *testing.T) {
	for _, addr := range formerlyTrustedAddrs {
		reached := false
		handler := guardedOKHandler(t, "secret", &reached)

		req := httptest.NewRequest(http.MethodGet, "/admin/version", nil)
		req.RemoteAddr = addr
		req.Header.Set(KeyHeader, "secreT")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized || reached {
			t.Fatalf("%s with wrong token: expected %d and no handler call, got %d (reached=%v)", addr, http.StatusUnauthorized, rr.Code, reached)
		}
	}
}

func TestMiddlewareAcceptsTokenFromAnyAddress(t *testing.T) {
	for _, addr := range append(formerlyTrustedAddrs, "8.8.8.8:3333") {
		reached := false
		handler := guardedOKHandler(t, "secret", &reached)

		req := httptest.NewRequest(http.MethodGet, "/admin/version", nil)
		req.RemoteAddr = addr
		req.Header.Set(KeyHeader, "secret")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK || !reached {
			t.Fatalf("%s with token: expected %d, got %d (reached=%v)", addr, http.StatusOK, rr.Code, reached)
		}
	}
}

func TestMiddlewareWithoutConfiguredTokenRefusesEverything(t *testing.T) {
	for _, header := range []string{"", "anything"} {
		reached := false
		handler := guardedOKHandler(t, "  ", &reached)

		req := httptest.NewRequest(http.MethodGet, "/admin/version", nil)
		req.RemoteAddr = "127.0.0.1:1234"
		if header != "" {
			req.Header.Set(KeyHeader, header)
		}
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized || reached {
			t.Fatalf("empty configured token, header %q: expected %d and no handler call, got %d", header, http.StatusUnauthorized, rr.Code)
		}
		if code := errorCode(t, decodeBody(t, rr)); code != "ADMIN_TOKEN_MISSING" {
			t.Fatalf("expected ADMIN_TOKEN_MISSING, got %s", code)
		}
	}
}

// Every /admin route on the real mux needs the token, and the old allowlist
// setting no longer lets anyone in.
func TestNewMuxGuardsEveryAdminRoute(t *testing.T) {
	t.Setenv("PAYRAM_AGENT_HOME", t.TempDir())
	t.Setenv("PAYRAM_AGENT_ADMIN_ALLOWLIST", "127.0.0.1/32,172.16.0.0/12")
	mux := NewMux(&spySupervisor{}, "tok")

	routes := []struct{ method, path string }{
		{http.MethodGet, "/admin/version"},
		{http.MethodGet, "/admin/update/available"},
		{http.MethodPost, "/admin/update/apply"},
		{http.MethodPost, "/admin/update/rollback"},
		{http.MethodGet, "/admin/update/status"},
		{http.MethodPost, "/admin/child/restart"},
		{http.MethodGet, "/admin/child/status"},
		{http.MethodGet, "/admin/logs"},
		{http.MethodPut, "/admin/secrets/openai"},
		{http.MethodDelete, "/admin/secrets/openai"},
		{http.MethodGet, "/admin/secrets/status"},
	}
	for _, route := range routes {
		for _, addr := range []string{"127.0.0.1:1234", "172.17.0.1:40000"} {
			req := httptest.NewRequest(route.method, route.path, nil)
			req.RemoteAddr = addr
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s from %s without token: expected %d, got %d", route.method, route.path, addr, http.StatusUnauthorized, rr.Code)
			}
		}
	}

	sup := &spySupervisor{}
	mux = NewMux(sup, "tok")
	req := httptest.NewRequest(http.MethodPost, "/admin/child/restart", nil)
	req.RemoteAddr = "172.17.0.1:40000"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized || sup.restarts != 0 {
		t.Fatalf("restart without token from the Docker gateway: expected 401 and no restart, got %d (restarts=%d)", rr.Code, sup.restarts)
	}
}

func TestResolveAdminTokenUsesSetting(t *testing.T) {
	t.Setenv(TokenEnv, " configured ")
	token, isGenerated, err := ResolveAdminToken()
	if err != nil {
		t.Fatalf("ResolveAdminToken: %v", err)
	}
	if token != "configured" || isGenerated {
		t.Fatalf("expected the configured token, got %q (generated=%v)", token, isGenerated)
	}
}

func TestResolveAdminTokenGeneratesWhenUnset(t *testing.T) {
	t.Setenv(TokenEnv, "")
	first, isGenerated, err := ResolveAdminToken()
	if err != nil {
		t.Fatalf("ResolveAdminToken: %v", err)
	}
	if !isGenerated {
		t.Fatalf("expected a generated token")
	}
	raw, err := hex.DecodeString(first)
	if err != nil || len(raw) != 32 {
		t.Fatalf("expected 32 random bytes as hex, got %q", first)
	}
	second, _, _ := ResolveAdminToken()
	if first == second {
		t.Fatalf("two generated tokens are equal")
	}
}

func decodeBody(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	return body
}

func errorCode(t *testing.T, body map[string]any) string {
	t.Helper()

	rawError, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object in response: %v", body)
	}

	code, ok := rawError["code"].(string)
	if !ok {
		t.Fatalf("expected error code in response: %v", body)
	}

	return code
}
