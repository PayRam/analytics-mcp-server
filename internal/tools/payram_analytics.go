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

// payramAnalyticsTool queries PayRam analytics APIs.
type payramAnalyticsTool struct {
	client *http.Client
}

// PayramAnalytics constructs the analytics tool.
func PayramAnalytics() *payramAnalyticsTool {
	return &payramAnalyticsTool{
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (t *payramAnalyticsTool) Descriptor() protocol.ToolDescriptor {
	return protocol.ToolDescriptor{
		Name:        "payram_analytics",
		Description: "Query PayRam analytics groups or graph data. Actions: list_groups, graph_data.",
		InputSchema: &protocol.JSONSchema{
			Type: "object",
			Properties: map[string]protocol.JSONSchema{
				"token": {
					Type:        "string",
					Description: "Bearer token override; defaults to PAYRAM_ANALYTICS_TOKEN env",
				},
				"base_url": {
					Type:        "string",
					Description: "API base override; required if PAYRAM_ANALYTICS_BASE_URL env is not set",
				},
				"action": {
					Type:        "string",
					Enum:        []string{"list_groups", "graph_data"},
					Description: "Action to perform",
				},
				"group_id": {
					Type:        "integer",
					Description: "Group ID (required for graph_data)",
				},
				"graph_id": {
					Type:        "integer",
					Description: "Graph ID (required for graph_data)",
				},
				"payload": {
					Type:                 "object",
					Description:          "Optional POST body; defaults to { analytics_date_filter: 'last_30_days' }",
					AdditionalProperties: true,
				},
			},
			Required: []string{"action"},
		},
	}
}

// analyticsArgs captures supported parameters.
type analyticsArgs struct {
	Action  string                     `json:"action"`
	GroupID int                        `json:"group_id,omitempty"`
	GraphID int                        `json:"graph_id,omitempty"`
	Token   string                     `json:"token,omitempty"`
	BaseURL string                     `json:"base_url,omitempty"`
	Payload map[string]json.RawMessage `json:"payload,omitempty"`
}

func (t *payramAnalyticsTool) Invoke(ctx context.Context, raw json.RawMessage) (protocol.CallResult, *protocol.ResponseError) {
	var args analyticsArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "invalid arguments"}
		}
	}

	token, base, errResp := resolveAnalyticsAuth(ctx, args.Token, args.BaseURL)
	if errResp != nil {
		return protocol.CallResult{}, errResp
	}
	switch args.Action {
	case "list_groups":
		return t.listGroups(ctx, base, token)
	case "graph_data":
		if args.GroupID == 0 || args.GraphID == 0 {
			return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "group_id and graph_id are required for graph_data"}
		}
		return t.graphData(ctx, base, token, args.GroupID, args.GraphID, args.Payload)
	default:
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "action must be list_groups or graph_data"}
	}
}

func (t *payramAnalyticsTool) listGroups(ctx context.Context, base, token string) (protocol.CallResult, *protocol.ResponseError) {
	url := base + "/api/v1/external-platform/all/analytics/groups"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32603, Message: fmt.Sprintf("build request: %v", err)}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := t.client.Do(req)
	if err != nil {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32603, Message: fmt.Sprintf("http error: %v", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return protocol.CallResult{}, &protocol.ResponseError{Code: resp.StatusCode, Message: fmt.Sprintf("unexpected status: %d", resp.StatusCode)}
	}

	var data []groupEntry
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32603, Message: fmt.Sprintf("decode response: %v", err)}
	}

	summary := summarizeGroups(data)
	pretty, _ := json.MarshalIndent(data, "", "  ")
	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: fmt.Sprintf("Groups (summary):\n%s\n\nRaw:\n%s", summary, string(pretty))}}}, nil
}

func (t *payramAnalyticsTool) graphData(ctx context.Context, base, token string, groupID, graphID int, payload map[string]json.RawMessage) (protocol.CallResult, *protocol.ResponseError) {
	if payload == nil {
		payload = map[string]json.RawMessage{"analytics_date_filter": json.RawMessage(`"last_30_days"`)}
	}
	body, _ := json.Marshal(payload)

	url := fmt.Sprintf("%s/api/v1/external-platform/all/analytics/groups/%d/graph/%d/data", base, groupID, graphID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32603, Message: fmt.Sprintf("build request: %v", err)}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := t.client.Do(req)
	if err != nil {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32603, Message: fmt.Sprintf("http error: %v", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return protocol.CallResult{}, &protocol.ResponseError{Code: resp.StatusCode, Message: fmt.Sprintf("unexpected status: %d", resp.StatusCode)}
	}

	var data json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32603, Message: fmt.Sprintf("decode response: %v", err)}
	}
	pretty, _ := json.MarshalIndent(data, "", "  ")
	header := fmt.Sprintf("Graph data for group %d graph %d:", groupID, graphID)
	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: fmt.Sprintf("%s\n%s", header, string(pretty))}}}, nil
}

type groupEntry struct {
	ID             int            `json:"id"`
	Name           string         `json:"name"`
	AnalyticsGroup analyticsGroup `json:"analyticsGroup"`
}

type analyticsGroup struct {
	Name string `json:"name"`
}

func summarizeGroups(data []groupEntry) string {
	if len(data) == 0 {
		return "(no groups)"
	}
	var b strings.Builder
	limit := len(data)
	if limit > 10 {
		limit = 10
	}
	for i := 0; i < limit; i++ {
		g := data[i]
		name := g.Name
		if name == "" {
			name = g.AnalyticsGroup.Name
		}
		fmt.Fprintf(&b, "- id: %d name: %s\n", g.ID, name)
	}
	if len(data) > limit {
		fmt.Fprintf(&b, "(+%d more)", len(data)-limit)
	}
	return strings.TrimSpace(b.String())
}
