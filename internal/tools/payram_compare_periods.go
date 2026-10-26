package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/payram/payram-analytics-mcp-server/internal/protocol"
)

// payramComparePeriodsTool compares analytics data between two time periods.
// Useful for analyzing growth, trends, and period-over-period changes.
type payramComparePeriodsTool struct {
	client *http.Client
}

// PayramComparePeriods constructs the tool.
func PayramComparePeriods() *payramComparePeriodsTool {
	return &payramComparePeriodsTool{client: &http.Client{Timeout: 30 * time.Second}}
}

func (t *payramComparePeriodsTool) Descriptor() protocol.ToolDescriptor {
	return protocol.ToolDescriptor{
		Name: "payram_compare_periods",
		Description: `Compare PayRam analytics data between two time periods. 

Use cases:
- Compare this month vs last month
- Compare last 7 days vs previous 7 days
- Analyze week-over-week or month-over-month growth
- Identify trends and changes in payment patterns

Returns data from both periods for comparison including:
- Transaction counts and amounts
- Percentage changes between periods`,
		InputSchema: &protocol.JSONSchema{
			Type: "object",
			Properties: map[string]protocol.JSONSchema{
				"token": {Type: "string", Description: "Bearer token override; defaults to PAYRAM_ANALYTICS_TOKEN env"},
				"period1": {
					Type:        "string",
					Description: "First period: today, yesterday, last_7_days, last_30_days, this_month, last_month, last_6_months",
				},
				"period2": {
					Type:        "string",
					Description: "Second period to compare against (e.g., compare this_month with last_month)",
				},
				"metric": {
					Type:        "string",
					Description: "Metric to compare: 'amount' (payments in USD), 'count' (number of transactions), or 'both'. Default: both",
				},
				"currency_codes": {
					Type:        "array",
					Description: "Optional currency filter: BTC, ETH, TRX, BASE, USDT, USDC, CBBTC",
					Items:       &protocol.JSONSchema{Type: "string"},
				},
			},
			Required: []string{"period1", "period2"},
		},
	}
}

type compareArgs struct {
	Token         string   `json:"token"`
	BaseURL       string   `json:"base_url"`
	Period1       string   `json:"period1"`
	Period2       string   `json:"period2"`
	Metric        string   `json:"metric"`
	CurrencyCodes []string `json:"currency_codes"`
}

func (t *payramComparePeriodsTool) Invoke(ctx context.Context, raw json.RawMessage) (protocol.CallResult, *protocol.ResponseError) {
	var args compareArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "invalid arguments"}
		}
	}

	if args.Period1 == "" || args.Period2 == "" {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "period1 and period2 are required"}
	}

	token, base, errResp := resolveAnalyticsAuth(ctx, args.Token, args.BaseURL)
	if errResp != nil {
		return protocol.CallResult{}, errResp
	}

	metric := strings.ToLower(strings.TrimSpace(args.Metric))
	if metric == "" {
		metric = "both"
	}

	// Find Transaction Summary group (contains amount and count graphs)
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
	respText.WriteString(fmt.Sprintf("# Period Comparison: %s vs %s\n\n", args.Period1, args.Period2))

	// Find amount and count graph IDs
	var amountGraphID, countGraphID int
	for _, gr := range txGroup.AnalyticsGroup.Graphs {
		name := strings.ToLower(gr.Name)
		if strings.Contains(name, "payments in usd") || strings.Contains(name, "amount") {
			amountGraphID = gr.ID
		}
		if strings.Contains(name, "number of transactions") || strings.Contains(name, "count") {
			countGraphID = gr.ID
		}
	}

	// Fetch and compare data
	if (metric == "amount" || metric == "both") && amountGraphID > 0 {
		respText.WriteString("## Payments in USD\n\n")

		data1, _ := t.fetchPeriodData(ctx, base, token, txGroup.AnalyticsGroup.ID, amountGraphID, args.Period1, args.CurrencyCodes)
		data2, _ := t.fetchPeriodData(ctx, base, token, txGroup.AnalyticsGroup.ID, amountGraphID, args.Period2, args.CurrencyCodes)

		respText.WriteString(fmt.Sprintf("### %s:\n%s\n\n", args.Period1, data1))
		respText.WriteString(fmt.Sprintf("### %s:\n%s\n\n", args.Period2, data2))
	}

	if (metric == "count" || metric == "both") && countGraphID > 0 {
		respText.WriteString("## Number of Transactions\n\n")

		data1, _ := t.fetchPeriodData(ctx, base, token, txGroup.AnalyticsGroup.ID, countGraphID, args.Period1, args.CurrencyCodes)
		data2, _ := t.fetchPeriodData(ctx, base, token, txGroup.AnalyticsGroup.ID, countGraphID, args.Period2, args.CurrencyCodes)

		respText.WriteString(fmt.Sprintf("### %s:\n%s\n\n", args.Period1, data1))
		respText.WriteString(fmt.Sprintf("### %s:\n%s\n\n", args.Period2, data2))
	}

	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: strings.TrimSpace(respText.String())}}}, nil
}

func (t *payramComparePeriodsTool) fetchPeriodData(ctx context.Context, base, token string, groupID, graphID int, period string, currencyCodes []string) (string, *protocol.ResponseError) {
	payload := map[string]any{
		"analytics_date_filter": period,
	}
	if len(currencyCodes) > 0 {
		payload["currency_codes"] = currencyCodes
	}

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

func (t *payramComparePeriodsTool) listGroups(ctx context.Context, base, token string) ([]paymentsGroupWrapper, *protocol.ResponseError) {
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
