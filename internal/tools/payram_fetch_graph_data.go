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

// payramFetchGraphDataTool fetches data from any specific analytics graph.
// This is a generic tool that can query any graph discovered via payram_discover_analytics.
type payramFetchGraphDataTool struct {
	client *http.Client
}

// PayramFetchGraphData constructs the tool.
func PayramFetchGraphData() *payramFetchGraphDataTool {
	return &payramFetchGraphDataTool{client: &http.Client{Timeout: 15 * time.Second}}
}

func (t *payramFetchGraphDataTool) Descriptor() protocol.ToolDescriptor {
	return protocol.ToolDescriptor{
		Name: "payram_fetch_graph_data",
		Description: `Fetch data from a specific PayRam analytics graph. Use after discovering available graphs with 'payram_discover_analytics'.

Graph types and their data formats:
- number_graph: Returns a single numeric value (e.g., total payments count)
- bar_graph: Returns time-series data with per-day/period breakdown (e.g., daily transaction counts)
- pie_with_info_graph: Returns distribution data (e.g., payments by currency)
- info_graph: Returns summary info cards
- table_graph: Returns tabular transaction data

Common graph IDs (may vary by environment):
- Group 1 (Numbers): Graphs 1-6 for key metrics
- Group 2 (Transaction Summary): Graph 7 (Payments in USD), Graph 8 (Number of Transactions per day)
- Group 3 (Deposit Distribution): Graph 9 (Payment distribution pie chart)
- Group 4 (Paying User Summary): Graphs 10-11 for user analytics
- Group 5 (Recent Transactions): Graph 12 for transaction table

For per-day transaction counts, use group_id=2, graph_id=8 with appropriate date_filter.`,
		InputSchema: &protocol.JSONSchema{
			Type: "object",
			Properties: map[string]protocol.JSONSchema{
				"token":    {Type: "string", Description: "Bearer token override; defaults to PAYRAM_ANALYTICS_TOKEN env"},
				"base_url": {Type: "string", Description: "API base override; required if PAYRAM_ANALYTICS_BASE_URL env is not set"},
				"group_id": {Type: "integer", Description: "Analytics group ID (required). Use payram_discover_analytics to find available groups."},
				"graph_id": {Type: "integer", Description: "Graph ID within the group (required). Use payram_discover_analytics to find available graphs."},
				"days":     {Type: "integer", Description: "If set, fetch last N days using a custom date range"},
				"date_filter": {
					Type:        "string",
					Description: "Date filter: today, yesterday, last_7_days, last_30_days, this_month, last_month, last_6_months, forever, custom. Default: last_30_days",
				},
				"custom_start_date": {Type: "string", Description: "ISO date/time (RFC3339) start when date_filter=custom"},
				"custom_end_date":   {Type: "string", Description: "ISO date/time (RFC3339) end when date_filter=custom"},
				"currency_codes": {
					Type:        "array",
					Description: "Optional currency filter: BTC, ETH, TRX, BASE, USDT, USDC, CBBTC",
					Items:       &protocol.JSONSchema{Type: "string"},
				},
				"group_by": {
					Type:        "string",
					Description: "For distribution graphs: 'currency_code' or 'blockchain_code'",
				},
			},
			Required: []string{"group_id", "graph_id"},
		},
	}
}

type fetchGraphArgs struct {
	Token          string   `json:"token"`
	BaseURL        string   `json:"base_url"`
	GroupID        int      `json:"group_id"`
	GraphID        int      `json:"graph_id"`
	Days           int      `json:"days"`
	DateFilter     string   `json:"date_filter"`
	CustomStartISO string   `json:"custom_start_date"`
	CustomEndISO   string   `json:"custom_end_date"`
	CurrencyCodes  []string `json:"currency_codes"`
	GroupBy        string   `json:"group_by"`
}

func (t *payramFetchGraphDataTool) Invoke(ctx context.Context, raw json.RawMessage) (protocol.CallResult, *protocol.ResponseError) {
	var args fetchGraphArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "invalid arguments"}
		}
	}

	if args.GroupID == 0 || args.GraphID == 0 {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "group_id and graph_id are required"}
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

	// Build flexible payload
	payload := t.buildPayload(dateFilter, customStart, customEnd, args.CurrencyCodes, args.GroupBy)

	data, err := t.graphData(ctx, base, token, args.GroupID, args.GraphID, payload)
	if err != nil {
		return protocol.CallResult{}, err
	}

	respText := strings.Builder{}
	respText.WriteString(fmt.Sprintf("Graph Data (group_id=%d, graph_id=%d, date_filter=%s):\n\n", args.GroupID, args.GraphID, dateFilter))
	respText.WriteString(data)

	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: strings.TrimSpace(respText.String())}}}, nil
}

func (t *payramFetchGraphDataTool) buildPayload(dateFilter, customStart, customEnd string, currencyCodes []string, groupBy string) map[string]any {
	payload := map[string]any{}
	if dateFilter == "custom" {
		payload["custom"] = map[string]any{
			"start_date": strings.TrimSpace(customStart),
			"end_date":   strings.TrimSpace(customEnd),
		}
	} else {
		payload["analytics_date_filter"] = dateFilter
	}
	if len(currencyCodes) > 0 {
		payload["currency_codes"] = currencyCodes
		payload["in_query_currency_filter"] = currencyCodes
	}
	if groupBy != "" {
		payload["group_by_only_network_currency_filter"] = map[string]string{
			"code": groupBy,
		}
	}
	return payload
}

func (t *payramFetchGraphDataTool) graphData(ctx context.Context, base, token string, groupID, graphID int, payload map[string]any) (string, *protocol.ResponseError) {
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
		return fmt.Sprintf("No data available for the requested time period.\nGraph type: %s\nThis means there were no transactions/payments in the selected date range. Try a longer time period (e.g., last_30_days, last_6_months, or forever).", graphResp.Type), nil
	}

	pretty, _ := json.MarshalIndent(graphResp, "", "  ")
	return string(pretty), nil
}
