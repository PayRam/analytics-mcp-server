package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/payram/payram-analytics-mcp-server/internal/agent/admin"
	"github.com/sirupsen/logrus"
)

// fakeAdminAPI serves the two update routes behind the real admin guard and
// records which ones the update loop got through to.
func fakeAdminAPI(t *testing.T, token string) (*httptest.Server, *[]string) {
	t.Helper()
	var reached []string
	routes := http.NewServeMux()
	routes.HandleFunc("/admin/update/available", func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"available": true, "current_version": "1.2.0", "target_version": "1.2.1"})
	})
	routes.HandleFunc("/admin/update/apply", func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(admin.NewAdminMiddleware(token)(routes))
	t.Cleanup(srv.Close)
	return srv, &reached
}

func TestAutoUpdateLoopSendsAdminToken(t *testing.T) {
	srv, reached := fakeAdminAPI(t, "agent-token")
	client := &http.Client{Timeout: 5 * time.Second}

	runAutoUpdateOnce(context.Background(), client, srv.URL, "stable", "agent-token", logrus.New().WithField("test", t.Name()))

	if len(*reached) != 2 || (*reached)[0] != "/admin/update/available" || (*reached)[1] != "/admin/update/apply" {
		t.Fatalf("update loop did not get through the admin guard: reached %v", *reached)
	}
}

func TestAutoUpdateLoopWithoutTokenIsRefused(t *testing.T) {
	srv, reached := fakeAdminAPI(t, "agent-token")
	client := &http.Client{Timeout: 5 * time.Second}

	runAutoUpdateOnce(context.Background(), client, srv.URL, "stable", "", logrus.New().WithField("test", t.Name()))

	if len(*reached) != 0 {
		t.Fatalf("a caller without the token reached %v", *reached)
	}
}
