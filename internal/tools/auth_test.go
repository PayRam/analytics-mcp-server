package tools_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/payram/payram-analytics-mcp-server/internal/app"
	"github.com/payram/payram-analytics-mcp-server/internal/mcp"
	"github.com/payram/payram-analytics-mcp-server/internal/tools"
)

// recordingServer stands in for one HTTP host and records every request it gets.
type recordingServer struct {
	*httptest.Server
	mu             sync.Mutex
	authorizations []string
}

func newRecordingServer(t *testing.T, handler http.HandlerFunc) *recordingServer {
	t.Helper()
	rs := &recordingServer{}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		rs.authorizations = append(rs.authorizations, r.Header.Get("Authorization"))
		rs.mu.Unlock()
		if handler != nil {
			handler(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(rs.Close)
	return rs
}

func (rs *recordingServer) seen() []string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return append([]string(nil), rs.authorizations...)
}

// countingTokenProvider plays the server's own auto-login.
type countingTokenProvider struct {
	mu    sync.Mutex
	calls int
	token string
}

func (p *countingTokenProvider) GetAccessToken(context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.token, nil
}

func (p *countingTokenProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func useServerToken(t *testing.T, token string) *countingTokenProvider {
	t.Helper()
	provider := &countingTokenProvider{token: token}
	tools.SetTokenProvider(provider)
	t.Cleanup(func() { tools.SetTokenProvider(nil) })
	return provider
}

// allToolboxes returns every tool the servers serve, plus payram_analytics,
// which is built but not registered.
func allToolboxes() []*mcp.Toolbox {
	return []*mcp.Toolbox{app.NewToolbox(), mcp.NewToolbox(tools.PayramAnalytics())}
}

// callsPayRam reports whether a tool reaches the PayRam API (it takes a token).
func callsPayRam(schema map[string]any) bool {
	props, _ := schema["properties"].(map[string]any)
	_, ok := props["token"]
	return ok
}

func schemaOf(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	return m
}

func TestNoToolAdvertisesBaseURL(t *testing.T) {
	for _, tb := range allToolboxes() {
		for _, desc := range tb.Describe() {
			schema := schemaOf(t, desc.InputSchema)
			props, _ := schema["properties"].(map[string]any)
			if _, ok := props["base_url"]; ok {
				t.Fatalf("tool %s still advertises base_url", desc.Name)
			}
		}
	}
}

// The audit's attack: a caller names its own host as base_url so the server
// sends the merchant's dashboard token there. Every tool must refuse it and
// nothing may reach either host.
func TestEveryToolRefusesBaseURL(t *testing.T) {
	payram := newRecordingServer(t, nil)
	attacker := newRecordingServer(t, nil)
	t.Setenv("PAYRAM_ANALYTICS_BASE_URL", payram.URL)
	t.Setenv("PAYRAM_ANALYTICS_TOKEN", "env-token")
	provider := useServerToken(t, "server-token")

	checked := 0
	for _, tb := range allToolboxes() {
		for _, desc := range tb.Describe() {
			for _, override := range []map[string]any{
				{"base_url": attacker.URL},
				{"base_url": attacker.URL, "token": "caller-token"},
				{"BASE_URL": attacker.URL},
			} {
				// Arguments that pass each tool's own checks, so the call
				// reaches the auth step.
				args := map[string]any{"action": "list_groups", "period1": "last_7_days", "period2": "last_30_days", "group_id": 1, "graph_id": 1, "amount": 10, "project_id": 1}
				for k, v := range override {
					args[k] = v
				}
				raw, _ := json.Marshal(args)
				_, errResp := tb.Call(context.Background(), desc.Name, raw)
				if !callsPayRam(schemaOf(t, desc.InputSchema)) {
					continue
				}
				checked++
				if errResp == nil {
					t.Fatalf("tool %s accepted base_url %v", desc.Name, args)
				}
				if errResp.Code != -32602 || !strings.Contains(errResp.Message, "base_url is not accepted") {
					t.Fatalf("tool %s: expected the base_url refusal, got %d %q", desc.Name, errResp.Code, errResp.Message)
				}
			}
		}
	}

	if checked < 3*18 {
		t.Fatalf("expected to check every PayRam tool, checked %d calls", checked)
	}
	if got := attacker.seen(); len(got) != 0 {
		t.Fatalf("caller-chosen host received %d requests: %v", len(got), got)
	}
	if got := payram.seen(); len(got) != 0 {
		t.Fatalf("a refused call still reached PayRam %d times", len(got))
	}
	if provider.callCount() != 0 {
		t.Fatalf("a refused call still asked for the server's token %d times", provider.callCount())
	}
}

func TestServerTokenGoesOnlyToConfiguredPayRam(t *testing.T) {
	payram := newRecordingServer(t, nil)
	t.Setenv("PAYRAM_ANALYTICS_BASE_URL", payram.URL+"/")
	t.Setenv("PAYRAM_ANALYTICS_TOKEN", "")
	useServerToken(t, "server-token")

	if _, errResp := app.NewToolbox().Call(context.Background(), "payram_discover_analytics", json.RawMessage(`{}`)); errResp != nil {
		t.Fatalf("discover_analytics: %d %s", errResp.Code, errResp.Message)
	}
	got := payram.seen()
	if len(got) != 1 || got[0] != "Bearer server-token" {
		t.Fatalf("configured PayRam expected one call with the server token, got %v", got)
	}
}

func TestCallerTokenGoesOnlyToConfiguredPayRam(t *testing.T) {
	payram := newRecordingServer(t, nil)
	t.Setenv("PAYRAM_ANALYTICS_BASE_URL", payram.URL)
	t.Setenv("PAYRAM_ANALYTICS_TOKEN", "")
	provider := useServerToken(t, "server-token")

	if _, errResp := app.NewToolbox().Call(context.Background(), "payram_discover_analytics", json.RawMessage(`{"token":"caller-token"}`)); errResp != nil {
		t.Fatalf("discover_analytics: %d %s", errResp.Code, errResp.Message)
	}
	got := payram.seen()
	if len(got) != 1 || got[0] != "Bearer caller-token" {
		t.Fatalf("configured PayRam expected one call with the caller token, got %v", got)
	}
	if provider.callCount() != 0 {
		t.Fatalf("a caller token must not trigger the server's login")
	}
}

// If the configured PayRam server redirects to a different host, the token
// does not travel with the redirect.
func TestRedirectToAnotherHostDropsToken(t *testing.T) {
	other := newRecordingServer(t, nil)
	otherURL := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)
	payram := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, otherURL+r.URL.Path, http.StatusTemporaryRedirect)
	})
	t.Setenv("PAYRAM_ANALYTICS_BASE_URL", payram.URL)
	t.Setenv("PAYRAM_ANALYTICS_TOKEN", "")
	useServerToken(t, "server-token")

	_, _ = app.NewToolbox().Call(context.Background(), "payram_discover_analytics", json.RawMessage(`{}`))

	if got := payram.seen(); len(got) != 1 || got[0] != "Bearer server-token" {
		t.Fatalf("configured PayRam expected the token once, got %v", got)
	}
	for _, auth := range other.seen() {
		if auth != "" {
			t.Fatalf("redirect target on another host received Authorization %q", auth)
		}
	}
}

func TestMissingConfiguredBaseURLIsAClearError(t *testing.T) {
	t.Setenv("PAYRAM_ANALYTICS_BASE_URL", "")
	t.Setenv("PAYRAM_ANALYTICS_TOKEN", "env-token")
	provider := useServerToken(t, "server-token")

	_, errResp := app.NewToolbox().Call(context.Background(), "payram_discover_analytics", json.RawMessage(`{}`))
	if errResp == nil || !strings.Contains(errResp.Message, "PAYRAM_ANALYTICS_BASE_URL") {
		t.Fatalf("expected a PAYRAM_ANALYTICS_BASE_URL error, got %+v", errResp)
	}
	if provider.callCount() != 0 {
		t.Fatalf("no login should happen without a configured PayRam URL")
	}
}
