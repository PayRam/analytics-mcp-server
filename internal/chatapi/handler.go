package chatapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/payram/payram-analytics-mcp-server/internal/chatserver"
	"github.com/payram/payram-analytics-mcp-server/internal/protocol"
	"github.com/sirupsen/logrus"
)

// Handler serves an OpenAI-compatible chat completions endpoint and resolves tool calls via MCP.
type Handler struct {
	openaiKey   string
	openaiModel string
	openaiBase  string
	mcp         *chatserver.MCPClient
	auth        func(ctx context.Context) (string, error)
	apiKey      string
	botBypass   string
	allowed     []*net.IPNet
	httpClient  *http.Client
	logger      *logrus.Entry
}

// NewHandler constructs a chat API handler.
func NewHandler(logger *logrus.Entry, apiKey, allowlist, botBypass, openaiKey, openaiModel, openaiBase, mcpURL string, authProvider func(ctx context.Context) (string, error)) *Handler {
	oc := &http.Client{Timeout: 30 * time.Second}
	mcpClient := chatserver.NewMCPClient(mcpURL)
	if authProvider != nil {
		mcpClient.SetAuthProvider(authProvider)
	}
	return &Handler{
		openaiKey:   openaiKey,
		openaiModel: openaiModel,
		openaiBase:  strings.TrimRight(openaiBase, "/"),
		mcp:         mcpClient,
		auth:        authProvider,
		apiKey:      apiKey,
		botBypass:   botBypass,
		allowed:     parseAllowlist(allowlist),
		httpClient:  oc,
		logger:      logger,
	}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/v1/chat/completions", h.handleChat)
	mux.HandleFunc("/v1/chat/completions/telegram", h.handleChatTelegram)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func (h *Handler) handleChat(w http.ResponseWriter, r *http.Request) {
	h.handleChatWithPrompt(w, r, systemPrompt, "markdown")
}

func (h *Handler) handleChatTelegram(w http.ResponseWriter, r *http.Request) {
	h.handleChatWithPrompt(w, r, systemPromptTelegram, "html")
}

// responseFormat: "markdown" escapes MarkdownV2; "html" leaves content as-is for parse_mode=HTML.
func (h *Handler) handleChatWithPrompt(w http.ResponseWriter, r *http.Request, prompt func() string, responseFormat string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.authorize(r) {
		h.logger.Warn("unauthorized request")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logger.Warnf("bad request: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Model == "" {
		req.Model = h.openaiModel
	}
	if len(req.Messages) == 0 {
		http.Error(w, "messages required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Build system prompt and tools from MCP.
	tools, err := h.mcp.ListTools(ctx)
	if err != nil {
		h.logger.Errorf("list tools error: %v", err)
		http.Error(w, fmt.Sprintf("list tools error: %v", err), http.StatusBadGateway)
		return
	}
	oaTools := convertTools(tools)

	system := OAChatMessage{Role: "system", Content: prompt()}
	messages := append([]OAChatMessage{system}, req.Messages...)

	firstReq := ChatCompletionRequest{
		Model:       req.Model,
		Messages:    messages,
		Tools:       oaTools,
		ToolChoice:  "auto",
		Temperature: sanitizeTemperature(req.Model, req.Temperature),
	}

	firstResp, err := h.callOpenAI(ctx, firstReq)
	if err != nil {
		h.logger.Errorf("openai first call error: %v", err)
		http.Error(w, fmt.Sprintf("openai error: %v", err), http.StatusBadGateway)
		return
	}
	if len(firstResp.Choices) == 0 {
		http.Error(w, "no choices", http.StatusBadGateway)
		return
	}

	choice := firstResp.Choices[0]
	if len(choice.Message.ToolCalls) == 0 {
		if responseFormat == "markdown" {
			escapeMarkdownResponse(&firstResp)
		} else if responseFormat == "html" {
			sanitizeTelegramHTMLResponse(&firstResp)
		}
		writeJSON(w, firstResp, http.StatusOK)
		return
	}

	// Execute tool calls via MCP, then ask LLM again with tool results.
	authToken := bearerToken(r.Header.Get("Authorization"))
	if authToken == "" && h.auth != nil {
		if tok, err := h.auth(ctx); err == nil {
			authToken = tok
		} else {
			h.logger.Warnf("auth provider failed: %v", err)
		}
	}
	toolMessages := make([]OAChatMessage, 0, len(choice.Message.ToolCalls))
	for _, tc := range choice.Message.ToolCalls {
		args := tc.Function.Arguments
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		var raw json.RawMessage = json.RawMessage(args)
		callArgs := mapFromRaw(raw)
		injectAuthToken(tc.Function.Name, authToken, callArgs)
		result, err := h.mcp.CallTool(ctx, tc.Function.Name, callArgs)
		if err != nil {
			h.logger.Errorf("tool error for %s: %v", tc.Function.Name, err)
			http.Error(w, fmt.Sprintf("tool error: %v", err), http.StatusBadGateway)
			return
		}
		rendered := renderContent(result)
		toolMessages = append(toolMessages, OAChatMessage{
			Role:       "tool",
			ToolCallID: tc.ID,
			Name:       tc.Function.Name,
			Content:    rendered,
		})
	}

	followMessages := append(messages, OAChatMessage{Role: "assistant", ToolCalls: choice.Message.ToolCalls})
	followMessages = append(followMessages, toolMessages...)

	secondReq := ChatCompletionRequest{
		Model:       req.Model,
		Messages:    followMessages,
		Temperature: sanitizeTemperature(req.Model, req.Temperature),
	}

	secondResp, err := h.callOpenAI(ctx, secondReq)
	if err != nil {
		h.logger.Errorf("openai second call error: %v", err)
		http.Error(w, fmt.Sprintf("openai error: %v", err), http.StatusBadGateway)
		return
	}
	if responseFormat == "markdown" {
		escapeMarkdownResponse(&secondResp)
	} else if responseFormat == "html" {
		sanitizeTelegramHTMLResponse(&secondResp)
	}
	writeJSON(w, secondResp, http.StatusOK)
}

func (h *Handler) callOpenAI(ctx context.Context, req ChatCompletionRequest) (ChatCompletionResponse, error) {
	var resp ChatCompletionResponse
	body, err := json.Marshal(req)
	if err != nil {
		return resp, fmt.Errorf("encode openai request: %w", err)
	}
	url := h.openaiBase + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, fmt.Errorf("build openai request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+h.openaiKey)

	httpResp, err := h.httpClient.Do(httpReq)
	if err != nil {
		return resp, fmt.Errorf("call openai: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, _ := io.ReadAll(httpResp.Body)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(respBody))
		if len(msg) > 400 {
			msg = msg[:400] + "..."
		}
		return resp, fmt.Errorf("openai status %d: %s", httpResp.StatusCode, msg)
	}

	if err := json.NewDecoder(bytes.NewReader(respBody)).Decode(&resp); err != nil {
		return resp, fmt.Errorf("decode openai response: %w", err)
	}
	return resp, nil
}

func (h *Handler) authorize(r *http.Request) bool {
	ip := parseRemoteIP(r.RemoteAddr)
	if isAllowed(ip, h.allowed) {
		return true
	}
	if h.botBypass != "" {
		if v := strings.TrimSpace(r.Header.Get("X-Telegram-Bot-Token")); v != "" {
			if subtle.ConstantTimeCompare([]byte(v), []byte(h.botBypass)) == 1 {
				return true
			}
		}
	}
	if h.apiKey == "" {
		return true
	}
	if v := r.Header.Get("X-MCP-Key"); v != "" {
		return strings.TrimSpace(v) == h.apiKey
	}
	return false
}

func parseRemoteIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		return net.ParseIP(host)
	}
	return net.ParseIP(remoteAddr)
}

func parseAllowlist(raw string) []*net.IPNet {
	var networks []*net.IPNet

	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			continue
		}
		networks = append(networks, network)
	}

	return networks
}

func isAllowed(ip net.IP, allowed []*net.IPNet) bool {
	if ip == nil {
		return false
	}

	if ip.IsLoopback() {
		return true
	}

	for _, network := range allowed {
		if network.Contains(ip) {
			return true
		}
	}

	return false
}

func writeJSON(w http.ResponseWriter, v any, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func systemPrompt() string {
	return `You are PayRam's analytics assistant. ALWAYS call MCP tools to answer questions—never guess, never ask the user for more details; pick the best tool and range and return what you can.

TOOL SELECTION GUIDE:
- For per-day/daily breakdown (e.g., "payments each day", "daily counts"): Use payram_daily_stats with days=N.
- For transaction counts over time: Use payram_transaction_counts with days=N.
- For total amounts/counts (all currencies): Use payram_payments_summary; for "last N days" set date_filter=last_7_days/last_30_days or days=N.
- For key metrics (total payments, users): Use payram_numbers_summary.
- For SPECIFIC CURRENCY queries (USDC, BTC, ETH, etc.): Use payram_currency_breakdown with currency_code set.
- For currency distribution breakdown: Use payram_deposit_distribution.
- For user growth (new vs recurring): Use payram_user_growth or payram_paying_users.
- For recent transactions table: Use payram_recent_transactions.
- For period comparison: Use payram_compare_periods.
- For any graph by ID: Use payram_fetch_graph_data (discover with payram_discover_analytics first).

OPERATING RULES:
- Do NOT ask the user for confirmation; assume defaults and proceed. If period is implied (e.g., "last 7 days"), set days=7 or date_filter=last_7_days. If no period is given, use last_30_days.
- If a tool returns empty data, reply with a concise "No records for that period" instead of asking questions.
- If a tool errors, report the error briefly and, if possible, try an alternate nearby tool (e.g., payram_payments_summary if numbers summary lacks data).

Reply concisely with the actual data or a clear "No records" if the tool returns none.

Always format your final answer as Markdown (use bullets, tables, or code blocks when useful).`
}

// systemPromptTelegram is tailored for Telegram with MarkdownV2-safe output.
func systemPromptTelegram() string {
	return "You are PayRam's Telegram analytics assistant. ALWAYS use MCP tools; never guess or ask for more details. Respond using Telegram HTML (parse_mode=HTML).\n\n" +
		"TOOL SELECTION GUIDE:\n" +
		"- payram_daily_stats: daily breakdown; default days=30 if none given.\n" +
		"- payram_transaction_counts: counts over time (days=N).\n" +
		"- payram_payments_summary: totals; use date_filter=last_7_days/last_30_days or days=N.\n" +
		"- payram_numbers_summary: key metrics (totals, users).\n" +
		"- payram_currency_breakdown: specific currency (set currency_code).\n" +
		"- payram_deposit_distribution: currency distribution breakdown.\n" +
		"- payram_user_growth or payram_paying_users: new vs recurring users.\n" +
		"- payram_recent_transactions: recent transactions table.\n" +
		"- payram_compare_periods: period comparisons.\n" +
		"- payram_fetch_graph_data: graph by id (discover via payram_discover_analytics).\n\n" +
		"OPERATING RULES:\n" +
		"- Assume defaults: if no period, use last_30_days.\n" +
		"- If a tool returns no data: reply 'No records for that period'.\n" +
		"- If a tool errors: mention it briefly.\n" +
		"- Respond concisely (<600 chars). Use ONLY Telegram-allowed HTML tags: <b>/<strong>, <i>/<em>, <u>/<ins>, <s>/<del>/<strike>, <code>, <pre>, <a href=...>, and <br>. No other tags.\n" +
		"- Format with brief sections: start labels in <b>...</b>, use <br> for new lines, and use plain text bullets like '• item' (no <ul>/<li>). Avoid tables."
}

// convert MCP tool descriptors to OpenAI tools schema.
func convertTools(tools []protocol.ToolDescriptor) []OATool {
	out := make([]OATool, 0, len(tools))
	for _, t := range tools {
		params := map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
		if t.InputSchema != nil {
			params = toParameterMap(*t.InputSchema)
		}
		out = append(out, OATool{
			Type: "function",
			Function: OAFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}
	return out
}

// toParameterMap converts our JSONSchema to a generic map for OpenAI.
func toParameterMap(s protocol.JSONSchema) map[string]interface{} {
	// Default to empty object schema.
	if s.Type == "" {
		s.Type = "object"
	}
	m := map[string]interface{}{"type": s.Type}
	if len(s.Required) > 0 {
		m["required"] = s.Required
	}
	if s.Description != "" {
		m["description"] = s.Description
	}
	if len(s.Enum) > 0 {
		m["enum"] = s.Enum
	}
	if s.Properties != nil {
		props := map[string]interface{}{}
		for k, v := range s.Properties {
			props[k] = toParameterMap(v)
		}
		m["properties"] = props
	} else if s.Type == "object" {
		m["properties"] = map[string]interface{}{}
	}
	if s.Items != nil {
		m["items"] = toParameterMap(*s.Items)
	} else if s.Type == "array" {
		m["items"] = map[string]interface{}{}
	}
	if s.AdditionalProperties != nil {
		m["additionalProperties"] = s.AdditionalProperties
	}
	return m
}

// renderContent joins call result content parts into a string.
func renderContent(result protocol.CallResult) string {
	var sb strings.Builder
	for i, c := range result.Content {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(c.Text)
	}
	return sb.String()
}

// sanitizeTemperature omits temperature when the target model does not support custom values.
// Mini models (gpt-4.1-mini, gpt-4o-mini, gpt-5-mini, etc.) reject non-default temps; drop to avoid 400s.
func sanitizeTemperature(model string, t *float64) *float64 {
	if t == nil {
		return nil
	}
	lowerModel := strings.ToLower(strings.TrimSpace(model))
	if (strings.Contains(lowerModel, "mini") || strings.Contains(lowerModel, "gpt-4.1") || strings.Contains(lowerModel, "gpt-4o")) && *t != 1.0 {
		return nil
	}
	return t
}

// escapeMarkdownResponse ensures Telegram MarkdownV2 safety for assistant messages.
func escapeMarkdownResponse(resp *ChatCompletionResponse) {
	for i := range resp.Choices {
		resp.Choices[i].Message.Content = escapeMarkdownV2(resp.Choices[i].Message.Content)
	}
}

// sanitizeTelegramHTMLResponse removes/rewrites unsupported HTML tags for Telegram parse_mode=HTML.
func sanitizeTelegramHTMLResponse(resp *ChatCompletionResponse) {
	for i := range resp.Choices {
		resp.Choices[i].Message.Content = sanitizeTelegramHTML(resp.Choices[i].Message.Content)
	}
}

// sanitizeTelegramHTML keeps a small subset of tags (b,strong,i,em,u,ins,s,strike,del,code,pre,a,br)
// and rewrites common list/paragraph tags into simple line breaks to avoid Telegram 400s.
func sanitizeTelegramHTML(s string) string {
	replacer := strings.NewReplacer(
		"<ul>", "",
		"</ul>", "",
		"<ol>", "",
		"</ol>", "",
		"<li>", "• ",
		"</li>", "<br>",
		"<p>", "",
		"</p>", "<br>",
		"<div>", "",
		"</div>", "<br>",
		"<br/>", "<br>",
		"<br />", "<br>",
		"<h1>", "<b>",
		"</h1>", "</b><br>",
		"<h2>", "<b>",
		"</h2>", "</b><br>",
		"<h3>", "<b>",
		"</h3>", "</b><br>",
		"<strong>", "<b>",
		"</strong>", "</b>",
		"<em>", "<i>",
		"</em>", "</i>",
		"<hr>", "<br>",
	)
	clean := replacer.Replace(s)
	clean = strings.ReplaceAll(clean, "<br>", "\n")

	allowed := map[string]struct{}{
		"b": {}, "strong": {}, "i": {}, "em": {}, "u": {}, "ins": {}, "s": {}, "del": {}, "strike": {}, "code": {}, "pre": {}, "a": {},
	}
	re := regexp.MustCompile(`<(/?)([a-zA-Z0-9]+)([^>]*)>`)
	clean = re.ReplaceAllStringFunc(clean, func(tag string) string {
		matches := re.FindStringSubmatch(tag)
		if len(matches) < 3 {
			return ""
		}
		name := strings.ToLower(matches[2])
		if _, ok := allowed[name]; !ok {
			return ""
		}
		// For <a>, preserve href attribute only.
		if name == "a" {
			// keep href if present
			hrefRe := regexp.MustCompile(`href\s*=\s*"([^"]+)"`)
			href := hrefRe.FindString(matches[3])
			if matches[1] == "/" {
				return "</a>"
			}
			if href == "" {
				return ""
			}
			return "<a " + href + ">"
		}
		// Self-closing br stays
		if name == "br" {
			return "<br>"
		}
		if matches[1] == "/" {
			return "</" + name + ">"
		}
		return "<" + name + ">"
	})

	return clean
}

// escapeMarkdownV2 escapes reserved characters for Telegram MarkdownV2 parse_mode.
func escapeMarkdownV2(s string) string {
	replacer := strings.NewReplacer(
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
		"~", "\\~",
		"`", "\\`",
		">", "\\>",
		"#", "\\#",
		"+", "\\+",
		"=", "\\=",
		"|", "\\|",
		"{", "\\{",
		"}", "\\}",
		"!", "\\!",
	)
	return replacer.Replace(s)
}

// bearerToken extracts the token portion from an Authorization header value.
func bearerToken(header string) string {
	if header == "" {
		return ""
	}
	v := strings.TrimSpace(header)
	if strings.HasPrefix(strings.ToLower(v), "bearer ") {
		return strings.TrimSpace(v[7:])
	}
	return v
}

// injectAuthToken injects an auth token into tool args if not already provided.
// All payram_* tools accept a "token" argument that defaults to PAYRAM_ANALYTICS_TOKEN env.
func injectAuthToken(toolName, token string, args map[string]any) {
	if token == "" {
		return
	}
	if args == nil {
		return
	}
	// Inject token for all payram_* tools that accept it
	if strings.HasPrefix(toolName, "payram_") {
		v, ok := args["token"]
		if !ok || strings.TrimSpace(fmt.Sprint(v)) == "" {
			args["token"] = token
		}
	}
}

// mapFromRaw attempts to turn raw JSON into a generic map; falls back to empty map on error.
func mapFromRaw(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{}
	}
	return m
}
