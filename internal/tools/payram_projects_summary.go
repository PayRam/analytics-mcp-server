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

// payramProjectsSummaryTool fetches project-level analytics: payments and transactions by project.
// This group may not be available in all environments (e.g., testnet).
type payramProjectsSummaryTool struct {
	client *http.Client
}

// PayramProjectsSummary constructs the tool.
func PayramProjectsSummary() *payramProjectsSummaryTool {
	return &payramProjectsSummaryTool{client: &http.Client{Timeout: 15 * time.Second}}
}

func (t *payramProjectsSummaryTool) Descriptor() protocol.ToolDescriptor {
	return protocol.ToolDescriptor{
		Name:        "payram_projects_summary",
		Description: "Fetch project-level analytics: payments in USD and number of transactions broken down by project. Note: May not be available in all environments.",
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
			},
			Required: []string{},
		},
	}
}

type projectsArgs struct {
	Token          string `json:"token"`
	BaseURL        string `json:"base_url"`
	Days           int    `json:"days"`
	DateFilter     string `json:"date_filter"`
	CustomStartISO string `json:"custom_start_date"`
	CustomEndISO   string `json:"custom_end_date"`
}

func (t *payramProjectsSummaryTool) Invoke(ctx context.Context, raw json.RawMessage) (protocol.CallResult, *protocol.ResponseError) {
	var args projectsArgs
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

	// Find "Projects Summary" group
	var projGroup *paymentsGroupWrapper
	for i, g := range groups {
		name := strings.ToLower(g.AnalyticsGroup.Name)
		if strings.Contains(name, "project") {
			projGroup = &groups[i]
			break
		}
	}
	if projGroup == nil {
		return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: "Projects Summary analytics group not found. This group may not be available in the current environment."}}}, nil
	}

	respText := strings.Builder{}
	respText.WriteString(fmt.Sprintf("Projects Summary (group %d):\n\n", projGroup.AnalyticsGroup.ID))

	// Build payload
	payload := map[string]any{}
	if dateFilter == "custom" {
		payload["custom"] = map[string]any{
			"start_date": strings.TrimSpace(customStart),
			"end_date":   strings.TrimSpace(customEnd),
		}
	} else {
		payload["analytics_date_filter"] = dateFilter
	}

	for _, gr := range projGroup.AnalyticsGroup.Graphs {
		data, err := t.graphData(ctx, base, token, projGroup.AnalyticsGroup.ID, gr.ID, payload)
		if err != nil {
			respText.WriteString(fmt.Sprintf("- %s: error fetching data\n", gr.Name))
			continue
		}
		respText.WriteString(fmt.Sprintf("- %s:\n%s\n\n", gr.Name, data))
	}

	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: strings.TrimSpace(respText.String())}}}, nil
}

func (t *payramProjectsSummaryTool) listGroups(ctx context.Context, base, token string) ([]paymentsGroupWrapper, *protocol.ResponseError) {
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

func (t *payramProjectsSummaryTool) graphData(ctx context.Context, base, token string, groupID, graphID int, payload map[string]any) (string, *protocol.ResponseError) {
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
