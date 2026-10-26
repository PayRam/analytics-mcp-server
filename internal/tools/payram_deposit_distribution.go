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

// payramDepositDistributionTool fetches deposit/payment distribution data (pie chart).
// Shows payment distribution by network or currency.
type payramDepositDistributionTool struct {
	client *http.Client
}

// PayramDepositDistribution constructs the tool.
func PayramDepositDistribution() *payramDepositDistributionTool {
	return &payramDepositDistributionTool{client: &http.Client{Timeout: 15 * time.Second}}
}

func (t *payramDepositDistributionTool) Descriptor() protocol.ToolDescriptor {
	return protocol.ToolDescriptor{
		Name:        "payram_deposit_distribution",
		Description: "Fetch payment distribution breakdown by network or currency. Shows pie chart data of how payments are distributed across different currencies/networks.",
		InputSchema: &protocol.JSONSchema{
			Type: "object",
			Properties: map[string]protocol.JSONSchema{
				"token": {Type: "string", Description: "Bearer token override; defaults to PAYRAM_ANALYTICS_TOKEN env"},
				"days":  {Type: "integer", Description: "If set, fetch last N days using a custom range (overrides date_filter)"},
				"date_filter": {
					Type:        "string",
					Description: "analytics_date_filter (today, yesterday, last_7_days, last_30_days, this_month, last_month, last_6_months, forever, custom). Default last_30_days.",
				},
				"custom_start_date": {Type: "string", Description: "ISO date/time (RFC3339) start when date_filter=custom"},
				"custom_end_date":   {Type: "string", Description: "ISO date/time (RFC3339) end when date_filter=custom"},
				"group_by": {
					Type:        "string",
					Description: "Group by 'currency_code' or 'blockchain_code'. Default currency_code.",
				},
			},
			Required: []string{},
		},
	}
}

type depositDistArgs struct {
	Token          string `json:"token"`
	BaseURL        string `json:"base_url"`
	Days           int    `json:"days"`
	DateFilter     string `json:"date_filter"`
	CustomStartISO string `json:"custom_start_date"`
	CustomEndISO   string `json:"custom_end_date"`
	GroupBy        string `json:"group_by"`
}

func (t *payramDepositDistributionTool) Invoke(ctx context.Context, raw json.RawMessage) (protocol.CallResult, *protocol.ResponseError) {
	var args depositDistArgs
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
	var dateErrResp *protocol.ResponseError
	if args.Days > 0 {
		dateFilter = "custom"
		customStart, customEnd = lastNDaysRange(args.Days)
	} else {
		dateFilter, customStart, customEnd, dateErrResp = normalizeDateFilter(args.DateFilter, args.CustomStartISO, args.CustomEndISO)
	}
	if dateErrResp != nil {
		return protocol.CallResult{}, dateErrResp
	}

	groupBy := strings.TrimSpace(args.GroupBy)
	if groupBy == "" {
		groupBy = "currency_code"
	}

	groups, err := t.listGroups(ctx, base, token)
	if err != nil {
		return protocol.CallResult{}, err
	}

	// Find "Deposit Distribution" group
	var distGroup *paymentsGroupWrapper
	for i, g := range groups {
		name := strings.ToLower(g.AnalyticsGroup.Name)
		if strings.Contains(name, "deposit distribution") || strings.Contains(name, "distribution") {
			distGroup = &groups[i]
			break
		}
	}
	if distGroup == nil {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32004, Message: "Deposit Distribution analytics group not found"}
	}

	respText := strings.Builder{}
	respText.WriteString(fmt.Sprintf("Deposit Distribution (group %d):\n\n", distGroup.AnalyticsGroup.ID))

	// Build payload
	payload := buildDistributionPayload(dateFilter, customStart, customEnd, groupBy)

	for _, gr := range distGroup.AnalyticsGroup.Graphs {
		data, err := t.graphData(ctx, base, token, distGroup.AnalyticsGroup.ID, gr.ID, payload)
		if err != nil {
			respText.WriteString(fmt.Sprintf("- %s: error fetching data\n", gr.Name))
			continue
		}
		respText.WriteString(fmt.Sprintf("- %s:\n%s\n\n", gr.Name, data))
	}

	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: strings.TrimSpace(respText.String())}}}, nil
}

func buildDistributionPayload(dateFilter, customStart, customEnd, groupBy string) map[string]any {
	payload := map[string]any{}
	if dateFilter == "custom" {
		payload["custom"] = map[string]any{
			"start_date": strings.TrimSpace(customStart),
			"end_date":   strings.TrimSpace(customEnd),
		}
	} else {
		payload["analytics_date_filter"] = dateFilter
	}
	if groupBy != "" {
		payload["group_by_only_network_currency_filter"] = map[string]string{
			"code": groupBy,
		}
	}
	return payload
}

func (t *payramDepositDistributionTool) listGroups(ctx context.Context, base, token string) ([]paymentsGroupWrapper, *protocol.ResponseError) {
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

func (t *payramDepositDistributionTool) graphData(ctx context.Context, base, token string, groupID, graphID int, payload map[string]any) (string, *protocol.ResponseError) {
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
