package admin

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/payram/payram-analytics-mcp-server/internal/agent/supervisor"
	"github.com/payram/payram-analytics-mcp-server/internal/agent/update"
)

func TestUpdateAvailableSuccess(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}

	home := t.TempDir()

	manifest := update.Manifest{
		Name:          "payram-analytics",
		Channel:       "stable",
		Version:       "1.7.4",
		Notes:         "bug fixes",
		ReleasedAt:    time.Date(2025, 12, 18, 0, 0, 0, 0, time.UTC),
		Compatibility: update.Compatibility{PayramCore: update.Range{Min: "1.12.0", Max: "1.13.x"}},
	}

	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	sig := ed25519.Sign(priv, raw)

	mux := http.NewServeMux()
	mux.HandleFunc("/stable/manifest.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
	})
	mux.HandleFunc("/stable/manifest.json.sig", func(w http.ResponseWriter, _ *http.Request) {
		w.Write(sig)
	})

	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"version":"1.12.3"}`))
	}))
	defer core.Close()

	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Setenv("PAYRAM_AGENT_HOME", home)
	t.Setenv("PAYRAM_AGENT_UPDATE_BASE_URL", srv.URL)
	t.Setenv("PAYRAM_AGENT_UPDATE_PUBKEY_B64", base64.StdEncoding.EncodeToString(pub))
	t.Setenv("PAYRAM_CORE_URL", core.URL)

	sup := &supervisor.Supervisor{}
	handler := NewMux(sup, "tok")

	req := httptest.NewRequest(http.MethodGet, "/admin/update/available?channel=stable", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set(KeyHeader, "tok")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data: %v", body)
	}

	if data["target_version"] != manifest.Version {
		t.Fatalf("unexpected target_version: %v", data["target_version"])
	}
	if data["notes"] != manifest.Notes {
		t.Fatalf("unexpected notes: %v", data["notes"])
	}
	coreData, ok := data["payram_core"].(map[string]any)
	if !ok {
		t.Fatalf("missing payram_core block: %v", data)
	}
	if compat, _ := coreData["compatible"].(bool); !compat {
		t.Fatalf("expected compatible core")
	}
}

func TestUpdateAvailableAlreadyCurrent(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}

	home := t.TempDir()

	manifest := update.Manifest{Version: "2.0.0"}
	raw, _ := json.Marshal(manifest)
	sig := ed25519.Sign(priv, raw)

	mux := http.NewServeMux()
	mux.HandleFunc("/stable/manifest.json", func(w http.ResponseWriter, _ *http.Request) { w.Write(raw) })
	mux.HandleFunc("/stable/manifest.json.sig", func(w http.ResponseWriter, _ *http.Request) { w.Write(sig) })

	srv := httptest.NewServer(mux)
	defer srv.Close()

	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"version":"1.12.3"}`))
	}))
	defer core.Close()

	t.Setenv("PAYRAM_AGENT_HOME", home)
	t.Setenv("PAYRAM_AGENT_UPDATE_BASE_URL", srv.URL)
	t.Setenv("PAYRAM_AGENT_UPDATE_PUBKEY_B64", base64.StdEncoding.EncodeToString(pub))
	t.Setenv("PAYRAM_CORE_URL", core.URL)

	if err := update.SaveStatus(update.UpdateStatus{CurrentVersion: manifest.Version}); err != nil {
		t.Fatalf("seed status: %v", err)
	}

	sup := &supervisor.Supervisor{}
	handler := NewMux(sup, "tok")

	req := httptest.NewRequest(http.MethodGet, "/admin/update/available", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set(KeyHeader, "tok")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	data := body["data"].(map[string]any)
	if avail, _ := data["available"].(bool); avail {
		t.Fatalf("expected available false when already current")
	}
	if cv := data["current_version"]; cv != manifest.Version {
		t.Fatalf("unexpected current_version: %v", cv)
	}
}

func TestUpdateAvailableSignatureInvalid(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}

	home := t.TempDir()

	manifest := update.Manifest{Version: "1.0.0"}
	raw, _ := json.Marshal(manifest)
	sig := ed25519.Sign(priv, raw)

	mux := http.NewServeMux()
	mux.HandleFunc("/stable/manifest.json", func(w http.ResponseWriter, _ *http.Request) { w.Write(raw) })
	mux.HandleFunc("/stable/manifest.json.sig", func(w http.ResponseWriter, _ *http.Request) { w.Write(sig) })

	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"version":"1.12.3"}`))
	}))
	defer core.Close()

	srv := httptest.NewServer(mux)
	defer srv.Close()

	wrongPub, _, _ := ed25519.GenerateKey(rand.Reader)

	t.Setenv("PAYRAM_AGENT_HOME", home)
	t.Setenv("PAYRAM_AGENT_UPDATE_BASE_URL", srv.URL)
	t.Setenv("PAYRAM_AGENT_UPDATE_PUBKEY_B64", base64.StdEncoding.EncodeToString(wrongPub))
	t.Setenv("PAYRAM_CORE_URL", core.URL)

	sup := &supervisor.Supervisor{}
	handler := NewMux(sup, "tok")

	req := httptest.NewRequest(http.MethodGet, "/admin/update/available", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set(KeyHeader, "tok")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object: %v", body)
	}
	if errObj["code"] != "SIGNATURE_INVALID" {
		t.Fatalf("unexpected error code: %v", errObj["code"])
	}
}

func TestUpdateAvailableCoreIncompatible(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}

	home := t.TempDir()

	manifest := update.Manifest{
		Version:       "1.0.0",
		Compatibility: update.Compatibility{PayramCore: update.Range{Min: "1.12.0", Max: "1.13.x"}},
	}

	raw, _ := json.Marshal(manifest)
	sig := ed25519.Sign(priv, raw)

	mux := http.NewServeMux()
	mux.HandleFunc("/stable/manifest.json", func(w http.ResponseWriter, _ *http.Request) { w.Write(raw) })
	mux.HandleFunc("/stable/manifest.json.sig", func(w http.ResponseWriter, _ *http.Request) { w.Write(sig) })

	srv := httptest.NewServer(mux)
	defer srv.Close()

	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"version":"1.10.0"}`))
	}))
	defer core.Close()

	t.Setenv("PAYRAM_AGENT_HOME", home)
	t.Setenv("PAYRAM_AGENT_UPDATE_BASE_URL", srv.URL)
	t.Setenv("PAYRAM_AGENT_UPDATE_PUBKEY_B64", base64.StdEncoding.EncodeToString(pub))
	t.Setenv("PAYRAM_CORE_URL", core.URL)

	sup := &supervisor.Supervisor{}
	handler := NewMux(sup, "tok")

	req := httptest.NewRequest(http.MethodGet, "/admin/update/available", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set(KeyHeader, "tok")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	data := body["data"].(map[string]any)
	coreData := data["payram_core"].(map[string]any)
	if compat, _ := coreData["compatible"].(bool); compat {
		t.Fatalf("expected incompatible core")
	}
}

func TestUpdateAvailableCoreUnreachable(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}

	home := t.TempDir()

	manifest := update.Manifest{Version: "1.0.0"}
	raw, _ := json.Marshal(manifest)
	sig := ed25519.Sign(priv, raw)

	mux := http.NewServeMux()
	mux.HandleFunc("/stable/manifest.json", func(w http.ResponseWriter, _ *http.Request) { w.Write(raw) })
	mux.HandleFunc("/stable/manifest.json.sig", func(w http.ResponseWriter, _ *http.Request) { w.Write(sig) })

	srv := httptest.NewServer(mux)
	defer srv.Close()

	deadCore := httptest.NewServer(http.NewServeMux())
	deadCore.Close()

	t.Setenv("PAYRAM_AGENT_HOME", home)
	t.Setenv("PAYRAM_AGENT_UPDATE_BASE_URL", srv.URL)
	t.Setenv("PAYRAM_AGENT_UPDATE_PUBKEY_B64", base64.StdEncoding.EncodeToString(pub))
	t.Setenv("PAYRAM_CORE_URL", deadCore.URL) // closed server

	sup := &supervisor.Supervisor{}
	handler := NewMux(sup, "tok")

	req := httptest.NewRequest(http.MethodGet, "/admin/update/available", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set(KeyHeader, "tok")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	errObj := body["error"].(map[string]any)
	if errObj["code"] != "CORE_UNREACHABLE" {
		t.Fatalf("unexpected error code: %v", errObj["code"])
	}
}

func TestUpdateAvailableIgnoreCompatNoCoreURL(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}

	home := t.TempDir()

	manifest := update.Manifest{Version: "1.0.0"}
	raw, _ := json.Marshal(manifest)
	sig := ed25519.Sign(priv, raw)

	mux := http.NewServeMux()
	mux.HandleFunc("/stable/manifest.json", func(w http.ResponseWriter, _ *http.Request) { w.Write(raw) })
	mux.HandleFunc("/stable/manifest.json.sig", func(w http.ResponseWriter, _ *http.Request) { w.Write(sig) })

	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Setenv("PAYRAM_AGENT_HOME", home)
	t.Setenv("PAYRAM_AGENT_UPDATE_BASE_URL", srv.URL)
	t.Setenv("PAYRAM_AGENT_UPDATE_PUBKEY_B64", base64.StdEncoding.EncodeToString(pub))
	t.Setenv("PAYRAM_AGENT_IGNORE_COMPAT", "true")

	sup := &supervisor.Supervisor{}
	handler := NewMux(sup, "tok")

	req := httptest.NewRequest(http.MethodGet, "/admin/update/available", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set(KeyHeader, "tok")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	payramCore := body["data"].(map[string]any)["payram_core"].(map[string]any)
	if payramCore["error_code"] != "CORE_URL_MISSING" {
		t.Fatalf("expected CORE_URL_MISSING, got %v", payramCore["error_code"])
	}
	compat := body["data"].(map[string]any)["compat"].(map[string]any)
	if ig, _ := compat["ignored"].(bool); !ig {
		t.Fatalf("expected ignored true")
	}
	if comp, _ := compat["compatible"].(bool); !comp {
		t.Fatalf("expected compatible true when ignored")
	}
}
