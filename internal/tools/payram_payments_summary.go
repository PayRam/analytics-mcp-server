package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/payram/payram-analytics-mcp-server/internal/protocol"
)

// payramPaymentsSummaryTool finds and queries payment amount and count graphs dynamically.
// It first lists analytics groups, locates suitable graphs by name, then fetches graph data.
type payramPaymentsSummaryTool struct {
	client *http.Client
}

// PayramPaymentsSummary constructs the tool.
func PayramPaymentsSummary() *payramPaymentsSummaryTool {
	return &payramPaymentsSummaryTool{client: &http.Client{Timeout: 15 * time.Second}}
}

func (t *payramPaymentsSummaryTool) Descriptor() protocol.ToolDescriptor {
	return protocol.ToolDescriptor{
		Name:        "payram_payments_summary",
		Description: "Fetch total payments amount and number of payments by discovering analytics graphs dynamically. Actions: fetch.",
		InputSchema: &protocol.JSONSchema{
			Type: "object",
			Properties: map[string]protocol.JSONSchema{
				"token":    {Type: "string", Description: "Bearer token override; defaults to PAYRAM_ANALYTICS_TOKEN env"},
				"base_url": {Type: "string", Description: "API base override; required if PAYRAM_ANALYTICS_BASE_URL env is not set"},
				"days":     {Type: "integer", Description: "If set, fetch last N days using a custom range (overrides date_filter)"},
				"date_filter": {
					Type:        "string",
					Description: "analytics_date_filter (today, yesterday, last_7_days, last_30_days, this_month, last_month, last_6_months, forever, custom). Default last_30_days. If user asks for 'last N days' or any other range, pass that string here and include custom_start_date/custom_end_date or let the tool auto-convert to custom.",
				},
				"custom_start_date": {Type: "string", Description: "ISO date/time (RFC3339) start when date_filter=custom"},
				"custom_end_date":   {Type: "string", Description: "ISO date/time (RFC3339) end when date_filter=custom"},
				"currency_codes": {
					Type:        "array",
					Description: "Optional currency codes (e.g., BTC, ETH, USDT) when supported by the graph's filters",
					Items:       &protocol.JSONSchema{Type: "string"},
				},
			},
			Required: []string{},
		},
	}
}

type paymentsArgs struct {
	Token          string   `json:"token"`
	BaseURL        string   `json:"base_url"`
	Days           int      `json:"days"`
	DateFilter     string   `json:"date_filter"`
	CustomStartISO string   `json:"custom_start_date"`
	CustomEndISO   string   `json:"custom_end_date"`
	CurrencyCodes  []string `json:"currency_codes"`
}

func (t *payramPaymentsSummaryTool) Invoke(ctx context.Context, raw json.RawMessage) (protocol.CallResult, *protocol.ResponseError) {
	var args paymentsArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "invalid arguments"}
		}
	}

	// Resolve token/base
	token := strings.TrimSpace(args.Token)
	if token == "" {
		token = strings.TrimSpace(os.Getenv("PAYRAM_ANALYTICS_TOKEN"))
	}
	if token == "" {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32000, Message: "Missing token: set PAYRAM_ANALYTICS_TOKEN env or pass token"}
	}
	base := strings.TrimSpace(args.BaseURL)
	if base == "" {
		base = strings.TrimSpace(os.Getenv("PAYRAM_ANALYTICS_BASE_URL"))
	}
	base = strings.TrimSuffix(base, "/")
	if base == "" {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32000, Message: "Missing base_url: set PAYRAM_ANALYTICS_BASE_URL env or pass base_url"}
	}

	var dateFilter, customStart, customEnd string
	var errResp *protocol.ResponseError
	if args.Days > 0 {
		dateFilter = "custom"
		customStart, customEnd = lastNDaysRange(args.Days)
	} else {
		dateFilter, customStart, customEnd, errResp = normalizeDateFilter(args.DateFilter, args.CustomStartISO, args.CustomEndISO)
	}
	if errResp != nil {
		return protocol.CallResult{}, errResp
	}

	groups, err := t.listGroups(ctx, base, token)
	if err != nil {
		return protocol.CallResult{}, err
	}

	// If the caller explicitly requested a range (days/date_filter/custom), avoid the static “Numbers” group
	userProvidedRange := args.Days > 0 || strings.TrimSpace(args.DateFilter) != "" || strings.TrimSpace(args.CustomStartISO) != "" || strings.TrimSpace(args.CustomEndISO) != ""
	if userProvidedRange {
		groups = filterOutGroups(groups, func(name string) bool {
			return strings.Contains(name, "numbers")
		})
	}

	amountSel := pickGraph(groups, amountGraphNames())
	countSel := pickGraph(groups, countGraphNames())

	if amountSel == nil && countSel == nil {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32004, Message: "No matching graphs found for payments amount or count"}
	}

	respText := strings.Builder{}

	if amountSel != nil {
		payload := buildPayload(dateFilter, customStart, customEnd, args.CurrencyCodes, amountSel.filters)
		data, err := t.graphData(ctx, base, token, amountSel.groupID, amountSel.graphID, payload)
		if err != nil {
			return protocol.CallResult{}, err
		}
		respText.WriteString(fmt.Sprintf("Amount graph: group %d graph %d (%s)\n", amountSel.groupID, amountSel.graphID, amountSel.name))
		respText.WriteString(data)
		respText.WriteString("\n\n")
	}

	if countSel != nil {
		payload := buildPayload(dateFilter, customStart, customEnd, args.CurrencyCodes, countSel.filters)
		data, err := t.graphData(ctx, base, token, countSel.groupID, countSel.graphID, payload)
		if err != nil {
			return protocol.CallResult{}, err
		}
		respText.WriteString(fmt.Sprintf("Count graph: group %d graph %d (%s)\n", countSel.groupID, countSel.graphID, countSel.name))
		respText.WriteString(data)
	} else {
		respText.WriteString("Count graph not found with known name patterns. Tried: " + strings.Join(countGraphNames(), ", "))
	}

	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: strings.TrimSpace(respText.String())}}}, nil
}

// listGroups reuses the external list endpoint.
func (t *payramPaymentsSummaryTool) listGroups(ctx context.Context, base, token string) ([]paymentsGroupWrapper, *protocol.ResponseError) {
	url := base + "/api/v1/external-platform/all/analytics/groups"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, &protocol.ResponseError{Code: -32603, Message: fmt.Sprintf("build request: %v", err)}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, &protocol.ResponseError{Code: -32603, Message: fmt.Sprintf("http error: %v", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &protocol.ResponseError{Code: resp.StatusCode, Message: fmt.Sprintf("unexpected status: %d", resp.StatusCode)}
	}

	var data []paymentsGroupWrapper
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, &protocol.ResponseError{Code: -32603, Message: fmt.Sprintf("decode response: %v", err)}
	}
	return data, nil
}

// graphData posts to graph endpoint.
func (t *payramPaymentsSummaryTool) graphData(ctx context.Context, base, token string, groupID, graphID int, payload map[string]any) (string, *protocol.ResponseError) {
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s/api/v1/external-platform/all/analytics/groups/%d/graph/%d/data", base, groupID, graphID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", &protocol.ResponseError{Code: -32603, Message: fmt.Sprintf("build request: %v", err)}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := t.client.Do(req)
	if err != nil {
		return "", &protocol.ResponseError{Code: -32603, Message: fmt.Sprintf("http error: %v", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &protocol.ResponseError{Code: resp.StatusCode, Message: fmt.Sprintf("unexpected status: %d", resp.StatusCode)}
	}

	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return "", &protocol.ResponseError{Code: -32603, Message: fmt.Sprintf("decode response: %v", err)}
	}
	pretty, _ := json.MarshalIndent(raw, "", "  ")
	return string(pretty), nil
}

type paymentsGroupWrapper struct {
	ID             int                    `json:"id"`
	AnalyticsGroup paymentsAnalyticsGroup `json:"analyticsGroup"`
}

type paymentsAnalyticsGroup struct {
	ID      int                       `json:"id"`
	Name    string                    `json:"name"`
	Filters []paymentsAnalyticsFilter `json:"filters"`
	Graphs  []paymentsAnalyticsGraph  `json:"graphs"`
}

type paymentsAnalyticsFilter struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type paymentsAnalyticsGraph struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type graphSelection struct {
	groupID int
	graphID int
	name    string
	filters []paymentsAnalyticsFilter
}

// filterOutGroups removes analytics groups whose name matcher returns true.
func filterOutGroups(groups []paymentsGroupWrapper, shouldSkip func(lowerName string) bool) []paymentsGroupWrapper {
	out := make([]paymentsGroupWrapper, 0, len(groups))
	for _, g := range groups {
		name := strings.ToLower(g.AnalyticsGroup.Name)
		if shouldSkip(name) {
			continue
		}
		out = append(out, g)
	}
	return out
}

func amountGraphNames() []string {
	return []string{
		"payments in usd",
		"total payments",
		"payments in last 30 days",
	}
}

func countGraphNames() []string {
	return []string{
		"number of transactions",
		"transactions",
		"transactions count",
		"payments count",
		"count of payments",
		"number of payments",
		"total transactions",
	}
}

func isAllowedDateFilter(v string) bool {
	switch strings.ToLower(v) {
	case "today", "yesterday", "last_7_days", "last_30_days", "this_month", "last_month", "last_6_months", "forever", "custom":
		return true
	default:
		return false
	}
}

// normalizeDateFilter validates or converts free-form ranges (e.g., "last 10 days") to a supported filter.
// If a custom range is needed and not provided, it computes it in UTC as [now-N days, now+1 day).
func normalizeDateFilter(raw, customStart, customEnd string) (string, string, string, *protocol.ResponseError) {
	df := strings.ToLower(strings.TrimSpace(raw))
	if df == "" {
		df = "last_30_days"
	}
	if isAllowedDateFilter(df) {
		if df == "custom" {
			s := strings.TrimSpace(customStart)
			e := strings.TrimSpace(customEnd)
			if s == "" || e == "" {
				return "", "", "", &protocol.ResponseError{Code: -32602, Message: "custom_start_date and custom_end_date are required when date_filter=custom"}
			}
			return df, s, e, nil
		}
		return df, strings.TrimSpace(customStart), strings.TrimSpace(customEnd), nil
	}

	// Try to parse patterns like "last 10 days", "last_10_days", "last-10-days".
	n := extractDays(df)
	if n > 0 {
		now := time.Now().UTC()
		start := now.Add(-time.Duration(n) * 24 * time.Hour).Format(time.RFC3339Nano)
		end := now.Format(time.RFC3339Nano)
		return "custom", start, end, nil
	}

	return "", "", "", &protocol.ResponseError{Code: -32602, Message: fmt.Sprintf("invalid date_filter: %s", raw)}
}

// lastNDaysRange returns a UTC RFC3339 range for the last N days: [now-N days, now+1 day).
func lastNDaysRange(n int) (string, string) {
	if n <= 0 {
		n = 1
	}
	now := time.Now().UTC()
	start := now.Add(-time.Duration(n) * 24 * time.Hour).Format(time.RFC3339)
	end := now.Add(24 * time.Hour).Format(time.RFC3339)
	return start, end
}

// extractDays pulls the first integer found in a string like "last 10 days" or "last_10_days".
func extractDays(s string) int {
	re := regexp.MustCompile(`\d+`)
	m := re.FindString(s)
	if m == "" {
		return 0
	}
	n, err := strconv.Atoi(m)
	if err != nil {
		return 0
	}
	return n
}

// pickGraph finds the first graph whose name contains any of the needles (case-insensitive).
func pickGraph(groups []paymentsGroupWrapper, needles []string) *graphSelection {
	for _, g := range groups {
		for _, gr := range g.AnalyticsGroup.Graphs {
			name := strings.ToLower(gr.Name)
			for _, n := range needles {
				if strings.Contains(name, n) {
					return &graphSelection{groupID: g.AnalyticsGroup.ID, graphID: gr.ID, name: gr.Name, filters: g.AnalyticsGroup.Filters}
				}
			}
		}
	}
	return nil
}

// buildPayload crafts a payload with date_filter and optional currency codes if supported.
func buildPayload(dateFilter, customStart, customEnd string, currencyCodes []string, filters []paymentsAnalyticsFilter) map[string]any {
	payload := map[string]any{}

	// When using custom dates, only include the "custom" object without analytics_date_filter
	if dateFilter == "custom" {
		payload["custom"] = map[string]any{
			"start_date": strings.TrimSpace(customStart),
			"end_date":   strings.TrimSpace(customEnd),
		}
	} else {
		payload["analytics_date_filter"] = dateFilter
	}

	if len(currencyCodes) == 0 {
		return payload
	}

	// If a currency-related filter exists, include it.
	for _, f := range filters {
		t := strings.ToLower(f.Type)
		if t == "group_by_network_currency_filter" || t == "group_by_only_network_currency_filter" || t == "in_query_currency_filter" {
			payload["currency_codes"] = currencyCodes
			break
		}
	}
	return payload
}
