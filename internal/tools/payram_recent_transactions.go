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

// payramRecentTransactionsTool fetches recent transactions table data.
type payramRecentTransactionsTool struct {
	client *http.Client
}

// PayramRecentTransactions constructs the tool.
func PayramRecentTransactions() *payramRecentTransactionsTool {
	return &payramRecentTransactionsTool{client: &http.Client{Timeout: 15 * time.Second}}
}

func (t *payramRecentTransactionsTool) Descriptor() protocol.ToolDescriptor {
	return protocol.ToolDescriptor{
		Name:        "payram_recent_transactions",
		Description: "Fetch recent transactions table: list of recent payments with details like amount, currency, timestamp, user, etc.",
		InputSchema: &protocol.JSONSchema{
			Type: "object",
			Properties: map[string]protocol.JSONSchema{
				"token":    {Type: "string", Description: "Bearer token override; defaults to PAYRAM_ANALYTICS_TOKEN env"},
				"base_url": {Type: "string", Description: "API base override; required if PAYRAM_ANALYTICS_BASE_URL env is not set"},
				"currency_codes": {
					Type:        "array",
					Description: "Optional currency codes filter (e.g., BTC, ETH, USDT)",
					Items:       &protocol.JSONSchema{Type: "string"},
				},
				"limit": {Type: "integer", Description: "Optional limit on number of transactions to return"},
			},
			Required: []string{},
		},
	}
}

type recentTxArgs struct {
	Token         string   `json:"token"`
	BaseURL       string   `json:"base_url"`
	CurrencyCodes []string `json:"currency_codes"`
	Limit         int      `json:"limit"`
}

func (t *payramRecentTransactionsTool) Invoke(ctx context.Context, raw json.RawMessage) (protocol.CallResult, *protocol.ResponseError) {
	var args recentTxArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "invalid arguments"}
		}
	}

	token, base, errResp := resolveAnalyticsAuth(ctx, args.Token, args.BaseURL)
	if errResp != nil {
		return protocol.CallResult{}, errResp
	}

	groups, err := t.listGroups(ctx, base, token)
	if err != nil {
		return protocol.CallResult{}, err
	}

	// Find "Recent Transactions" group
	var txGroup *paymentsGroupWrapper
	for i, g := range groups {
		name := strings.ToLower(g.AnalyticsGroup.Name)
		if strings.Contains(name, "recent transaction") || strings.Contains(name, "recent payments") {
			txGroup = &groups[i]
			break
		}
	}
	if txGroup == nil {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32004, Message: "Recent Transactions analytics group not found"}
	}

	respText := strings.Builder{}
	respText.WriteString(fmt.Sprintf("Recent Transactions (group %d):\n\n", txGroup.AnalyticsGroup.ID))

	// Build payload with currency filter if supported
	payload := buildRecentTxPayload(args.CurrencyCodes, args.Limit, txGroup.AnalyticsGroup.Filters)

	for _, gr := range txGroup.AnalyticsGroup.Graphs {
		data, err := t.graphData(ctx, base, token, txGroup.AnalyticsGroup.ID, gr.ID, payload)
		if err != nil {
			respText.WriteString(fmt.Sprintf("- %s: error fetching data\n", gr.Name))
			continue
		}
		respText.WriteString(fmt.Sprintf("- %s:\n%s\n\n", gr.Name, data))
	}

	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: strings.TrimSpace(respText.String())}}}, nil
}

func buildRecentTxPayload(currencyCodes []string, limit int, filters []paymentsAnalyticsFilter) map[string]any {
	payload := map[string]any{}

	if limit > 0 {
		payload["limit"] = limit
	}

	if len(currencyCodes) == 0 {
		return payload
	}

	// Check for currency filter support
	for _, f := range filters {
		t := strings.ToLower(f.Type)
		if t == "in_query_currency_filter" {
			payload["in_query_currency_filter"] = currencyCodes
			break
		}
	}
	return payload
}

func (t *payramRecentTransactionsTool) listGroups(ctx context.Context, base, token string) ([]paymentsGroupWrapper, *protocol.ResponseError) {
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

func (t *payramRecentTransactionsTool) graphData(ctx context.Context, base, token string, groupID, graphID int, payload map[string]any) (string, *protocol.ResponseError) {
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
