package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/payram/payram-analytics-mcp-server/internal/protocol"
)

// payramCurrencyBreakdownTool provides detailed payment breakdown by currency.
type payramCurrencyBreakdownTool struct {
	client *http.Client
}

// PayramCurrencyBreakdown constructs the tool.
func PayramCurrencyBreakdown() *payramCurrencyBreakdownTool {
	return &payramCurrencyBreakdownTool{client: &http.Client{Timeout: 15 * time.Second}}
}

func (t *payramCurrencyBreakdownTool) Descriptor() protocol.ToolDescriptor {
	return protocol.ToolDescriptor{
		Name: "payram_currency_breakdown",
		Description: `Get payment breakdown by cryptocurrency/currency.

Use cases:
- Get payment amount for a SPECIFIC currency (e.g., "USDC amount in last 5 days")
- See which currencies are most used for payments
- Compare payment volumes across BTC, ETH, USDT, etc.
- Analyze currency distribution over time

Supported currencies: BTC, ETH, TRX, BASE, USDT, USDC, CBBTC

Returns payment amounts grouped by currency. If currency_code is specified, returns only that currency's data.`,
		InputSchema: &protocol.JSONSchema{
			Type: "object",
			Properties: map[string]protocol.JSONSchema{
				"token":    {Type: "string", Description: "Bearer token override; defaults to PAYRAM_ANALYTICS_TOKEN env"},
				"base_url": {Type: "string", Description: "API base override; required if PAYRAM_ANALYTICS_BASE_URL env is not set"},
				"days":     {Type: "integer", Description: "Fetch last N days (e.g., 5, 7, 30, 90)"},
				"date_filter": {
					Type:        "string",
					Description: "Date filter: today, yesterday, last_7_days, last_30_days, this_month, last_month, last_6_months, forever. Default: last_30_days",
				},
				"currency_code": {
					Type:        "string",
					Description: "Filter for a specific currency: BTC, ETH, TRX, BASE, USDT, USDC, CBBTC. If set, returns only data for this currency.",
				},
				"group_by": {
					Type:        "string",
					Description: "Group by: 'currency_code' (individual currencies) or 'blockchain_code' (by network). Default: currency_code",
				},
			},
			Required: []string{},
		},
	}
}

type currencyBreakdownArgs struct {
	Token        string `json:"token"`
	BaseURL      string `json:"base_url"`
	Days         int    `json:"days"`
	DateFilter   string `json:"date_filter"`
	CurrencyCode string `json:"currency_code"`
	GroupBy      string `json:"group_by"`
}

func (t *payramCurrencyBreakdownTool) Invoke(ctx context.Context, raw json.RawMessage) (protocol.CallResult, *protocol.ResponseError) {
	var args currencyBreakdownArgs
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
		dateFilter, customStart, customEnd, errResp = normalizeDateFilter(args.DateFilter, "", "")
	}
	if errResp != nil {
		return protocol.CallResult{}, errResp
	}

	groupBy := strings.TrimSpace(args.GroupBy)
	if groupBy == "" {
		groupBy = "currency_code"
	}

	currencyFilter := strings.ToUpper(strings.TrimSpace(args.CurrencyCode))

	groups, err := t.listGroups(ctx, base, token)
	if err != nil {
		return protocol.CallResult{}, err
	}

	// Find "Deposit Distribution" group for pie/distribution data
	var distGroup *paymentsGroupWrapper
	for i, g := range groups {
		name := strings.ToLower(g.AnalyticsGroup.Name)
		if strings.Contains(name, "distribution") {
			distGroup = &groups[i]
			break
		}
	}
	if distGroup == nil {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32004, Message: "Distribution analytics group not found"}
	}

	respText := strings.Builder{}
	if currencyFilter != "" {
		respText.WriteString(fmt.Sprintf("# %s Payment Data (%s)\n\n", currencyFilter, dateFilter))
	} else {
		respText.WriteString(fmt.Sprintf("# Currency Breakdown (grouped by %s, %s)\n\n", groupBy, dateFilter))
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
	payload["group_by_only_network_currency_filter"] = map[string]string{
		"code": groupBy,
	}

	for _, gr := range distGroup.AnalyticsGroup.Graphs {
		data, graphErr := t.graphData(ctx, base, token, distGroup.AnalyticsGroup.ID, gr.ID, payload)
		if graphErr != nil {
			respText.WriteString(fmt.Sprintf("- %s: error (%s)\n", gr.Name, graphErr.Message))
			continue
		}

		// If currency filter is set, extract only that currency's data
		if currencyFilter != "" {
			extracted, found := t.extractCurrencyData(data, currencyFilter)
			if found {
				respText.WriteString(fmt.Sprintf("## %s\n%s\n\n", gr.Name, extracted))
			}
			// If not found, we continue to next graph silently
		} else {
			respText.WriteString(fmt.Sprintf("## %s\n%s\n\n", gr.Name, data))
		}
	}

	// If we have a currency filter but found nothing, return helpful message
	result := strings.TrimSpace(respText.String())
	if currencyFilter != "" && result == fmt.Sprintf("# %s Payment Data (%s)", currencyFilter, dateFilter) {
		return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: fmt.Sprintf("No %s transactions found in the selected period. The data might be grouped differently - try without currency_code to see all currencies.", currencyFilter)}}}, nil
	}

	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: result}}}, nil
}

// extractCurrencyData extracts data for a specific currency from JSON response
// Returns the extracted data and whether it was found
func (t *payramCurrencyBreakdownTool) extractCurrencyData(jsonData, currencyCode string) (string, bool) {
	log.Printf("[payram_currency_breakdown] extractCurrencyData looking for %s in: %s", currencyCode, jsonData[:min(200, len(jsonData))])

	var data any
	if err := json.Unmarshal([]byte(jsonData), &data); err != nil {
		log.Printf("[payram_currency_breakdown] JSON parse error: %v", err)
		return "", false
	}

	// Handle array response (list of currency data)
	if arr, ok := data.([]any); ok {
		log.Printf("[payram_currency_breakdown] Response is array with %d items", len(arr))
		for _, item := range arr {
			if m, ok := item.(map[string]any); ok {
				// Check various field names that might contain the currency code
				for _, field := range []string{"currency_code", "code", "name", "label", "currency"} {
					if code, exists := m[field]; exists {
						codeStr := fmt.Sprint(code)
						if strings.EqualFold(codeStr, currencyCode) {
							pretty, _ := json.MarshalIndent(m, "", "  ")
							return string(pretty), true
						}
					}
				}
			}
		}
	}

	// Handle object response with currency keys
	if obj, ok := data.(map[string]any); ok {
		log.Printf("[payram_currency_breakdown] Response is object with keys: %v", getKeys(obj))
		// Direct lookup
		if val, exists := obj[currencyCode]; exists {
			pretty, _ := json.MarshalIndent(val, "", "  ")
			return string(pretty), true
		}
		// Case-insensitive lookup
		for key, val := range obj {
			if strings.EqualFold(key, currencyCode) {
				pretty, _ := json.MarshalIndent(val, "", "  ")
				return string(pretty), true
			}
		}
		// Check if it's nested data with "data" key
		if dataArr, exists := obj["data"]; exists {
			if arr, ok := dataArr.([]any); ok {
				for _, item := range arr {
					if m, ok := item.(map[string]any); ok {
						for _, field := range []string{"currency_code", "code", "name", "label", "currency"} {
							if code, exists := m[field]; exists {
								if strings.EqualFold(fmt.Sprint(code), currencyCode) {
									pretty, _ := json.MarshalIndent(m, "", "  ")
									return string(pretty), true
								}
							}
						}
					}
				}
			}
		}
	}

	return "", false
}

func getKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (t *payramCurrencyBreakdownTool) listGroups(ctx context.Context, base, token string) ([]paymentsGroupWrapper, *protocol.ResponseError) {
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

func (t *payramCurrencyBreakdownTool) graphData(ctx context.Context, base, token string, groupID, graphID int, payload map[string]any) (string, *protocol.ResponseError) {
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
