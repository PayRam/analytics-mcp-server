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

// payramNumbersSummaryTool fetches key numeric metrics from the "Numbers" analytics group.
// Graphs include: Total payments, Payments in last 30 days, Total paying users, etc.
type payramNumbersSummaryTool struct {
	client *http.Client
}

// PayramNumbersSummary constructs the tool.
func PayramNumbersSummary() *payramNumbersSummaryTool {
	return &payramNumbersSummaryTool{client: &http.Client{Timeout: 15 * time.Second}}
}

func (t *payramNumbersSummaryTool) Descriptor() protocol.ToolDescriptor {
	return protocol.ToolDescriptor{
		Name:        "payram_numbers_summary",
		Description: "Fetch key numeric metrics (all-time/static 'Numbers' group): total payments, payments in last 30 days, total paying users, users in last 30 days, total users requested, users attempted in last 30 days. Note: this group is static and may ignore date ranges; prefer date-aware tools for ranged queries.",
		InputSchema: &protocol.JSONSchema{
			Type: "object",
			Properties: map[string]protocol.JSONSchema{
				"token":    {Type: "string", Description: "Bearer token override; defaults to PAYRAM_ANALYTICS_TOKEN env"},
				"base_url": {Type: "string", Description: "API base override; required if PAYRAM_ANALYTICS_BASE_URL env is not set"},
			},
			Required: []string{},
		},
	}
}

type numbersArgs struct {
	Token   string `json:"token"`
	BaseURL string `json:"base_url"`
}

func (t *payramNumbersSummaryTool) Invoke(ctx context.Context, raw json.RawMessage) (protocol.CallResult, *protocol.ResponseError) {
	var args numbersArgs
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

	// Find "Numbers" group
	var numbersGroup *paymentsGroupWrapper
	for i, g := range groups {
		if strings.EqualFold(g.AnalyticsGroup.Name, "Numbers") {
			numbersGroup = &groups[i]
			break
		}
	}
	if numbersGroup == nil {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32004, Message: "Numbers analytics group not found"}
	}

	respText := strings.Builder{}
	respText.WriteString(fmt.Sprintf("Numbers Summary (group %d):\n\n", numbersGroup.AnalyticsGroup.ID))

	// Fetch data for each graph in this group
	for _, gr := range numbersGroup.AnalyticsGroup.Graphs {
		data, err := t.graphData(ctx, base, token, numbersGroup.AnalyticsGroup.ID, gr.ID, map[string]any{})
		if err != nil {
			respText.WriteString(fmt.Sprintf("- %s: error fetching data\n", gr.Name))
			continue
		}
		respText.WriteString(fmt.Sprintf("- %s:\n%s\n\n", gr.Name, data))
	}

	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: strings.TrimSpace(respText.String())}}}, nil
}

func (t *payramNumbersSummaryTool) listGroups(ctx context.Context, base, token string) ([]paymentsGroupWrapper, *protocol.ResponseError) {
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

func (t *payramNumbersSummaryTool) graphData(ctx context.Context, base, token string, groupID, graphID int, payload map[string]any) (string, *protocol.ResponseError) {
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
