package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/payram/payram-analytics-mcp-server/internal/protocol"
)

// payramDailyStatsTool provides per-day statistics for a given period.
type payramDailyStatsTool struct {
	client *http.Client
}

// PayramDailyStats constructs the tool.
func PayramDailyStats() *payramDailyStatsTool {
	return &payramDailyStatsTool{client: &http.Client{Timeout: 15 * time.Second}}
}

func (t *payramDailyStatsTool) Descriptor() protocol.ToolDescriptor {
	return protocol.ToolDescriptor{
		Name: "payram_daily_stats",
		Description: `Get per-day payment statistics including transaction counts and amounts.

Use this tool when user asks for:
- Daily transaction counts (e.g., "how many payments each day last week")
- Per-day payment amounts
- Day-by-day breakdown of payments
- Daily trends and patterns

Returns per-day data with:
- Number of transactions per day
- Payment amounts per day (in USD)
- Breakdown by currency if applicable`,
		InputSchema: &protocol.JSONSchema{
			Type: "object",
			Properties: map[string]protocol.JSONSchema{
				"token":    {Type: "string", Description: "Bearer token override; defaults to PAYRAM_ANALYTICS_TOKEN env"},
				"base_url": {Type: "string", Description: "API base override; required if PAYRAM_ANALYTICS_BASE_URL env is not set"},
				"days":     {Type: "integer", Description: "Fetch last N days (e.g., days=10 for last 10 days). This is the preferred way to specify time range."},
				"date_filter": {
					Type:        "string",
					Description: "Predefined date filter: today, yesterday, last_7_days, last_30_days, this_month, last_month. Default: last_7_days",
				},
				"currency_codes": {
					Type:        "array",
					Description: "Filter by currencies: BTC, ETH, TRX, BASE, USDT, USDC, CBBTC",
					Items:       &protocol.JSONSchema{Type: "string"},
				},
				"include_amounts": {
					Type:        "boolean",
					Description: "Include payment amounts in USD. Default: true",
				},
				"include_counts": {
					Type:        "boolean",
					Description: "Include transaction counts. Default: true",
				},
			},
			Required: []string{},
		},
	}
}

type dailyStatsArgs struct {
	Token          string   `json:"token"`
	BaseURL        string   `json:"base_url"`
	Days           int      `json:"days"`
	DateFilter     string   `json:"date_filter"`
	CurrencyCodes  []string `json:"currency_codes"`
	IncludeAmounts *bool    `json:"include_amounts"`
	IncludeCounts  *bool    `json:"include_counts"`
}

func (t *payramDailyStatsTool) Invoke(ctx context.Context, raw json.RawMessage) (protocol.CallResult, *protocol.ResponseError) {
	var args dailyStatsArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "invalid arguments"}
		}
	}

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
		df := args.DateFilter
		if df == "" {
			df = "last_7_days"
		}
		dateFilter, customStart, customEnd, errResp = normalizeDateFilter(df, "", "")
	}
	if errResp != nil {
		return protocol.CallResult{}, errResp
	}

	includeAmounts := args.IncludeAmounts == nil || *args.IncludeAmounts
	includeCounts := args.IncludeCounts == nil || *args.IncludeCounts

	groups, err := t.listGroups(ctx, base, token)
	if err != nil {
		return protocol.CallResult{}, err
	}

	var txGroup *paymentsGroupWrapper
	for i, g := range groups {
		name := strings.ToLower(g.AnalyticsGroup.Name)
		if strings.Contains(name, "transaction summary") {
			txGroup = &groups[i]
			break
		}
	}
	if txGroup == nil {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32004, Message: "Transaction Summary group not found"}
	}

	respText := strings.Builder{}
	if args.Days > 0 {
		respText.WriteString(fmt.Sprintf("# Daily Statistics (Last %d Days)\n\n", args.Days))
	} else {
		respText.WriteString(fmt.Sprintf("# Daily Statistics (%s)\n\n", dateFilter))
	}

	payload := map[string]any{}
	if dateFilter == "custom" {
		payload["custom"] = map[string]any{
			"start_date": customStart,
			"end_date":   customEnd,
		}
	} else {
		payload["analytics_date_filter"] = dateFilter
	}
	if len(args.CurrencyCodes) > 0 {
		payload["currency_codes"] = args.CurrencyCodes
	}

	for _, gr := range txGroup.AnalyticsGroup.Graphs {
		name := strings.ToLower(gr.Name)
		isAmount := strings.Contains(name, "usd") || strings.Contains(name, "amount")
		isCount := strings.Contains(name, "number") || strings.Contains(name, "count") || strings.Contains(name, "transactions")

		if (isAmount && !includeAmounts) || (isCount && !isAmount && !includeCounts) {
			continue
		}

		data, graphErr := t.graphData(ctx, base, token, txGroup.AnalyticsGroup.ID, gr.ID, payload)
		if graphErr != nil {
			respText.WriteString(fmt.Sprintf("## %s\nError: %s\n\n", gr.Name, graphErr.Message))
			continue
		}
		respText.WriteString(fmt.Sprintf("## %s\n%s\n\n", gr.Name, data))
	}

	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: strings.TrimSpace(respText.String())}}}, nil
}

func (t *payramDailyStatsTool) listGroups(ctx context.Context, base, token string) ([]paymentsGroupWrapper, *protocol.ResponseError) {
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

func (t *payramDailyStatsTool) graphData(ctx context.Context, base, token string, groupID, graphID int, payload map[string]any) (string, *protocol.ResponseError) {
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

	var graphResp struct {
		ID         int             `json:"id"`
		Type       string          `json:"type"`
		Data       json.RawMessage `json:"data"`
		Attributes json.RawMessage `json:"attributes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&graphResp); err != nil {
		return "", &protocol.ResponseError{Code: -32603, Message: fmt.Sprintf("decode response: %v", err)}
	}

	// Check if data is null or empty
	if len(graphResp.Data) == 0 || string(graphResp.Data) == "null" {
		return "No data for this period (no transactions recorded).", nil
	}

	pretty, _ := json.MarshalIndent(graphResp, "", "  ")
	return string(pretty), nil
}
