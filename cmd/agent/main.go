package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
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
	"github.com/payram/payram-analytics-mcp-server/internal/logging"
	"github.com/sirupsen/logrus"
)

func main() {
	_ = godotenv.Load()
	addr := os.Getenv("PAYRAM_AGENT_LISTEN_ADDR")
	if addr == "" {
		addr = ":9900"
	}

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

	handler := admin.NewMux(sup)
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

	startAutoUpdateLoop(ctx, logEntry, addr)

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

func startAutoUpdateLoop(ctx context.Context, logEntry *logrus.Entry, listenAddr string) {
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
	adminBase := "http://" + ensureHost(listenAddr)

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
				runAutoUpdateOnce(ctx, client, adminBase, channel, logEntry)
				ticker := time.NewTicker(interval)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						runAutoUpdateOnce(ctx, client, adminBase, channel, logEntry)
					}
				}
			}
		}
	}()
}

func runAutoUpdateOnce(ctx context.Context, client *http.Client, adminBase, channel string, logEntry *logrus.Entry) {
	availURL := fmt.Sprintf("%s/admin/update/available?channel=%s", strings.TrimRight(adminBase, "/"), url.QueryEscape(channel))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, availURL, nil)
	if err != nil {
		logEntry.WithError(err).Warn("auto-update: build available request failed")
		return
	}

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

func ensureHost(addr string) string {
	if strings.HasPrefix(addr, "http") {
		return addr
	}
	if strings.HasPrefix(addr, ":") {
		return net.JoinHostPort("127.0.0.1", strings.TrimPrefix(addr, ":"))
	}
	if strings.Contains(addr, "://") {
		return addr
	}
	if strings.Contains(addr, ":") {
		return addr
	}
	return net.JoinHostPort("127.0.0.1", addr)
}
