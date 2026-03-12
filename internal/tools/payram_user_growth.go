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

// payramUserGrowthTool analyzes paying user growth and retention.
type payramUserGrowthTool struct {
	client *http.Client
}

// PayramUserGrowth constructs the tool.
func PayramUserGrowth() *payramUserGrowthTool {
	return &payramUserGrowthTool{client: &http.Client{Timeout: 15 * time.Second}}
}

func (t *payramUserGrowthTool) Descriptor() protocol.ToolDescriptor {
	return protocol.ToolDescriptor{
		Name: "payram_user_growth",
		Description: `Analyze paying user growth and retention patterns.

Use cases:
- Track new vs recurring user ratio
- Analyze user acquisition trends
- Measure customer retention over time
- Compare user growth across periods

Returns:
- New paying users count over time
- Recurring paying users count
- Total paying user breakdown
- Growth trends`,
		InputSchema: &protocol.JSONSchema{
			Type: "object",
			Properties: map[string]protocol.JSONSchema{
				"token":    {Type: "string", Description: "Bearer token override; defaults to PAYRAM_ANALYTICS_TOKEN env"},
				"base_url": {Type: "string", Description: "API base override; required if PAYRAM_ANALYTICS_BASE_URL env is not set"},
				"days":     {Type: "integer", Description: "Fetch last N days"},
				"date_filter": {
					Type:        "string",
					Description: "Date filter: today, yesterday, last_7_days, last_30_days, this_month, last_month, last_6_months, forever. Default: last_30_days",
				},
				"currency_codes": {
					Type:        "array",
					Description: "Filter by currencies: BTC, ETH, TRX, BASE, USDT, USDC, CBBTC",
					Items:       &protocol.JSONSchema{Type: "string"},
				},
			},
			Required: []string{},
		},
	}
}

type userGrowthArgs struct {
	Token         string   `json:"token"`
	BaseURL       string   `json:"base_url"`
	Days          int      `json:"days"`
	DateFilter    string   `json:"date_filter"`
	CurrencyCodes []string `json:"currency_codes"`
}

func (t *payramUserGrowthTool) Invoke(ctx context.Context, raw json.RawMessage) (protocol.CallResult, *protocol.ResponseError) {
	var args userGrowthArgs
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

	groups, err := t.listGroups(ctx, base, token)
	if err != nil {
		return protocol.CallResult{}, err
	}

	// Find "Paying User Summary" group
	var userGroup *paymentsGroupWrapper
	for i, g := range groups {
		name := strings.ToLower(g.AnalyticsGroup.Name)
		if strings.Contains(name, "paying user") {
			userGroup = &groups[i]
			break
		}
	}
	if userGroup == nil {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32004, Message: "Paying User Summary group not found"}
	}

	respText := strings.Builder{}
	respText.WriteString(fmt.Sprintf("# User Growth Analysis (%s)\n\n", dateFilter))

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
		payload["in_query_currency_filter"] = args.CurrencyCodes
	}

	for _, gr := range userGroup.AnalyticsGroup.Graphs {
		data, graphErr := t.graphData(ctx, base, token, userGroup.AnalyticsGroup.ID, gr.ID, payload)
		if graphErr != nil {
			respText.WriteString(fmt.Sprintf("- %s: error (%s)\n", gr.Name, graphErr.Message))
			continue
		}
		respText.WriteString(fmt.Sprintf("## %s\n", gr.Name))
		if gr.Description != "" {
			respText.WriteString(fmt.Sprintf("*%s*\n\n", gr.Description))
		}
		respText.WriteString(data)
		respText.WriteString("\n\n")
	}

	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: strings.TrimSpace(respText.String())}}}, nil
}

func (t *payramUserGrowthTool) listGroups(ctx context.Context, base, token string) ([]paymentsGroupWrapper, *protocol.ResponseError) {
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

func (t *payramUserGrowthTool) graphData(ctx context.Context, base, token string, groupID, graphID int, payload map[string]any) (string, *protocol.ResponseError) {
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
