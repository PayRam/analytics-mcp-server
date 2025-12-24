package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/payram/payram-analytics-mcp-server/internal/agent/update"
)

// Secrets holds persisted secret material.
type Secrets struct {
	OpenAIAPIKey         string   `json:"openai_api_key,omitempty"`
	TelegramBotToken     string   `json:"telegram_bot_token,omitempty"`
	TelegramBaseURL      string   `json:"telegram_base_url,omitempty"`
	TelegramAllowedChats []string `json:"telegram_allowed_chats,omitempty"`
	PayramDashboardUser  string   `json:"payram_dashboard_username,omitempty"`
	PayramDashboardPass  string   `json:"payram_dashboard_password,omitempty"`
}

// Load returns secrets and their source: "env", "state", or "missing".
func Load(home string) (Secrets, string, error) {
	if home == "" {
		home = update.HomeDir()
	}

	var s Secrets
	source := ""

	path := pathFor(home)
	raw, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(raw, &s); err != nil {
			return Secrets{}, "", err
		}
		if hasSecretsSet(s) {
			source = "state"
		}
	} else {
		if !errors.Is(err, os.ErrNotExist) {
			return Secrets{}, "", err
		}
	}

	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		s.OpenAIAPIKey = key
		source = "env"
	}

	if hasSecretsSet(s) {
		if source == "" {
			source = "missing"
		}
		return s, source, nil
	}

	return Secrets{}, "missing", nil
}

// PutOpenAIKey writes the key atomically with 0600 permissions.

// PutOpenAIKey writes only the OpenAI key, preserving other stored fields.
func PutOpenAIKey(home, key string) error {
	if key == "" {
		return fmt.Errorf("openai api key empty")
	}
	if home == "" {
		home = update.HomeDir()
	}

	secrets, _, err := Load(home)
	if err != nil {
		return err
	}
	secrets.OpenAIAPIKey = key
	return write(home, secrets)
}

// PutTelegramConfig stores telegram and dashboard credentials, merging with existing secrets.
func PutTelegramConfig(home string, cfg Secrets) error {
	if home == "" {
		home = update.HomeDir()
	}

	secrets, _, err := Load(home)
	if err != nil {
		return err
	}

	if cfg.TelegramBotToken != "" {
		secrets.TelegramBotToken = cfg.TelegramBotToken
	}
	if cfg.TelegramBaseURL != "" {
		secrets.TelegramBaseURL = cfg.TelegramBaseURL
	}
	if len(cfg.TelegramAllowedChats) > 0 {
		secrets.TelegramAllowedChats = mergeUnique(secrets.TelegramAllowedChats, cfg.TelegramAllowedChats)
	}
	if cfg.PayramDashboardUser != "" {
		secrets.PayramDashboardUser = cfg.PayramDashboardUser
	}
	if cfg.PayramDashboardPass != "" {
		secrets.PayramDashboardPass = cfg.PayramDashboardPass
	}

	return write(home, secrets)
}

// DeleteOpenAIKey removes the stored key.
func DeleteOpenAIKey(home string) error {
	if home == "" {
		home = update.HomeDir()
	}
	path := pathFor(home)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func pathFor(home string) string {
	return filepath.Join(home, "state", "secrets.json")
}

func mergeUnique(existing, incoming []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(existing)+len(incoming))
	for _, v := range existing {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	for _, v := range incoming {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func hasSecretsSet(s Secrets) bool {
	if s.OpenAIAPIKey != "" || s.TelegramBotToken != "" || s.TelegramBaseURL != "" || len(s.TelegramAllowedChats) > 0 || s.PayramDashboardUser != "" || s.PayramDashboardPass != "" {
		return true
	}
	return false
}

func write(home string, secrets Secrets) error {
	dir := filepath.Join(home, "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	path := pathFor(home)
	tmp := path + ".tmp"

	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}

	enc, err := json.Marshal(secrets)
	if err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}

	if _, err := f.Write(enc); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}

	return nil
}
