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

// payramPayingUsersTool fetches paying user analytics: new vs recurring users breakdown.
type payramPayingUsersTool struct {
	client *http.Client
}

// PayramPayingUsers constructs the tool.
func PayramPayingUsers() *payramPayingUsersTool {
	return &payramPayingUsersTool{client: &http.Client{Timeout: 15 * time.Second}}
}

func (t *payramPayingUsersTool) Descriptor() protocol.ToolDescriptor {
	return protocol.ToolDescriptor{
		Name:        "payram_paying_users",
		Description: "Fetch paying user analytics: new vs recurring users breakdown over time, and total paying user counts.",
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

type payingUsersArgs struct {
	Token          string   `json:"token"`
	BaseURL        string   `json:"base_url"`
	Days           int      `json:"days"`
	DateFilter     string   `json:"date_filter"`
	CustomStartISO string   `json:"custom_start_date"`
	CustomEndISO   string   `json:"custom_end_date"`
	CurrencyCodes  []string `json:"currency_codes"`
}

func (t *payramPayingUsersTool) Invoke(ctx context.Context, raw json.RawMessage) (protocol.CallResult, *protocol.ResponseError) {
	var args payingUsersArgs
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
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32004, Message: "Paying User Summary analytics group not found"}
	}

	respText := strings.Builder{}
	respText.WriteString(fmt.Sprintf("Paying User Summary (group %d):\n\n", userGroup.AnalyticsGroup.ID))

	// Build payload with currency filter if supported
	payload := buildPayingUsersPayload(dateFilter, customStart, customEnd, args.CurrencyCodes, userGroup.AnalyticsGroup.Filters)

	for _, gr := range userGroup.AnalyticsGroup.Graphs {
		data, err := t.graphData(ctx, base, token, userGroup.AnalyticsGroup.ID, gr.ID, payload)
		if err != nil {
			respText.WriteString(fmt.Sprintf("- %s: error fetching data\n", gr.Name))
			continue
		}
		respText.WriteString(fmt.Sprintf("- %s (%s):\n%s\n\n", gr.Name, gr.Description, data))
	}

	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: strings.TrimSpace(respText.String())}}}, nil
}

func buildPayingUsersPayload(dateFilter, customStart, customEnd string, currencyCodes []string, filters []paymentsAnalyticsFilter) map[string]any {
	payload := map[string]any{}
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

func (t *payramPayingUsersTool) listGroups(ctx context.Context, base, token string) ([]paymentsGroupWrapper, *protocol.ResponseError) {
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

func (t *payramPayingUsersTool) graphData(ctx context.Context, base, token string, groupID, graphID int, payload map[string]any) (string, *protocol.ResponseError) {
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
