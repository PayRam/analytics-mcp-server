package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/payram/payram-analytics-mcp-server/internal/chatapi"
	"github.com/sirupsen/logrus"
)

// Config holds runtime options for the Telegram bot.
type Config struct {
	Token          string
	WebhookBase    string
	WebhookListen  string
	AllowedChats   []string
	ChatAPIURL     string
	ChatAPIPath    string
	Model          string
	PollTimeout    time.Duration
	IdleSleep      time.Duration
	UseMarkdown    bool
	UseHTML        bool
	ReloadInterval time.Duration
	ReloadAllowed  func() ([]string, error)
	Logger         *logrus.Entry
}

// Run starts the bot loop (long polling).
func Run(ctx context.Context, cfg Config) error {
	if strings.TrimSpace(cfg.Token) == "" {
		return fmt.Errorf("telegram token required")
	}
	if cfg.ChatAPIURL == "" {
		return fmt.Errorf("chat api url required")
	}
	if cfg.Logger == nil {
		cfg.Logger = logrus.New().WithField("component", "telegram-bot")
	}
	pollTimeout := cfg.PollTimeout
	if pollTimeout <= 0 {
		pollTimeout = 25 * time.Second
	}
	idleSleep := cfg.IdleSleep
	if idleSleep <= 0 {
		idleSleep = 1 * time.Second
	}
	reloadEvery := cfg.ReloadInterval
	if cfg.ReloadAllowed != nil && reloadEvery <= 0 {
		reloadEvery = 10 * time.Second
	}
	webhookListen := cfg.WebhookListen
	if webhookListen == "" {
		webhookListen = ":8788"
	}

	allowed := make(map[string]struct{})
	for _, id := range cfg.AllowedChats {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		allowed[id] = struct{}{}
	}

	b := &bot{
		token:         cfg.Token,
		baseAPI:       fmt.Sprintf("https://api.telegram.org/bot%s", cfg.Token),
		webhookBase:   strings.TrimRight(cfg.WebhookBase, "/"),
		allowed:       allowed,
		chatAPI:       strings.TrimRight(cfg.ChatAPIURL, "/"),
		chatAPIPath:   cfg.ChatAPIPath,
		model:         cfg.Model,
		pollTimeout:   pollTimeout,
		idleSleep:     idleSleep,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
		log:           cfg.Logger,
		reloadAllowed: cfg.ReloadAllowed,
		reloadEvery:   reloadEvery,
		webhookListen: webhookListen,
		useMarkdown:   cfg.UseMarkdown,
		useHTML:       cfg.UseHTML,
	}

	mode := "polling"
	if b.webhookBase != "" {
		mode = "webhook"
	}

	b.log.WithFields(logrus.Fields{
		"mode":           mode,
		"chat_api":       b.chatAPI,
		"chat_api_path":  b.chatAPIPath,
		"webhook_base":   b.webhookBase,
		"allowed_chats":  len(allowed),
		"poll_timeout_s": int(pollTimeout.Seconds()),
		"reload_secs":    int(reloadEvery.Seconds()),
		"listen":         webhookListen,
		"use_html":       b.useHTML,
		"use_markdown":   b.useMarkdown,
	}).Info("telegram bot starting")

	if mode == "webhook" {
		return b.runWebhook(ctx)
	}
	return b.loop(ctx)
}

type bot struct {
	token         string
	baseAPI       string
	webhookBase   string
	allowed       map[string]struct{}
	chatAPI       string
	chatAPIPath   string
	model         string
	pollTimeout   time.Duration
	idleSleep     time.Duration
	httpClient    *http.Client
	log           *logrus.Entry
	reloadAllowed func() ([]string, error)
	reloadEvery   time.Duration
	webhookListen string
	useMarkdown   bool
	useHTML       bool
	mu            sync.RWMutex
}

func (b *bot) loop(ctx context.Context) error {
	var reloadTicker *time.Ticker
	if b.reloadAllowed != nil && b.reloadEvery > 0 {
		reloadTicker = time.NewTicker(b.reloadEvery)
		defer reloadTicker.Stop()
	}

	var offset int64
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-reloadTickerTick(reloadTicker):
			b.refreshAllowed()
		default:
		}

		updates, err := b.getUpdates(ctx, offset)
		if err != nil {
			b.log.WithError(err).Warn("getUpdates failed")
			time.Sleep(b.idleSleep)
			continue
		}

		if len(updates) == 0 {
			time.Sleep(b.idleSleep)
			continue
		}

		for _, up := range updates {
			offset = up.UpdateID + 1
			b.handleUpdate(ctx, up)
		}
	}
}

func (b *bot) runWebhook(ctx context.Context) error {
	if b.webhookBase == "" {
		return b.loop(ctx)
	}

	var reloadTicker *time.Ticker
	if b.reloadAllowed != nil && b.reloadEvery > 0 {
		reloadTicker = time.NewTicker(b.reloadEvery)
		defer reloadTicker.Stop()
	}

	path := "/telegram/webhook/" + url.PathEscape(b.token)
	webhookURL := b.webhookBase + path

	if err := b.setWebhook(ctx, webhookURL); err != nil {
		b.log.WithError(err).Error("setWebhook failed")
		return err
	}

	updates := make(chan Update, 100)
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var up Update
		if err := json.NewDecoder(r.Body).Decode(&up); err != nil {
			b.log.WithError(err).Warn("webhook decode failed")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case updates <- up:
		default:
			b.log.Warn("update channel full, dropping webhook update")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:    b.webhookListen,
		Handler: mux,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			b.log.WithError(err).Error("webhook server error")
		}
	}()

	b.log.WithFields(logrus.Fields{
		"listen":      b.webhookListen,
		"webhook_url": webhookURL,
	}).Info("webhook server started")

	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
			return ctx.Err()
		case <-reloadTickerTick(reloadTicker):
			b.refreshAllowed()
		case up := <-updates:
			b.handleUpdate(ctx, up)
		}
	}
}

type updateResponse struct {
	Ok     bool     `json:"ok"`
	Result []Update `json:"result"`
}

type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	Text      string `json:"text"`
	Chat      Chat   `json:"chat"`
	Date      int64  `json:"date"`
}

type Chat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Username string `json:"username"`
}

func (b *bot) getUpdates(ctx context.Context, offset int64) ([]Update, error) {
	params := url.Values{}
	if offset > 0 {
		params.Set("offset", strconv.FormatInt(offset, 10))
	}
	params.Set("timeout", fmt.Sprintf("%d", int(b.pollTimeout.Seconds())))
	params.Set("limit", "50")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseAPI+"/getUpdates?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("getUpdates status %d", resp.StatusCode)
	}

	var body updateResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if !body.Ok {
		return nil, fmt.Errorf("getUpdates not ok")
	}
	return body.Result, nil
}

func (b *bot) handleUpdate(ctx context.Context, up Update) {
	if up.Message == nil {
		return
	}
	msg := up.Message
	chatID := msg.Chat.ID
	chatIDStr := strconv.FormatInt(chatID, 10)

	entry := b.log.WithFields(logrus.Fields{
		"chat_id":   chatIDStr,
		"chat_type": msg.Chat.Type,
		"msg_id":    msg.MessageID,
	})

	if strings.TrimSpace(msg.Text) == "" {
		entry.Debug("skip non-text message")
		return
	}

	if !b.isAllowed(chatIDStr) {
		entry.Warn("chat not allowed")
		msg := fmt.Sprintf("*Access denied*\nAdd this chat\\_id to the allowlist: `%s`\nOn the host, run:\n`./setup_payram_agent.sh --add-telegram-chat-ids \"%s\"`\nThen retry your message\\.", escapeMarkdownV2(chatIDStr), escapeMarkdownV2(chatIDStr))
		if err := b.sendMessageMarkdown(ctx, chatID, msg); err != nil {
			entry.WithError(err).Warn("failed to send not-allowed notice")
		}
		return
	}

	entry.Info("processing message")

	start := time.Now()
	reply, err := b.callChatAPI(ctx, msg.Text)
	dur := time.Since(start).Round(time.Millisecond)

	if err != nil {
		entry.WithFields(logrus.Fields{"dur": dur}).WithError(err).Error("chat api failed")
		send := b.sendMessage
		if b.useHTML {
			send = b.sendMessageHTML
		} else if b.useMarkdown {
			send = b.sendMessageMarkdown
		}
		_ = send(ctx, chatID, "Sorry, something went wrong handling your request.")
		return
	}

	entry.WithFields(logrus.Fields{"dur": dur, "chars": len(reply)}).Info("sending reply")
	if strings.TrimSpace(reply) == "" {
		reply = "No response."
	}
	send := b.sendMessage
	if b.useHTML {
		send = b.sendMessageHTML
	} else if b.useMarkdown {
		send = b.sendMessageMarkdown
	}
	if err := send(ctx, chatID, reply); err != nil {
		if b.useHTML {
			entry.WithError(err).Warn("failed to send reply with html; retrying without markup")
			if err2 := b.sendMessage(ctx, chatID, reply); err2 != nil {
				entry.WithError(err2).Warn("failed to send reply fallback")
			}
		} else if b.useMarkdown {
			entry.WithError(err).Warn("failed to send reply with markdown; retrying without markdown")
			if err2 := b.sendMessage(ctx, chatID, reply); err2 != nil {
				entry.WithError(err2).Warn("failed to send reply fallback")
			}
		} else {
			entry.WithError(err).Warn("failed to send reply")
		}
	}
}

func (b *bot) callChatAPI(ctx context.Context, text string) (string, error) {
	reqBody := chatapi.ChatCompletionRequest{
		Model: b.model,
		Messages: []chatapi.OAChatMessage{{
			Role:    "user",
			Content: text,
		}},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	path := b.chatAPIPath
	if path == "" {
		path = "/v1/chat/completions"
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.chatAPI+path, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Telegram-Bot-Token", b.token)

	resp, err := b.httpClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("chat api status %d", resp.StatusCode)
	}

	var chatResp chatapi.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return "", err
	}
	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("chat api returned no choices")
	}

	return chatResp.Choices[0].Message.Content, nil
}

func (b *bot) sendMessage(ctx context.Context, chatID int64, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	data := url.Values{}
	data.Set("chat_id", strconv.FormatInt(chatID, 10))
	data.Set("text", text)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseAPI+"/sendMessage", strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("sendMessage status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (b *bot) sendMessageHTML(ctx context.Context, chatID int64, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	data := url.Values{}
	data.Set("chat_id", strconv.FormatInt(chatID, 10))
	data.Set("text", text)
	data.Set("parse_mode", "HTML")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseAPI+"/sendMessage", strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("sendMessage status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (b *bot) setWebhook(ctx context.Context, webhookURL string) error {
	data := url.Values{}
	data.Set("url", webhookURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseAPI+"/setWebhook", strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("setWebhook status %d", resp.StatusCode)
	}

	var body struct {
		Ok          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return err
	}
	if !body.Ok {
		return fmt.Errorf("setWebhook not ok: %s", body.Description)
	}
	return nil
}

func (b *bot) refreshAllowed() {
	if b.reloadAllowed == nil {
		return
	}
	ids, err := b.reloadAllowed()
	if err != nil {
		b.log.WithError(err).Warn("reload allowed chat ids failed")
		return
	}
	newSet := make(map[string]struct{})
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		newSet[id] = struct{}{}
	}

	b.mu.Lock()
	prev := len(b.allowed)
	b.allowed = newSet
	b.mu.Unlock()
	b.log.WithFields(logrus.Fields{"allowed_chats": len(newSet), "prev": prev}).Info("reloaded allowed chats")
}

func (b *bot) isAllowed(chatID string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.allowed[chatID]
	return ok
}

func reloadTickerTick(t *time.Ticker) <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.C
}

func (b *bot) sendMessageMarkdown(ctx context.Context, chatID int64, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	data := url.Values{}
	data.Set("chat_id", strconv.FormatInt(chatID, 10))
	data.Set("text", text)
	data.Set("parse_mode", "MarkdownV2")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseAPI+"/sendMessage", strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("sendMessage status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func escapeMarkdownV2(s string) string {
	replacer := strings.NewReplacer(
		"_", "\\_",
		"*", "\\*",
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
		"~", "\\~",
		"`", "\\`",
		">", "\\>",
		"#", "\\#",
		"+", "\\+",
		"-", "\\-",
		"=", "\\=",
		"|", "\\|",
		"{", "\\{",
		"}", "\\}",
		".", "\\.",
		"!", "\\!",
	)
	return replacer.Replace(s)
}
