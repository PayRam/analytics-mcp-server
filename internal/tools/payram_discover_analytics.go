package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/payram/payram-analytics-mcp-server/internal/protocol"
)

// payramDiscoverAnalyticsTool lists all available analytics groups and their graphs.
// Use this tool first to understand what analytics data is available before fetching specific data.
type payramDiscoverAnalyticsTool struct {
	client *http.Client
}

// PayramDiscoverAnalytics constructs the tool.
func PayramDiscoverAnalytics() *payramDiscoverAnalyticsTool {
	return &payramDiscoverAnalyticsTool{client: &http.Client{Timeout: 15 * time.Second}}
}

func (t *payramDiscoverAnalyticsTool) Descriptor() protocol.ToolDescriptor {
	return protocol.ToolDescriptor{
		Name: "payram_discover_analytics",
		Description: `Discover all available PayRam analytics groups and graphs. Use this FIRST to understand what data is available.

Returns a list of analytics groups, each containing:
- Group ID, name, and description
- Available graphs within each group (with graph ID, name, type, and description)
- Available filters for each group (date filters, currency filters, etc.)

Common groups include:
- Numbers: Key metrics like total payments, paying users counts
- Transaction Summary: Bar graphs showing payments in USD and transaction counts per day
- Deposit Distribution: Pie chart of payment distribution by currency/network
- Paying User Summary: New vs recurring users breakdown
- Recent Transactions: Table of recent payment transactions
- Projects Summary: Per-project breakdown (if available)

After discovering available graphs, use 'payram_fetch_graph_data' to get specific data.`,
		InputSchema: &protocol.JSONSchema{
			Type: "object",
			Properties: map[string]protocol.JSONSchema{
				"token": {Type: "string", Description: "Bearer token override; defaults to PAYRAM_ANALYTICS_TOKEN env"},
			},
			Required: []string{},
		},
	}
}

type discoverArgs struct {
	Token   string `json:"token"`
	BaseURL string `json:"base_url"`
}

func (t *payramDiscoverAnalyticsTool) Invoke(ctx context.Context, raw json.RawMessage) (protocol.CallResult, *protocol.ResponseError) {
	var args discoverArgs
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

	// Format output as structured discovery info
	respText := strings.Builder{}
	respText.WriteString("# Available PayRam Analytics\n\n")

	for _, g := range groups {
		ag := g.AnalyticsGroup
		respText.WriteString(fmt.Sprintf("## Group: %s (ID: %d)\n", ag.Name, ag.ID))
		if ag.Description != "" {
			respText.WriteString(fmt.Sprintf("Description: %s\n", ag.Description))
		}

		// List filters
		if len(ag.Filters) > 0 {
			respText.WriteString("### Filters:\n")
			for _, f := range ag.Filters {
				respText.WriteString(fmt.Sprintf("- %s (type: %s)\n", f.Name, f.Type))
			}
		}

		// List graphs
		if len(ag.Graphs) > 0 {
			respText.WriteString("### Graphs:\n")
			for _, gr := range ag.Graphs {
				respText.WriteString(fmt.Sprintf("- **%s** (ID: %d, type: %s)\n", gr.Name, gr.ID, gr.GraphType))
				if gr.Description != "" {
					respText.WriteString(fmt.Sprintf("  Description: %s\n", gr.Description))
				}
			}
		}
		respText.WriteString("\n")
	}

	respText.WriteString("---\n")
	respText.WriteString("To fetch data from a specific graph, use `payram_fetch_graph_data` with the group_id and graph_id.\n")

	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: strings.TrimSpace(respText.String())}}}, nil
}

func (t *payramDiscoverAnalyticsTool) listGroups(ctx context.Context, base, token string) ([]discoverGroupWrapper, *protocol.ResponseError) {
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

	var data []discoverGroupWrapper
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, &protocol.ResponseError{Code: -32603, Message: fmt.Sprintf("decode response: %v", err)}
	}
	return data, nil
}

// Types for discovery (with graph type info)
type discoverGroupWrapper struct {
	ID             int                    `json:"id"`
	AnalyticsGroup discoverAnalyticsGroup `json:"analyticsGroup"`
}

type discoverAnalyticsGroup struct {
	ID          int              `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Filters     []discoverFilter `json:"filters"`
	Graphs      []discoverGraph  `json:"graphs"`
}

type discoverFilter struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Value   string `json:"value"`
	Options string `json:"options"`
}

type discoverGraph struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	GraphType   string `json:"graphType"`
}
