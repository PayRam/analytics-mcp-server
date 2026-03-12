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

// payramTransactionCountsTool fetches per-day transaction counts from the "Number of Transactions" bar graph.
// Returns daily breakdown of transaction counts and amounts.
type payramTransactionCountsTool struct {
	client *http.Client
}

// PayramTransactionCounts constructs the tool.
func PayramTransactionCounts() *payramTransactionCountsTool {
	return &payramTransactionCountsTool{client: &http.Client{Timeout: 15 * time.Second}}
}

func (t *payramTransactionCountsTool) Descriptor() protocol.ToolDescriptor {
	return protocol.ToolDescriptor{
		Name:        "payram_transaction_counts",
		Description: "Fetch per-day transaction counts. Returns daily breakdown showing the number of transactions and amounts for each day in the selected period. Use this when user asks for transaction counts per day, daily breakdown, or number of payments over time.",
		InputSchema: &protocol.JSONSchema{
			Type: "object",
			Properties: map[string]protocol.JSONSchema{
				"token":    {Type: "string", Description: "Bearer token override; defaults to PAYRAM_ANALYTICS_TOKEN env"},
				"base_url": {Type: "string", Description: "API base override; required if PAYRAM_ANALYTICS_BASE_URL env is not set"},
				"days":     {Type: "integer", Description: "If set, fetch last N days using a custom range (overrides date_filter)"},
				"date_filter": {
					Type:        "string",
					Description: "analytics_date_filter (today, yesterday, last_7_days, last_30_days, this_month, last_month, last_6_months, forever, custom). Default last_30_days.",
				},
				"custom_start_date": {Type: "string", Description: "ISO date/time (RFC3339) start when date_filter=custom"},
				"custom_end_date":   {Type: "string", Description: "ISO date/time (RFC3339) end when date_filter=custom"},
				"currency_codes": {
					Type:        "array",
					Description: "Optional currency codes filter (e.g., BTC, ETH, USDT)",
					Items:       &protocol.JSONSchema{Type: "string"},
				},
			},
			Required: []string{},
		},
	}
}

type txCountsArgs struct {
	Token          string   `json:"token"`
	BaseURL        string   `json:"base_url"`
	Days           int      `json:"days"`
	DateFilter     string   `json:"date_filter"`
	CustomStartISO string   `json:"custom_start_date"`
	CustomEndISO   string   `json:"custom_end_date"`
	CurrencyCodes  []string `json:"currency_codes"`
}

func (t *payramTransactionCountsTool) Invoke(ctx context.Context, raw json.RawMessage) (protocol.CallResult, *protocol.ResponseError) {
	var args txCountsArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "invalid arguments"}
		}
	}

	token, base, errResp := resolveAnalyticsAuth(ctx, args.Token, args.BaseURL)
	if errResp != nil {
		return protocol.CallResult{}, errResp
	}

	var dateFilter, customStart, customEnd string
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

	// Find the "Transaction Summary" group which contains both count and amount bar graphs
	var txSummaryGroup *paymentsGroupWrapper
	for i, g := range groups {
		name := strings.ToLower(g.AnalyticsGroup.Name)
		if strings.Contains(name, "transaction summary") {
			txSummaryGroup = &groups[i]
			break
		}
	}
	if txSummaryGroup == nil {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32004, Message: "Transaction Summary analytics group not found"}
	}

	respText := strings.Builder{}
	respText.WriteString(fmt.Sprintf("Transaction Counts - Per Day Breakdown (group %d, date_filter: %s):\n\n", txSummaryGroup.AnalyticsGroup.ID, dateFilter))

	// Build payload
	payload := buildPayload(dateFilter, customStart, customEnd, args.CurrencyCodes, txSummaryGroup.AnalyticsGroup.Filters)

	// Fetch data for each graph (should include "Number of Transactions" and "Payments in USD")
	for _, gr := range txSummaryGroup.AnalyticsGroup.Graphs {
		data, graphErr := t.graphData(ctx, base, token, txSummaryGroup.AnalyticsGroup.ID, gr.ID, payload)
		if graphErr != nil {
			respText.WriteString(fmt.Sprintf("- %s: error fetching data (%s)\n", gr.Name, graphErr.Message))
			continue
		}

		// Parse and format the bar graph data for better readability
		formatted := t.formatBarGraphData(gr.Name, data)
		respText.WriteString(formatted)
		respText.WriteString("\n")
	}

	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: strings.TrimSpace(respText.String())}}}, nil
}

// formatBarGraphData parses bar graph JSON and formats it as a readable per-day breakdown
func (t *payramTransactionCountsTool) formatBarGraphData(graphName, jsonData string) string {
	var result strings.Builder
	result.WriteString(fmt.Sprintf("## %s\n", graphName))

	// Try to parse as array of data points
	var dataPoints []map[string]interface{}
	if err := json.Unmarshal([]byte(jsonData), &dataPoints); err != nil {
		// If not array, return raw JSON
		result.WriteString(jsonData)
		return result.String()
	}

	if len(dataPoints) == 0 {
		result.WriteString("No data available for this period.\n")
		return result.String()
	}

	// Format each data point
	for _, dp := range dataPoints {
		timestamp := ""
		if ts, ok := dp["timestamp"].(string); ok {
			timestamp = ts
		} else if ts, ok := dp["date"].(string); ok {
			timestamp = ts
		} else if ts, ok := dp["x"].(string); ok {
			timestamp = ts
		}

		// Build a line for this data point
		line := fmt.Sprintf("- %s: ", timestamp)
		parts := []string{}
		for k, v := range dp {
			if k == "timestamp" || k == "date" || k == "x" {
				continue
			}
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
		line += strings.Join(parts, ", ")
		result.WriteString(line + "\n")
	}

	return result.String()
}

func (t *payramTransactionCountsTool) listGroups(ctx context.Context, base, token string) ([]paymentsGroupWrapper, *protocol.ResponseError) {
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

func (t *payramTransactionCountsTool) graphData(ctx context.Context, base, token string, groupID, graphID int, payload map[string]any) (string, *protocol.ResponseError) {
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
	return string(raw), nil
}
