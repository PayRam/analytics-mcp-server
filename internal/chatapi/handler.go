package chatapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
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
	apiKey      string
	allowed     []*net.IPNet
	httpClient  *http.Client
	logger      *logrus.Entry
}

// NewHandler constructs a chat API handler.
func NewHandler(logger *logrus.Entry, apiKey, allowlist, openaiKey, openaiModel, openaiBase, mcpURL string) *Handler {
	oc := &http.Client{Timeout: 30 * time.Second}
	return &Handler{
		openaiKey:   openaiKey,
		openaiModel: openaiModel,
		openaiBase:  strings.TrimRight(openaiBase, "/"),
		mcp:         chatserver.NewMCPClient(mcpURL),
		apiKey:      apiKey,
		allowed:     parseAllowlist(allowlist),
		httpClient:  oc,
		logger:      logger,
	}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/v1/chat/completions", h.handleChat)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func (h *Handler) handleChat(w http.ResponseWriter, r *http.Request) {
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

	system := OAChatMessage{Role: "system", Content: systemPrompt()}
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
		writeJSON(w, firstResp, http.StatusOK)
		return
	}

	// Execute tool calls via MCP, then ask LLM again with tool results.
	authToken := bearerToken(r.Header.Get("Authorization"))
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

Reply concisely with the actual data or a clear "No records" if the tool returns none.`
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
