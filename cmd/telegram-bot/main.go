package main

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/payram/payram-analytics-mcp-server/internal/agent/secrets"
	"github.com/payram/payram-analytics-mcp-server/internal/agent/update"
	"github.com/payram/payram-analytics-mcp-server/internal/logging"
	"github.com/payram/payram-analytics-mcp-server/internal/telegrambot"
	"github.com/sirupsen/logrus"
)

func main() {
	_ = godotenv.Load()

	logger, cleanup, err := logging.New("telegram-bot")
	if err != nil {
		logger = logrus.New().WithField("component", "telegram-bot")
	} else {
		defer cleanup()
	}

	cfg := loadConfig(logger)
	if strings.TrimSpace(cfg.Token) == "" {
		logger.Fatal("TELEGRAM_BOT_TOKEN not set")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := telegrambot.Run(ctx, cfg); err != nil && err != context.Canceled {
		logger.Fatalf("bot exited with error: %v", err)
	}
}

func loadConfig(logger *logrus.Entry) telegrambot.Config {
	home := update.HomeDir()
	sec, _, _ := secrets.Load(home)

	token := envOr("TELEGRAM_BOT_TOKEN", sec.TelegramBotToken)
	webhookBase := envOr("TELEGRAM_WEBHOOK_BASE_URL", sec.TelegramBaseURL)

	allowed := allowedFromEnvOrSecrets(sec)
	reloadFn := func() ([]string, error) {
		s, _, err := secrets.Load(home)
		if err != nil {
			return nil, err
		}
		return allowedFromEnvOrSecrets(s), nil
	}

	port := envOr("PAYRAM_CHAT_PORT", "2358")
	chatAPI := envOr("TELEGRAM_CHAT_API_URL", "http://127.0.0.1:"+port)
	chatPath := envOr("TELEGRAM_CHAT_API_PATH", "/v1/chat/completions/telegram")
	model := envOr("TELEGRAM_MODEL", envOr("OPENAI_MODEL", "gpt-4o-mini"))
	reloadInterval := durationOr(logger, "TELEGRAM_RELOAD_INTERVAL", 10*time.Second)
	webhookListen := envOr("TELEGRAM_WEBHOOK_LISTEN", ":8788")
	useMarkdown := strings.ToLower(strings.TrimSpace(envOr("TELEGRAM_BOT_MARKDOWN", "false"))) == "true"
	useHTML := strings.ToLower(strings.TrimSpace(envOr("TELEGRAM_BOT_HTML", "true"))) != "false"

	return telegrambot.Config{
		Token:          token,
		WebhookBase:    webhookBase,
		WebhookListen:  webhookListen,
		AllowedChats:   allowed,
		ChatAPIURL:     chatAPI,
		ChatAPIPath:    chatPath,
		Model:          model,
		PollTimeout:    25 * time.Second,
		IdleSleep:      1 * time.Second,
		UseMarkdown:    useMarkdown,
		UseHTML:        useHTML,
		ReloadInterval: reloadInterval,
		ReloadAllowed:  reloadFn,
		Logger:         logger,
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func splitCSV(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		p := strings.TrimSpace(part)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func allowedFromEnvOrSecrets(sec secrets.Secrets) []string {
	if raw := os.Getenv("TELEGRAM_ALLOWED_CHAT_IDS"); raw != "" {
		return splitCSV(raw)
	}
	return append([]string(nil), sec.TelegramAllowedChats...)
}

func durationOr(logger *logrus.Entry, key string, def time.Duration) time.Duration {
	raw := os.Getenv(key)
	if strings.TrimSpace(raw) == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		logger.WithField("key", key).WithError(err).Warn("invalid duration, using default")
		return def
	}
	return d
}
