package admin

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/payram/payram-analytics-mcp-server/internal/agent/supervisor"
	"github.com/payram/payram-analytics-mcp-server/internal/agent/update"
)

type fakeSupervisor struct{ restarts int }

func (f *fakeSupervisor) RestartAll() error         { f.restarts++; return nil }
func (f *fakeSupervisor) Status() supervisor.Status { return supervisor.Status{} }
func (f *fakeSupervisor) Logs(string, int) []string { return nil }

func TestUpdateApplySuccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PAYRAM_AGENT_HOME", home)
	t.Setenv("PAYRAM_AGENT_IGNORE_COMPAT", "false")

	chatData := []byte("chat-binary")
	mcpData := []byte("mcp-binary")

	chatHash := sha256.Sum256(chatData)
	mcpHash := sha256.Sum256(mcpData)

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	manifest := update.Manifest{
		Version: "2.0.0",
		Artifacts: update.Artifacts{
			Chat: update.Artifact{SHA256: hex.EncodeToString(chatHash[:])},
			MCP:  update.Artifact{SHA256: hex.EncodeToString(mcpHash[:])},
		},
		Compatibility: update.Compatibility{PayramCore: update.Range{Min: "1.12.0", Max: "1.13.x"}},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/stable/manifest.json", func(w http.ResponseWriter, _ *http.Request) {
		raw, _ := json.Marshal(manifest)
		w.Write(raw)
	})
	mux.HandleFunc("/stable/manifest.json.sig", func(w http.ResponseWriter, _ *http.Request) {
		raw, _ := json.Marshal(manifest)
		w.Write(ed25519.Sign(priv, raw))
	})
	mux.HandleFunc("/chat", func(w http.ResponseWriter, _ *http.Request) { w.Write(chatData) })
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, _ *http.Request) { w.Write(mcpData) })

	srv := httptest.NewServer(mux)
	defer srv.Close()

	manifest.Artifacts.Chat.URL = srv.URL + "/chat"
	manifest.Artifacts.MCP.URL = srv.URL + "/mcp"

	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"version":"1.12.3"}`))
	}))
	defer core.Close()

	chatHealth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer chatHealth.Close()
	mcpHealth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer mcpHealth.Close()

	t.Setenv("PAYRAM_AGENT_UPDATE_BASE_URL", srv.URL)
	t.Setenv("PAYRAM_AGENT_UPDATE_PUBKEY_B64", base64.StdEncoding.EncodeToString(pub))
	t.Setenv("PAYRAM_CORE_URL", core.URL)
	t.Setenv("PAYRAM_CHAT_PORT", portFromURL(chatHealth.URL))
	t.Setenv("PAYRAM_MCP_PORT", portFromURL(mcpHealth.URL))

	sup := &fakeSupervisor{}
	handler := NewMux(sup, "tok")

	req := httptest.NewRequest(http.MethodPost, "/admin/update/apply", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set(KeyHeader, "tok")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d body=%s", rr.Code, rr.Body.String())
	}

	st, err := update.LoadStatus()
	if err != nil {
		t.Fatalf("load status: %v", err)
	}
	if st.CurrentVersion != manifest.Version {
		t.Fatalf("current version mismatch: %s", st.CurrentVersion)
	}
	if st.InProgress {
		t.Fatalf("expected in_progress false")
	}
	if sup.restarts != 1 {
		t.Fatalf("expected 1 restart got %d", sup.restarts)
	}

	target, err := os.Readlink(update.CurrentSymlink())
	if err != nil {
		t.Fatalf("readlink current: %v", err)
	}
	if filepath.Base(target) != manifest.Version {
		t.Fatalf("unexpected current target: %s", target)
	}

	if _, err := os.Stat(filepath.Join(update.ReleaseDir(manifest.Version), "payram-analytics-chat")); err != nil {
		t.Fatalf("chat binary missing: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(update.ReleaseDir(manifest.Version), "chat")); err != nil || filepath.Base(target) != "payram-analytics-chat" {
		t.Fatalf("chat compat link missing/invalid: err=%v target=%s", err, target)
	}
	if target, err := os.Readlink(filepath.Join(update.ReleaseDir(manifest.Version), "mcp")); err != nil || filepath.Base(target) != "payram-analytics-mcp" {
		t.Fatalf("mcp compat link missing/invalid: err=%v target=%s", err, target)
	}
}

func TestUpdateApplyIgnoreCompatNoCoreURL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PAYRAM_AGENT_HOME", home)
	t.Setenv("PAYRAM_AGENT_IGNORE_COMPAT", "1")

	chatData := []byte("chat-binary")
	mcpData := []byte("mcp-binary")

	chatHash := sha256.Sum256(chatData)
	mcpHash := sha256.Sum256(mcpData)

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	manifest := update.Manifest{
		Version: "2.0.0",
		Artifacts: update.Artifacts{
			Chat: update.Artifact{SHA256: hex.EncodeToString(chatHash[:])},
			MCP:  update.Artifact{SHA256: hex.EncodeToString(mcpHash[:])},
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/stable/manifest.json", func(w http.ResponseWriter, _ *http.Request) {
		raw, _ := json.Marshal(manifest)
		w.Write(raw)
	})
	mux.HandleFunc("/stable/manifest.json.sig", func(w http.ResponseWriter, _ *http.Request) {
		raw, _ := json.Marshal(manifest)
		w.Write(ed25519.Sign(priv, raw))
	})
	mux.HandleFunc("/chat", func(w http.ResponseWriter, _ *http.Request) { w.Write(chatData) })
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, _ *http.Request) { w.Write(mcpData) })

	srv := httptest.NewServer(mux)
	defer srv.Close()

	manifest.Artifacts.Chat.URL = srv.URL + "/chat"
	manifest.Artifacts.MCP.URL = srv.URL + "/mcp"

	chatHealth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer chatHealth.Close()
	mcpHealth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer mcpHealth.Close()

	t.Setenv("PAYRAM_AGENT_UPDATE_BASE_URL", srv.URL)
	t.Setenv("PAYRAM_AGENT_UPDATE_PUBKEY_B64", base64.StdEncoding.EncodeToString(pub))
	t.Setenv("PAYRAM_CHAT_PORT", portFromURL(chatHealth.URL))
	t.Setenv("PAYRAM_MCP_PORT", portFromURL(mcpHealth.URL))

	sup := &fakeSupervisor{}
	handler := NewMux(sup, "tok")

	req := httptest.NewRequest(http.MethodPost, "/admin/update/apply", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set(KeyHeader, "tok")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d body=%s", rr.Code, rr.Body.String())
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if warnings, ok := body["data"].(map[string]any)["warnings"].([]any); !ok || len(warnings) == 0 {
		t.Fatalf("expected warnings in response")
	}
}

func TestUpdateApplyHealthRollback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PAYRAM_AGENT_HOME", home)
	t.Setenv("PAYRAM_AGENT_HEALTH_TIMEOUT_MS", "200")
	t.Setenv("PAYRAM_AGENT_IGNORE_COMPAT", "false")

	oldDir := filepath.Join(home, "releases", "0.0.1")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatalf("mkdir old: %v", err)
	}
	if _, err := update.UpdateSymlinks(oldDir); err != nil {
		t.Fatalf("seed symlinks: %v", err)
	}

	initialStatus := update.UpdateStatus{}
	initialStatus.MarkSuccess("0.0.1", "0.0.0")
	if err := update.SaveStatus(initialStatus); err != nil {
		t.Fatalf("seed status: %v", err)
	}

	chatData := []byte("chat-new")
	mcpData := []byte("mcp-new")
	chatHash := sha256.Sum256(chatData)
	mcpHash := sha256.Sum256(mcpData)

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	manifest := update.Manifest{
		Version: "2.0.0",
		Artifacts: update.Artifacts{
			Chat: update.Artifact{SHA256: hex.EncodeToString(chatHash[:])},
			MCP:  update.Artifact{SHA256: hex.EncodeToString(mcpHash[:])},
		},
		Compatibility: update.Compatibility{PayramCore: update.Range{Min: "1.12.0", Max: "1.13.x"}},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/stable/manifest.json", func(w http.ResponseWriter, _ *http.Request) {
		raw, _ := json.Marshal(manifest)
		w.Write(raw)
	})
	mux.HandleFunc("/stable/manifest.json.sig", func(w http.ResponseWriter, _ *http.Request) {
		raw, _ := json.Marshal(manifest)
		w.Write(ed25519.Sign(priv, raw))
	})
	mux.HandleFunc("/chat", func(w http.ResponseWriter, _ *http.Request) { w.Write(chatData) })
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, _ *http.Request) { w.Write(mcpData) })

	srv := httptest.NewServer(mux)
	defer srv.Close()

	manifest.Artifacts.Chat.URL = srv.URL + "/chat"
	manifest.Artifacts.MCP.URL = srv.URL + "/mcp"

	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"version":"1.12.3"}`))
	}))
	defer core.Close()

	chatHealth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	defer chatHealth.Close()
	mcpHealth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	defer mcpHealth.Close()

	t.Setenv("PAYRAM_AGENT_UPDATE_BASE_URL", srv.URL)
	t.Setenv("PAYRAM_AGENT_UPDATE_PUBKEY_B64", base64.StdEncoding.EncodeToString(pub))
	t.Setenv("PAYRAM_CORE_URL", core.URL)
	t.Setenv("PAYRAM_CHAT_PORT", portFromURL(chatHealth.URL))
	t.Setenv("PAYRAM_MCP_PORT", portFromURL(mcpHealth.URL))

	sup := &fakeSupervisor{}
	handler := NewMux(sup, "tok")

	req := httptest.NewRequest(http.MethodPost, "/admin/update/apply", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set(KeyHeader, "tok")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 got %d body=%s", rr.Code, rr.Body.String())
	}

	st, err := update.LoadStatus()
	if err != nil {
		t.Fatalf("load status: %v", err)
	}
	if st.LastErrorCode != "UPDATE_FAILED_ROLLED_BACK" {
		t.Fatalf("unexpected error code: %s", st.LastErrorCode)
	}
	if st.LastSuccessVersion != "0.0.1" {
		t.Fatalf("last_success_version changed: %s", st.LastSuccessVersion)
	}
	if st.CurrentVersion != "0.0.1" {
		t.Fatalf("current version should reflect rollback: %s", st.CurrentVersion)
	}
	if st.LastAttemptVersion != manifest.Version {
		t.Fatalf("last_attempt_version mismatch: %s", st.LastAttemptVersion)
	}

	curTarget, _ := os.Readlink(update.CurrentSymlink())
	if filepath.Base(curTarget) != "0.0.1" {
		t.Fatalf("current not rolled back: %s", curTarget)
	}
	prevTarget, _ := os.Readlink(update.PreviousSymlink())
	if filepath.Base(prevTarget) != manifest.Version {
		t.Fatalf("previous not pointing to new release: %s", prevTarget)
	}
	if sup.restarts < 2 {
		t.Fatalf("expected at least 2 restarts got %d", sup.restarts)
	}
}

func TestRollbackEndpoint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PAYRAM_AGENT_HOME", home)
	t.Setenv("PAYRAM_AGENT_HEALTH_TIMEOUT_MS", "200")
	t.Setenv("PAYRAM_AGENT_IGNORE_COMPAT", "false")

	oldDir := filepath.Join(home, "releases", "1.0.0")
	newDir := filepath.Join(home, "releases", "2.0.0")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatalf("mkdir old: %v", err)
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatalf("mkdir new: %v", err)
	}

	if _, err := update.UpdateSymlinks(oldDir); err != nil {
		t.Fatalf("seed old: %v", err)
	}
	if _, err := update.UpdateSymlinks(newDir); err != nil {
		t.Fatalf("seed new: %v", err)
	}

	chatHealth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer chatHealth.Close()
	mcpHealth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer mcpHealth.Close()

	t.Setenv("PAYRAM_CHAT_PORT", portFromURL(chatHealth.URL))
	t.Setenv("PAYRAM_MCP_PORT", portFromURL(mcpHealth.URL))

	sup := &fakeSupervisor{}
	handler := NewMux(sup, "tok")

	req := httptest.NewRequest(http.MethodPost, "/admin/update/rollback", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set(KeyHeader, "tok")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d body=%s", rr.Code, rr.Body.String())
	}

	curTarget, _ := os.Readlink(update.CurrentSymlink())
	if filepath.Base(curTarget) != "1.0.0" {
		t.Fatalf("current should point to old: %s", curTarget)
	}
	prevTarget, _ := os.Readlink(update.PreviousSymlink())
	if filepath.Base(prevTarget) != "2.0.0" {
		t.Fatalf("previous should point to new: %s", prevTarget)
	}
	if sup.restarts == 0 {
		t.Fatalf("expected restart on rollback")
	}
}

func portFromURL(raw string) string {
	u, _ := url.Parse(raw)
	return u.Port()
}
