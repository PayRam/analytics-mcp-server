package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/payram/payram-analytics-mcp-server/internal/agent/secrets"
	"github.com/payram/payram-analytics-mcp-server/internal/agent/supervisor"
)

type spySupervisor struct {
	restarts int
}

func (s *spySupervisor) RestartAll() error         { s.restarts++; return nil }
func (s *spySupervisor) Status() supervisor.Status { return supervisor.Status{} }
func (s *spySupervisor) Logs(string, int) []string { return nil }

func TestSecretsHandlers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PAYRAM_AGENT_HOME", home)

	sup := &spySupervisor{}
	mux := NewMux(sup, "tok")

	// PUT key
	body := map[string]string{"openai_api_key": "sk-test"}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/admin/secrets/openai", bytes.NewReader(buf))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set(KeyHeader, "tok")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("put status: %d body=%s", rr.Code, rr.Body.String())
	}
	if sup.restarts != 1 {
		t.Fatalf("expected restart after setting key, got %d", sup.restarts)
	}

	// Status should show set
	req = httptest.NewRequest(http.MethodGet, "/admin/secrets/status", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set(KeyHeader, "tok")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status code: %d body=%s", rr.Code, rr.Body.String())
	}
	var statusResp struct {
		Data struct {
			OpenAISet bool   `json:"openai_api_key_set"`
			Source    string `json:"source"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &statusResp); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !statusResp.Data.OpenAISet || statusResp.Data.Source == "missing" {
		t.Fatalf("expected key set in status")
	}

	// Delete
	req = httptest.NewRequest(http.MethodDelete, "/admin/secrets/openai", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set(KeyHeader, "tok")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete code: %d body=%s", rr.Code, rr.Body.String())
	}

	// Status should show missing
	req = httptest.NewRequest(http.MethodGet, "/admin/secrets/status", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set(KeyHeader, "tok")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status code 2: %d body=%s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &statusResp); err != nil {
		t.Fatalf("decode status 2: %v", err)
	}
	if statusResp.Data.OpenAISet {
		t.Fatalf("expected key cleared")
	}

	// Verify file removed
	if _, source, _ := secrets.Load(home); source != "missing" {
		t.Fatalf("expected missing after delete, got %s", source)
	}
}
