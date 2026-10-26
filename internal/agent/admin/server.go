package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/payram/payram-analytics-mcp-server/internal/agent/secrets"
	"github.com/payram/payram-analytics-mcp-server/internal/agent/supervisor"
	"github.com/payram/payram-analytics-mcp-server/internal/agent/update"
	"github.com/payram/payram-analytics-mcp-server/internal/version"
)

// Supervisor defines the minimal interface required from the supervisor.
type Supervisor interface {
	RestartAll() error
	Status() supervisor.Status
	Logs(component string, tail int) []string
}

// NewMux builds the agent's HTTP routes. Every /admin route needs adminToken
// in the X-MCP-Key header.
func NewMux(sup Supervisor, adminToken string) http.Handler {
	if sup == nil {
		panic("supervisor must not be nil")
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/version", versionHandler)

	chatPort := envPort("PAYRAM_CHAT_PORT", 2358)
	mcpPort := envPort("PAYRAM_MCP_PORT", 3333)

	if chatProxyEnabled() {
		if proxy := buildServiceProxy("/v1/chat/completions", chatPort, "CHAT_PROXY_ERROR"); proxy != nil {
			mux.Handle("/v1/chat/completions", proxy)
		}
	}

	// Expose full chat API under /chat/*
	if proxy := buildPathProxy("/chat", chatPort, "CHAT_PROXY_ERROR"); proxy != nil {
		mux.Handle("/chat", proxy)
		mux.Handle("/chat/", proxy)
	}

	// Expose full MCP HTTP API under /mcp/*
	if proxy := buildPathProxy("/mcp", mcpPort, "MCP_PROXY_ERROR"); proxy != nil {
		mux.Handle("/mcp", proxy)
		mux.Handle("/mcp/", proxy)
	}

	adminGuard := NewAdminMiddleware(adminToken)
	mux.Handle("/admin/version", adminGuard(http.HandlerFunc(adminVersionHandler)))
	mux.Handle("/admin/update/available", adminGuard(http.HandlerFunc(updateAvailableHandler)))
	mux.Handle("/admin/update/apply", adminGuard(http.HandlerFunc(updateApplyHandler(sup))))
	mux.Handle("/admin/update/rollback", adminGuard(http.HandlerFunc(updateRollbackHandler(sup))))
	mux.Handle("/admin/update/status", adminGuard(http.HandlerFunc(updateStatusHandler)))
	mux.Handle("/admin/child/restart", adminGuard(http.HandlerFunc(restartHandler(sup))))
	mux.Handle("/admin/child/status", adminGuard(http.HandlerFunc(statusHandler(sup))))
	mux.Handle("/admin/logs", adminGuard(http.HandlerFunc(logsHandler(sup))))
	mux.Handle("/admin/secrets/openai", adminGuard(http.HandlerFunc(secretsHandler(sup))))
	mux.Handle("/admin/secrets/status", adminGuard(http.HandlerFunc(secretsStatusHandler)))

	return mux
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	RespondOK(w, http.StatusOK, map[string]any{"status": "healthy"})
}

func versionHandler(w http.ResponseWriter, _ *http.Request) {
	RespondOK(w, http.StatusOK, version.Get())
}

// chatProxyEnabled returns true when PAYRAM_AGENT_PROXY_CHAT is truthy (1/true/yes).
func chatProxyEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("PAYRAM_AGENT_PROXY_CHAT")))
	return v == "1" || v == "true" || v == "yes"
}

// buildServiceProxy proxies a single fixed path to a target port (no path rewrite).
func buildServiceProxy(path string, port int, errCode string) http.Handler {
	target, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			RespondError(w, http.StatusInternalServerError, errCode+"_TARGET_INVALID", err.Error())
		})
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		RespondError(w, http.StatusBadGateway, errCode, err.Error())
	}
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host
		req.URL.Path = path
	}
	return proxy
}

// buildPathProxy proxies any request under a prefix to a target port, stripping the prefix.
func buildPathProxy(prefix string, port int, errCode string) http.Handler {
	target, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			RespondError(w, http.StatusInternalServerError, errCode+"_TARGET_INVALID", err.Error())
		})
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		RespondError(w, http.StatusBadGateway, errCode, err.Error())
	}
	orig := proxy.Director
	proxy.Director = func(req *http.Request) {
		orig(req)
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host
		req.URL.Path = strings.TrimPrefix(req.URL.Path, prefix)
		if req.URL.Path == "" {
			req.URL.Path = "/"
		}
	}
	return proxy
}

func adminVersionHandler(w http.ResponseWriter, r *http.Request) {
	client := &http.Client{Timeout: 2 * time.Second}
	ctx := r.Context()

	chatPort := envPort("PAYRAM_CHAT_PORT", 2358)
	mcpPort := envPort("PAYRAM_MCP_PORT", 3333)

	chatURL := fmt.Sprintf("http://127.0.0.1:%d/version", chatPort)
	mcpURL := fmt.Sprintf("http://127.0.0.1:%d/version", mcpPort)

	chat := fetchChildVersion(ctx, client, chatURL)
	mcp := fetchChildVersion(ctx, client, mcpURL)

	RespondOK(w, http.StatusOK, map[string]any{
		"agent": version.Get(),
		"chat":  chat,
		"mcp":   mcp,
	})
}

func updateAvailableHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		RespondError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "only GET allowed")
		return
	}

	baseURL := os.Getenv("PAYRAM_AGENT_UPDATE_BASE_URL")
	if baseURL == "" {
		RespondError(w, http.StatusInternalServerError, "UPDATE_BASE_URL_MISSING", "update base URL not configured")
		return
	}

	pub := os.Getenv("PAYRAM_AGENT_UPDATE_PUBKEY_B64")
	if pub == "" {
		RespondError(w, http.StatusInternalServerError, "UPDATE_PUBKEY_MISSING", "update public key not configured")
		return
	}
	ignoreCompat := ignoreCompatEnabled()

	coreURL := os.Getenv("PAYRAM_CORE_URL")

	channel := r.URL.Query().Get("channel")
	if channel == "" {
		channel = "stable"
	}

	manifest, raw, sig, err := update.FetchManifest(r.Context(), baseURL, channel)
	if err != nil {
		RespondError(w, http.StatusInternalServerError, "UPDATE_FETCH_FAILED", err.Error())
		return
	}

	if err := update.VerifyManifest(raw, sig, pub); err != nil {
		RespondError(w, http.StatusInternalServerError, "SIGNATURE_INVALID", err.Error())
		return
	}

	status, err := update.LoadStatus()
	if err != nil {
		RespondError(w, http.StatusInternalServerError, "STATUS_LOAD_FAILED", err.Error())
		return
	}

	available := manifest.Version != "" && manifest.Version != status.CurrentVersion && !manifest.Revoked

	compatRange := manifest.Compatibility.PayramCore
	coreInfo := map[string]any{
		"min": compatRange.Min,
		"max": compatRange.Max,
	}

	compatResult := map[string]any{
		"ignored":    ignoreCompat,
		"compatible": false,
		"reason":     "",
	}

	if coreURL == "" {
		if ignoreCompat {
			compatResult["compatible"] = true
			compatResult["reason"] = "compatibility ignored: PAYRAM_CORE_URL not set"
			coreInfo["error_code"] = "CORE_URL_MISSING"
			coreInfo["error_message"] = "payram core URL not configured"
			coreInfo["compatible"] = compatResult["compatible"]
			coreInfo["reason"] = compatResult["reason"]
			coreInfo["ignored"] = ignoreCompat
			RespondOK(w, http.StatusOK, map[string]any{
				"available":       available,
				"current_version": status.CurrentVersion,
				"target_version":  manifest.Version,
				"notes":           manifest.Notes,
				"revoked":         manifest.Revoked,
				"payram_core":     coreInfo,
				"compat":          compatResult,
			})
			return
		}
		RespondError(w, http.StatusInternalServerError, "CORE_URL_MISSING", "payram core URL not configured")
		return
	}

	coreVersion, err := update.GetPayramCoreVersion(r.Context(), coreURL)
	if err != nil {
		if ignoreCompat {
			compatResult["compatible"] = true
			compatResult["reason"] = "compatibility ignored: core unreachable"
			coreInfo["error_code"] = "CORE_UNREACHABLE"
			coreInfo["error_message"] = err.Error()
			coreInfo["compatible"] = compatResult["compatible"]
			coreInfo["reason"] = compatResult["reason"]
			coreInfo["ignored"] = ignoreCompat
			RespondOK(w, http.StatusOK, map[string]any{
				"available":       available,
				"current_version": status.CurrentVersion,
				"target_version":  manifest.Version,
				"notes":           manifest.Notes,
				"revoked":         manifest.Revoked,
				"payram_core":     coreInfo,
				"compat":          compatResult,
			})
			return
		}
		RespondError(w, http.StatusInternalServerError, "CORE_UNREACHABLE", err.Error())
		return
	}

	coreInfo["current"] = coreVersion
	compatible, reason := update.IsCompatible(coreVersion, compatRange.Min, compatRange.Max)
	compatResult["compatible"] = compatible
	compatResult["reason"] = reason

	if ignoreCompat && !compatible {
		compatResult["compatible"] = true
		compatResult["reason"] = fmt.Sprintf("compatibility ignored: %s", reason)
	}

	coreInfo["compatible"] = compatResult["compatible"]
	coreInfo["reason"] = compatResult["reason"]
	coreInfo["ignored"] = ignoreCompat

	if !ignoreCompat && !compatible {
		compatResult["compatible"] = false
		RespondOK(w, http.StatusOK, map[string]any{
			"available":       available,
			"current_version": status.CurrentVersion,
			"target_version":  manifest.Version,
			"notes":           manifest.Notes,
			"revoked":         manifest.Revoked,
			"payram_core":     coreInfo,
			"compat":          compatResult,
		})
		return
	}

	RespondOK(w, http.StatusOK, map[string]any{
		"available":       available,
		"current_version": status.CurrentVersion,
		"target_version":  manifest.Version,
		"notes":           manifest.Notes,
		"revoked":         manifest.Revoked,
		"payram_core":     coreInfo,
		"compat":          compatResult,
	})
}

func updateApplyHandler(sup Supervisor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			RespondError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "only POST allowed")
			return
		}

		ignoreCompat := ignoreCompatEnabled()

		unlock, err := update.AcquireUpdateLock()
		if err != nil {
			if errors.Is(err, update.ErrUpdateInProgress) {
				RespondError(w, http.StatusConflict, "UPDATE_IN_PROGRESS", "update already in progress")
				return
			}
			RespondError(w, http.StatusInternalServerError, "LOCK_FAILED", err.Error())
			return
		}
		defer func() { _ = unlock() }()

		status, err := update.LoadStatus()
		if err != nil {
			RespondError(w, http.StatusInternalServerError, "STATUS_LOAD_FAILED", err.Error())
			return
		}
		status.MarkAttempt()
		if err := update.SaveStatus(status); err != nil {
			RespondError(w, http.StatusInternalServerError, "STATUS_SAVE_FAILED", err.Error())
			return
		}
		defer func() {
			status.InProgress = false
			_ = update.SaveStatus(status)
		}()

		baseURL := os.Getenv("PAYRAM_AGENT_UPDATE_BASE_URL")
		if baseURL == "" {
			RespondError(w, http.StatusInternalServerError, "UPDATE_BASE_URL_MISSING", "update base URL not configured")
			return
		}

		pub := os.Getenv("PAYRAM_AGENT_UPDATE_PUBKEY_B64")
		if pub == "" {
			RespondError(w, http.StatusInternalServerError, "UPDATE_PUBKEY_MISSING", "update public key not configured")
			return
		}

		coreURL := os.Getenv("PAYRAM_CORE_URL")

		channel := r.URL.Query().Get("channel")
		if channel == "" {
			channel = "stable"
		}

		manifest, raw, sig, err := update.FetchManifest(r.Context(), baseURL, channel)
		if err != nil {
			status.MarkFailure("UPDATE_FETCH_FAILED", err.Error())
			_ = update.SaveStatus(status)
			RespondError(w, http.StatusInternalServerError, "UPDATE_FETCH_FAILED", err.Error())
			return
		}

		if err := update.VerifyManifest(raw, sig, pub); err != nil {
			status.MarkFailure("SIGNATURE_INVALID", err.Error())
			_ = update.SaveStatus(status)
			RespondError(w, http.StatusInternalServerError, "SIGNATURE_INVALID", err.Error())
			return
		}

		status.LastAttemptVersion = manifest.Version
		if err := update.SaveStatus(status); err != nil {
			RespondError(w, http.StatusInternalServerError, "STATUS_SAVE_FAILED", err.Error())
			return
		}

		if manifest.Revoked {
			msg := "release revoked"
			status.MarkFailure("REVOKED_RELEASE", msg)
			_ = update.SaveStatus(status)
			RespondError(w, http.StatusBadRequest, "REVOKED_RELEASE", msg)
			return
		}

		warnings := []string{}
		coreVersion := ""
		if coreURL == "" {
			if ignoreCompat {
				warnings = append(warnings, "compatibility ignored: PAYRAM_CORE_URL not set")
			} else {
				status.MarkFailure("CORE_URL_MISSING", "payram core URL not configured")
				_ = update.SaveStatus(status)
				RespondError(w, http.StatusInternalServerError, "CORE_URL_MISSING", "payram core URL not configured")
				return
			}
		} else {
			cv, err := update.GetPayramCoreVersion(r.Context(), coreURL)
			if err != nil {
				if ignoreCompat {
					warnings = append(warnings, fmt.Sprintf("compatibility ignored: core unreachable (%s)", err.Error()))
				} else {
					status.MarkFailure("CORE_UNREACHABLE", err.Error())
					_ = update.SaveStatus(status)
					RespondError(w, http.StatusInternalServerError, "CORE_UNREACHABLE", err.Error())
					return
				}
			} else {
				coreVersion = cv
				compat := manifest.Compatibility.PayramCore
				compatible, reason := update.IsCompatible(coreVersion, compat.Min, compat.Max)
				if !compatible {
					if ignoreCompat {
						warnings = append(warnings, fmt.Sprintf("compatibility ignored: %s", reason))
					} else {
						if reason == "" {
							reason = "incompatible payram-core version"
						}
						status.MarkFailure("INCOMPATIBLE_CORE", reason)
						_ = update.SaveStatus(status)
						RespondError(w, http.StatusBadRequest, "INCOMPATIBLE_CORE", reason)
						return
					}
				}
			}
		}

		releaseDir := update.ReleaseDir(manifest.Version)
		stageDir := filepath.Join(update.ReleasesDir(), manifest.Version+".tmp-"+randHex(6))

		_ = os.RemoveAll(stageDir)
		if err := os.MkdirAll(stageDir, 0o755); err != nil {
			status.MarkFailure("STAGE_CREATE_FAILED", err.Error())
			_ = update.SaveStatus(status)
			RespondError(w, http.StatusInternalServerError, "STAGE_CREATE_FAILED", err.Error())
			return
		}

		download := func(url, path, sha string) error {
			if err := update.DownloadToFile(r.Context(), url, path); err != nil {
				return fmt.Errorf("download: %w", err)
			}
			if err := update.VerifySHA256(path, sha); err != nil {
				return fmt.Errorf("sha256: %w", err)
			}
			return os.Chmod(path, 0o755)
		}

		chatPath := filepath.Join(stageDir, "payram-analytics-chat")
		if err := download(manifest.Artifacts.Chat.URL, chatPath, manifest.Artifacts.Chat.SHA256); err != nil {
			status.MarkFailure("UPDATE_DOWNLOAD_FAILED", err.Error())
			_ = update.SaveStatus(status)
			RespondError(w, http.StatusInternalServerError, "UPDATE_DOWNLOAD_FAILED", err.Error())
			return
		}

		mcpPath := filepath.Join(stageDir, "payram-analytics-mcp")
		if err := download(manifest.Artifacts.MCP.URL, mcpPath, manifest.Artifacts.MCP.SHA256); err != nil {
			status.MarkFailure("UPDATE_DOWNLOAD_FAILED", err.Error())
			_ = update.SaveStatus(status)
			RespondError(w, http.StatusInternalServerError, "UPDATE_DOWNLOAD_FAILED", err.Error())
			return
		}

		if manifest.Artifacts.Bot != nil && strings.TrimSpace(manifest.Artifacts.Bot.URL) != "" {
			botPath := filepath.Join(stageDir, "payram-analytics-telegram-bot")
			if err := download(manifest.Artifacts.Bot.URL, botPath, manifest.Artifacts.Bot.SHA256); err != nil {
				status.MarkFailure("UPDATE_DOWNLOAD_FAILED", err.Error())
				_ = update.SaveStatus(status)
				RespondError(w, http.StatusInternalServerError, "UPDATE_DOWNLOAD_FAILED", err.Error())
				return
			}
		}

		_ = os.RemoveAll(releaseDir)
		if err := os.Rename(stageDir, releaseDir); err != nil {
			status.MarkFailure("FINALIZE_FAILED", err.Error())
			_ = update.SaveStatus(status)
			RespondError(w, http.StatusInternalServerError, "FINALIZE_FAILED", err.Error())
			return
		}

		if err := update.EnsureCompatSymlinks(releaseDir); err != nil {
			status.MarkFailure("FINALIZE_FAILED", err.Error())
			_ = update.SaveStatus(status)
			RespondError(w, http.StatusInternalServerError, "FINALIZE_FAILED", err.Error())
			return
		}

		oldTarget, err := update.UpdateSymlinks(releaseDir)
		if err != nil {
			status.MarkFailure("SYMLINK_UPDATE_FAILED", err.Error())
			_ = update.SaveStatus(status)
			RespondError(w, http.StatusInternalServerError, "SYMLINK_UPDATE_FAILED", err.Error())
			return
		}

		previousVersion := update.VersionFromTarget(oldTarget)
		status.CurrentVersion = manifest.Version
		status.PreviousVersion = previousVersion
		if err := update.SaveStatus(status); err != nil {
			RespondError(w, http.StatusInternalServerError, "STATUS_SAVE_FAILED", err.Error())
			return
		}

		if err := sup.RestartAll(); err != nil {
			status.MarkFailure("RESTART_FAILED", err.Error())
			_ = update.SaveStatus(status)
			RespondError(w, http.StatusInternalServerError, "RESTART_FAILED", err.Error())
			return
		}

		healthErr := waitForHealth(envPort("PAYRAM_CHAT_PORT", 2358), envPort("PAYRAM_MCP_PORT", 3333), healthTimeout())
		if healthErr != nil {
			_, _ = update.UpdateSymlinks(oldTarget)
			_ = sup.RestartAll()
			reloaded, err := update.LoadStatus()
			if err != nil {
				RespondError(w, http.StatusInternalServerError, "STATUS_LOAD_FAILED", err.Error())
				return
			}
			reloaded.MarkFailure("UPDATE_FAILED_ROLLED_BACK", healthErr.Error())
			reloaded.CurrentVersion = previousVersion
			reloaded.PreviousVersion = manifest.Version
			if reloaded.LastAttemptVersion == "" {
				reloaded.LastAttemptVersion = manifest.Version
				reloaded.LastAttemptAt = time.Now()
			}
			_ = update.SaveStatus(reloaded)
			status = reloaded
			RespondError(w, http.StatusInternalServerError, "UPDATE_FAILED_ROLLED_BACK", healthErr.Error())
			return
		}

		status.MarkSuccess(manifest.Version, previousVersion)
		if err := update.SaveStatus(status); err != nil {
			RespondError(w, http.StatusInternalServerError, "STATUS_SAVE_FAILED", err.Error())
			return
		}

		resp := map[string]any{"ok": true, "updated_to": manifest.Version}
		if len(warnings) > 0 {
			resp["warnings"] = warnings
		}

		RespondOK(w, http.StatusOK, resp)
	}
}

func updateRollbackHandler(sup Supervisor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			RespondError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "only POST allowed")
			return
		}

		unlock, err := update.AcquireUpdateLock()
		if err != nil {
			if errors.Is(err, update.ErrUpdateInProgress) {
				RespondError(w, http.StatusConflict, "UPDATE_IN_PROGRESS", "update already in progress")
				return
			}
			RespondError(w, http.StatusInternalServerError, "LOCK_FAILED", err.Error())
			return
		}
		defer func() { _ = unlock() }()

		status, err := update.LoadStatus()
		if err != nil {
			RespondError(w, http.StatusInternalServerError, "STATUS_LOAD_FAILED", err.Error())
			return
		}
		status.MarkAttempt()
		_ = update.SaveStatus(status)

		prevTarget, err := os.Readlink(update.PreviousSymlink())
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			status.MarkFailure("ROLLBACK_FAILED", err.Error())
			_ = update.SaveStatus(status)
			RespondError(w, http.StatusInternalServerError, "ROLLBACK_FAILED", err.Error())
			return
		}

		if prevTarget == "" {
			status.MarkFailure("NO_PREVIOUS_VERSION", "no previous version to roll back to")
			_ = update.SaveStatus(status)
			RespondError(w, http.StatusBadRequest, "NO_PREVIOUS_VERSION", "no previous version")
			return
		}

		oldCurrent, err := update.UpdateSymlinks(prevTarget)
		if err != nil {
			status.MarkFailure("SYMLINK_UPDATE_FAILED", err.Error())
			_ = update.SaveStatus(status)
			RespondError(w, http.StatusInternalServerError, "SYMLINK_UPDATE_FAILED", err.Error())
			return
		}

		if err := sup.RestartAll(); err != nil {
			status.MarkFailure("RESTART_FAILED", err.Error())
			_ = update.SaveStatus(status)
			RespondError(w, http.StatusInternalServerError, "RESTART_FAILED", err.Error())
			return
		}

		if err := waitForHealth(envPort("PAYRAM_CHAT_PORT", 2358), envPort("PAYRAM_MCP_PORT", 3333), healthTimeout()); err != nil {
			status.MarkFailure("ROLLBACK_HEALTH_FAILED", err.Error())
			_ = update.SaveStatus(status)
			RespondError(w, http.StatusInternalServerError, "ROLLBACK_HEALTH_FAILED", err.Error())
			return
		}

		status.CurrentVersion = update.VersionFromTarget(prevTarget)
		status.PreviousVersion = update.VersionFromTarget(oldCurrent)
		status.InProgress = false
		if status.LastAttemptVersion == "" {
			status.LastAttemptVersion = status.CurrentVersion
			status.LastAttemptAt = time.Now()
		}
		if err := update.SaveStatus(status); err != nil {
			RespondError(w, http.StatusInternalServerError, "STATUS_SAVE_FAILED", err.Error())
			return
		}

		RespondOK(w, http.StatusOK, map[string]any{"ok": true, "rolled_back_to": update.VersionFromTarget(prevTarget)})
	}
}

func updateStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		RespondError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "only GET allowed")
		return
	}

	status, err := update.LoadStatus()
	if err != nil {
		RespondError(w, http.StatusInternalServerError, "STATUS_LOAD_FAILED", err.Error())
		return
	}

	RespondOK(w, http.StatusOK, status)
}

func ignoreCompatEnabled() bool {
	v := strings.ToLower(os.Getenv("PAYRAM_AGENT_IGNORE_COMPAT"))
	return v == "1" || v == "true"
}

func restartHandler(sup Supervisor) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			RespondError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "only POST allowed")
			return
		}

		if err := sup.RestartAll(); err != nil {
			RespondError(w, http.StatusInternalServerError, "RESTART_FAILED", err.Error())
			return
		}

		RespondOK(w, http.StatusOK, map[string]any{"status": "restarted"})
	}
}

func statusHandler(sup Supervisor) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		RespondOK(w, http.StatusOK, sup.Status())
	}
}

func logsHandler(sup Supervisor) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		component := r.URL.Query().Get("component")
		if component == "" {
			RespondError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "component is required")
			return
		}

		tail := 200
		if rawTail := r.URL.Query().Get("tail"); rawTail != "" {
			if parsed, err := strconv.Atoi(rawTail); err == nil && parsed > 0 {
				tail = parsed
			}
		}

		logs := sup.Logs(component, tail)
		if logs == nil {
			RespondError(w, http.StatusBadRequest, "INVALID_COMPONENT", "component must be chat, mcp, or bot")
			return
		}

		RespondOK(w, http.StatusOK, map[string]any{
			"component": component,
			"lines":     logs,
		})
	}
}

func secretsHandler(sup Supervisor) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			var req struct {
				OpenAIAPIKey string `json:"openai_api_key"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				RespondError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
				return
			}
			if strings.TrimSpace(req.OpenAIAPIKey) == "" {
				RespondError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "openai_api_key is required")
				return
			}
			if err := secrets.PutOpenAIKey(update.HomeDir(), req.OpenAIAPIKey); err != nil {
				RespondError(w, http.StatusInternalServerError, "SECRET_SAVE_FAILED", err.Error())
				return
			}

			if sup != nil {
				if err := sup.RestartAll(); err != nil {
					RespondError(w, http.StatusInternalServerError, "RESTART_FAILED", err.Error())
					return
				}
			}

			RespondOK(w, http.StatusOK, map[string]any{"ok": true})
		case http.MethodDelete:
			if err := secrets.DeleteOpenAIKey(update.HomeDir()); err != nil {
				RespondError(w, http.StatusInternalServerError, "SECRET_DELETE_FAILED", err.Error())
				return
			}
			RespondOK(w, http.StatusOK, map[string]any{"ok": true})
		default:
			RespondError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "only PUT and DELETE allowed")
		}
	}
}

func secretsStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		RespondError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "only GET allowed")
		return
	}

	_, source, err := secrets.Load(update.HomeDir())
	if err != nil {
		RespondError(w, http.StatusInternalServerError, "SECRET_LOAD_FAILED", err.Error())
		return
	}

	set := source == "env" || source == "state"
	RespondOK(w, http.StatusOK, map[string]any{
		"openai_api_key_set": set,
		"source":             source,
	})
}

type childVersionResult struct {
	Info  *version.Info `json:"info,omitempty"`
	Error *respError    `json:"error,omitempty"`
}

func fetchChildVersion(ctx context.Context, client *http.Client, url string) childVersionResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return childVersionResult{Error: &respError{Code: "FETCH_FAILED", Message: err.Error()}}
	}

	resp, err := client.Do(req)
	if err != nil {
		return childVersionResult{Error: &respError{Code: "FETCH_FAILED", Message: err.Error()}}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return childVersionResult{Error: &respError{Code: "FETCH_FAILED", Message: fmt.Sprintf("status %d", resp.StatusCode)}}
	}

	var info version.Info
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return childVersionResult{Error: &respError{Code: "FETCH_FAILED", Message: err.Error()}}
	}

	return childVersionResult{Info: &info}
}

func envPort(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			return p
		}
	}
	return fallback
}

func healthTimeout() time.Duration {
	if v := os.Getenv("PAYRAM_AGENT_HEALTH_TIMEOUT_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return 20 * time.Second
}

func childHealthPath() string {
	path := os.Getenv("PAYRAM_AGENT_CHILD_HEALTH_PATH")
	if path == "" {
		return "/health"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func waitForHealth(chatPort, mcpPort int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if err := checkHealth(chatPort, mcpPort, childHealthPath()); err == nil {
			return nil
		} else {
			lastErr = err
		}

		if time.Now().After(deadline) {
			return lastErr
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func checkHealth(chatPort, mcpPort int, path string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	chatURL := fmt.Sprintf("http://127.0.0.1:%d%s", chatPort, path)
	mcpURL := fmt.Sprintf("http://127.0.0.1:%d%s", mcpPort, path)

	if err := pingOnce(client, chatURL); err != nil {
		return fmt.Errorf("chat health: %w", err)
	}
	if err := pingOnce(client, mcpURL); err != nil {
		return fmt.Errorf("mcp health: %w", err)
	}
	return nil
}

func pingOnce(client *http.Client, url string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
