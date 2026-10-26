package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/payram/payram-analytics-mcp-server/internal/agent/admin"
	"github.com/payram/payram-analytics-mcp-server/internal/agent/supervisor"
	"github.com/payram/payram-analytics-mcp-server/internal/agent/update"
	"github.com/payram/payram-analytics-mcp-server/internal/listen"
	"github.com/payram/payram-analytics-mcp-server/internal/logging"
	"github.com/sirupsen/logrus"
)

func main() {
	_ = godotenv.Load()
	addr := listen.Addr(os.Getenv("PAYRAM_AGENT_LISTEN_ADDR"), "9900")

	if seeded, version, err := update.EnsureSeedRelease(context.Background(), update.HomeDir()); err != nil {
		log.Fatalf("failed to seed release: %v", err)
	} else if seeded {
		log.Printf("seeded initial release %s", version)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sup, err := supervisor.NewFromEnv()
	if err != nil {
		log.Fatalf("failed to configure supervisor: %v", err)
	}
	if err := sup.Start(ctx); err != nil {
		log.Fatalf("failed to start supervisor: %v", err)
	}

	var logEntry *logrus.Entry

	adminToken, isAdminTokenGenerated, err := admin.ResolveAdminToken()
	if err != nil {
		log.Fatalf("agent -> main: resolve admin token: %v", err)
	}

	handler := admin.NewMux(sup, adminToken)
	srv := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	logger, cleanup, err := logging.New("agent")
	useLogger := err == nil
	if useLogger {
		defer cleanup()
		logger.Infof("agent starting on %s", addr)
		logEntry = logger
	} else {
		log.Printf("agent starting on %s (fallback logging): %v", addr, err)
		logEntry = logrus.New().WithField("component", "agent")
	}

	if warning := listen.ExposureWarning("Agent (admin API and /mcp, /chat proxies)", addr); warning != "" {
		logEntry.Warn(warning)
	}
	if isAdminTokenGenerated {
		logEntry.Warnf("%s is not set: /admin answers only this agent's own update checks. Set it to call /admin yourself.", admin.TokenEnv)
	}
	if strings.TrimSpace(os.Getenv("PAYRAM_AGENT_ADMIN_ALLOWLIST")) != "" {
		logEntry.Warnf("PAYRAM_AGENT_ADMIN_ALLOWLIST is ignored: every /admin request needs the %s header, whatever its address.", admin.KeyHeader)
	}

	startAutoUpdateLoop(ctx, logEntry, addr, adminToken)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			if useLogger {
				logger.Errorf("server error: %v", err)
			} else {
				log.Printf("server error: %v", err)
			}
			stop()
		}
	}()

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil && err != http.ErrServerClosed {
		if useLogger {
			logger.Errorf("shutdown error: %v", err)
		} else {
			log.Printf("shutdown error: %v", err)
		}
	}

	sup.Wait()
}

type updateAvailableResponse struct {
	Available      bool   `json:"available"`
	CurrentVersion string `json:"current_version"`
	TargetVersion  string `json:"target_version"`
}

func startAutoUpdateLoop(ctx context.Context, logEntry *logrus.Entry, listenAddr, adminToken string) {
	baseURL := strings.TrimSpace(os.Getenv("PAYRAM_AGENT_UPDATE_BASE_URL"))
	pub := strings.TrimSpace(os.Getenv("PAYRAM_AGENT_UPDATE_PUBKEY_B64"))
	if baseURL == "" || pub == "" {
		logEntry.Info("auto-update disabled (update base or pubkey missing)")
		return
	}

	channel := strings.TrimSpace(os.Getenv("PAYRAM_AGENT_UPDATE_CHANNEL"))
	if channel == "" {
		channel = "stable"
	}
	interval := 6 * time.Hour
	adminBase := "http://" + listen.LocalDialAddr(listenAddr)

	client := &http.Client{Timeout: 20 * time.Second}

	go func() {
		// initial delay to let services come up
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				runAutoUpdateOnce(ctx, client, adminBase, channel, adminToken, logEntry)
				ticker := time.NewTicker(interval)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						runAutoUpdateOnce(ctx, client, adminBase, channel, adminToken, logEntry)
					}
				}
			}
		}
	}()
}

// runAutoUpdateOnce asks this agent's own admin API for an update and applies
// it. Both calls carry the admin token: /admin trusts no caller by address.
func runAutoUpdateOnce(ctx context.Context, client *http.Client, adminBase, channel, adminToken string, logEntry *logrus.Entry) {
	availURL := fmt.Sprintf("%s/admin/update/available?channel=%s", strings.TrimRight(adminBase, "/"), url.QueryEscape(channel))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, availURL, nil)
	if err != nil {
		logEntry.WithError(err).Warn("auto-update: build available request failed")
		return
	}
	req.Header.Set(admin.KeyHeader, adminToken)

	resp, err := client.Do(req)
	if err != nil {
		logEntry.WithError(err).Warn("auto-update: available request failed")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		logEntry.WithFields(logrus.Fields{"status": resp.StatusCode, "body": strings.TrimSpace(string(body))}).Warn("auto-update: available request unexpected status")
		return
	}

	var body updateAvailableResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		logEntry.WithError(err).Warn("auto-update: decode available response failed")
		return
	}

	if !body.Available || strings.TrimSpace(body.TargetVersion) == "" {
		logEntry.WithField("current", body.CurrentVersion).Debug("auto-update: no update available")
		return
	}

	applyURL := fmt.Sprintf("%s/admin/update/apply?channel=%s", strings.TrimRight(adminBase, "/"), url.QueryEscape(channel))
	applyReq, err := http.NewRequestWithContext(ctx, http.MethodPost, applyURL, nil)
	if err != nil {
		logEntry.WithError(err).Warn("auto-update: build apply request failed")
		return
	}
	applyReq.Header.Set(admin.KeyHeader, adminToken)
	applyResp, err := client.Do(applyReq)
	if err != nil {
		logEntry.WithError(err).Warn("auto-update: apply request failed")
		return
	}
	defer applyResp.Body.Close()
	if applyResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(applyResp.Body)
		logEntry.WithFields(logrus.Fields{"status": applyResp.StatusCode, "body": strings.TrimSpace(string(body))}).Warn("auto-update: apply request unexpected status")
		return
	}

	logEntry.WithFields(logrus.Fields{"from": body.CurrentVersion, "to": body.TargetVersion}).Info("auto-update applied")
}
