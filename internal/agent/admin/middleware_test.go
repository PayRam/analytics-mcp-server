package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMiddlewareMissingToken(t *testing.T) {
	t.Setenv("PAYRAM_AGENT_ADMIN_TOKEN", "")
	t.Setenv("PAYRAM_AGENT_ADMIN_ALLOWLIST", "")

	handler := NewAdminMiddlewareFromEnv()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		RespondOK(w, http.StatusOK, map[string]string{"value": "ok"})
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin/version", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	body := decodeBody(t, rr)
	if ok, _ := body["ok"].(bool); !ok {
		t.Fatalf("expected ok response, got %v", body)
	}
}

func TestMiddlewareWrongToken(t *testing.T) {
	t.Setenv("PAYRAM_AGENT_ADMIN_TOKEN", "secret")
	t.Setenv("PAYRAM_AGENT_ADMIN_ALLOWLIST", "")

	handler := NewAdminMiddlewareFromEnv()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		RespondOK(w, http.StatusOK, map[string]string{"value": "ok"})
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin/version", nil)
	req.RemoteAddr = "8.8.8.8:3333" // not allowlisted
	req.Header.Set(adminKeyHeader, "nope")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}

	body := decodeBody(t, rr)
	if code := errorCode(t, body); code != "UNAUTHORIZED" {
		t.Fatalf("expected error code UNAUTHORIZED, got %s", code)
	}
}

func TestMiddlewareAllowsLocalhost(t *testing.T) {
	t.Setenv("PAYRAM_AGENT_ADMIN_TOKEN", "secret")
	t.Setenv("PAYRAM_AGENT_ADMIN_ALLOWLIST", "")

	handler := NewAdminMiddlewareFromEnv()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		RespondOK(w, http.StatusOK, map[string]string{"value": "ok"})
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin/version", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set(adminKeyHeader, "secret")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	body := decodeBody(t, rr)
	if ok, _ := body["ok"].(bool); !ok {
		t.Fatalf("expected ok response, got %v", body)
	}
}

func TestMiddlewareAllowsCIDR(t *testing.T) {
	t.Setenv("PAYRAM_AGENT_ADMIN_TOKEN", "secret")
	t.Setenv("PAYRAM_AGENT_ADMIN_ALLOWLIST", "10.0.0.0/8")

	handler := NewAdminMiddlewareFromEnv()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		RespondOK(w, http.StatusOK, map[string]string{"value": "ok"})
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin/version", nil)
	req.RemoteAddr = "10.1.2.3:2358"
	req.Header.Set(adminKeyHeader, "secret")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	body := decodeBody(t, rr)
	if ok, _ := body["ok"].(bool); !ok {
		t.Fatalf("expected ok response, got %v", body)
	}
}

func TestMiddlewareNonAllowlistWithValidToken(t *testing.T) {
	t.Setenv("PAYRAM_AGENT_ADMIN_TOKEN", "secret")
	t.Setenv("PAYRAM_AGENT_ADMIN_ALLOWLIST", "")

	handler := NewAdminMiddlewareFromEnv()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		RespondOK(w, http.StatusOK, map[string]string{"value": "ok"})
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin/version", nil)
	req.RemoteAddr = "8.8.8.8:3333" // not allowlisted
	req.Header.Set(adminKeyHeader, "secret")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	body := decodeBody(t, rr)
	if ok, _ := body["ok"].(bool); !ok {
		t.Fatalf("expected ok response, got %v", body)
	}
}

func TestMiddlewareBlocksIP(t *testing.T) {
	t.Setenv("PAYRAM_AGENT_ADMIN_TOKEN", "")
	t.Setenv("PAYRAM_AGENT_ADMIN_ALLOWLIST", "")

	handler := NewAdminMiddlewareFromEnv()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		RespondOK(w, http.StatusOK, map[string]string{"value": "ok"})
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin/version", nil)
	req.RemoteAddr = "8.8.8.8:3333"
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, rr.Code)
	}

	body := decodeBody(t, rr)
	if code := errorCode(t, body); code != "ADMIN_TOKEN_MISSING" {
		t.Fatalf("expected error code ADMIN_TOKEN_MISSING, got %s", code)
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
