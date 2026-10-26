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

// payramPaymentSummaryTool calls the payment summary endpoint for all projects or a specific project.
type payramPaymentSummaryTool struct {
	client *http.Client
}

// PayramPaymentSummary constructs the tool.
func PayramPaymentSummary() *payramPaymentSummaryTool {
	return &payramPaymentSummaryTool{client: &http.Client{Timeout: 15 * time.Second}}
}

func (t *payramPaymentSummaryTool) Descriptor() protocol.ToolDescriptor {
	return protocol.ToolDescriptor{
		Name:        "payram_payment_summary",
		Description: "Fetch payment summary counts (total, closed, open, cancelled) for all projects or a specific project.",
		InputSchema: &protocol.JSONSchema{
			Type: "object",
			Properties: map[string]protocol.JSONSchema{
				"token":      {Type: "string", Description: "Bearer token override; defaults to PAYRAM_ANALYTICS_TOKEN env"},
				"project_id": {Type: "integer", Description: "Optional project ID; when omitted or 0, fetches the summary across all projects"},
			},
		},
	}
}

type paymentSummaryArgs struct {
	Token     string `json:"token"`
	BaseURL   string `json:"base_url"`
	ProjectID int    `json:"project_id"`
}

type paymentSummaryResponse struct {
	TotalCount     int64 `json:"totalCount"`
	ClosedCount    int64 `json:"closedCount"`
	OpenCount      int64 `json:"openCount"`
	CancelledCount int64 `json:"cancelledCount"`
}

func (t *payramPaymentSummaryTool) Invoke(ctx context.Context, raw json.RawMessage) (protocol.CallResult, *protocol.ResponseError) {
	var args paymentSummaryArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "invalid arguments"}
		}
	}

	token, base, errResp := resolveAnalyticsAuth(ctx, args.Token, args.BaseURL)
	if errResp != nil {
		return protocol.CallResult{}, errResp
	}

	target := "all"
	if args.ProjectID > 0 {
		target = fmt.Sprintf("%d", args.ProjectID)
	}

	url := fmt.Sprintf("%s/api/v1/external-platform/%s/payment/summary", base, target)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(`{}`)))
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

	var payload paymentSummaryResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32603, Message: fmt.Sprintf("decode response: %v", err)}
	}

	scope := "all projects"
	if args.ProjectID > 0 {
		scope = fmt.Sprintf("project %d", args.ProjectID)
	}

	text := fmt.Sprintf("Payment summary for %s:\n- total: %d\n- closed: %d\n- open: %d\n- cancelled: %d", scope, payload.TotalCount, payload.ClosedCount, payload.OpenCount, payload.CancelledCount)
	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: text}}}, nil
}

// payramProjectsListTool fetches projects (id + name) to help pick a project_id for other tools.
type payramProjectsListTool struct {
	client *http.Client
}

// PayramProjectsList constructs the tool.
func PayramProjectsList() *payramProjectsListTool {
	return &payramProjectsListTool{client: &http.Client{Timeout: 15 * time.Second}}
}

func (t *payramProjectsListTool) Descriptor() protocol.ToolDescriptor {
	return protocol.ToolDescriptor{
		Name:        "payram_projects_list",
		Description: "List external platform projects with their IDs and names.",
		InputSchema: &protocol.JSONSchema{
			Type: "object",
			Properties: map[string]protocol.JSONSchema{
				"token": {Type: "string", Description: "Bearer token override; defaults to PAYRAM_ANALYTICS_TOKEN env"},
			},
		},
	}
}

type externalProject struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (t *payramProjectsListTool) Invoke(ctx context.Context, raw json.RawMessage) (protocol.CallResult, *protocol.ResponseError) {
	var args struct {
		Token   string `json:"token"`
		BaseURL string `json:"base_url"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "invalid arguments"}
		}
	}

	token, base, errResp := resolveAnalyticsAuth(ctx, args.Token, args.BaseURL)
	if errResp != nil {
		return protocol.CallResult{}, errResp
	}

	url := base + "/api/v1/external-platform/all"
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

	var projects []externalProject
	if err := json.NewDecoder(resp.Body).Decode(&projects); err != nil {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32603, Message: fmt.Sprintf("decode response: %v", err)}
	}

	if len(projects) == 0 {
		return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: "No projects found."}}}, nil
	}

	limit := len(projects)
	if limit > 50 {
		limit = 50
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Projects (showing %d of %d):\n", limit, len(projects))
	for i := 0; i < limit; i++ {
		p := projects[i]
		fmt.Fprintf(&b, "- id: %d name: %s\n", p.ID, strings.TrimSpace(p.Name))
	}
	if len(projects) > limit {
		fmt.Fprintf(&b, "...and %d more", len(projects)-limit)
	}

	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: strings.TrimSpace(b.String())}}}, nil
}

// payramPaymentSearchTool queries payments with filters and optional project scoping.
type payramPaymentSearchTool struct {
	client *http.Client
}

// PayramPaymentSearch constructs the tool.
func PayramPaymentSearch() *payramPaymentSearchTool {
	return &payramPaymentSearchTool{client: &http.Client{Timeout: 20 * time.Second}}
}

func (t *payramPaymentSearchTool) Descriptor() protocol.ToolDescriptor {
	return protocol.ToolDescriptor{
		Name:        "payram_payment_search",
		Description: "Search payments with filters (query, status, currency, network, date range) across all projects or a specific project.",
		InputSchema: &protocol.JSONSchema{
			Type: "object",
			Properties: map[string]protocol.JSONSchema{
				"token":          {Type: "string", Description: "Bearer token override; defaults to PAYRAM_ANALYTICS_TOKEN env"},
				"project_id":     {Type: "integer", Description: "Optional project ID; when omitted or 0, searches across all projects"},
				"query":          {Type: "string", Description: "Free-text query (e.g., reference ID, invoice ID)"},
				"sort_by":        {Type: "string", Description: "Sort field (default: createdAt)"},
				"sort_direction": {Type: "string", Description: "ASC or DESC (default: DESC)"},
				"limit":          {Type: "integer", Description: "Max results to return (default 100)"},
				"offset":         {Type: "integer", Description: "Offset for pagination"},
				"payment_status": {Type: "array", Items: &protocol.JSONSchema{Type: "string"}, Description: "Filter by payment status"},
				"webhook_status": {Type: "array", Items: &protocol.JSONSchema{Type: "string"}, Description: "Filter by webhook status"},
				"currency":       {Type: "array", Items: &protocol.JSONSchema{Type: "string"}, Description: "Filter by currency codes"},
				"network":        {Type: "array", Items: &protocol.JSONSchema{Type: "string"}, Description: "Filter by network"},
				"created_by":     {Type: "array", Items: &protocol.JSONSchema{Type: "string"}, Description: "Filter by creator"},
				"date_from":      {Type: "string", Description: "ISO timestamp start (inclusive)"},
				"date_to":        {Type: "string", Description: "ISO timestamp end (inclusive)"},
			},
		},
	}
}

type paymentSearchArgs struct {
	Token         string   `json:"token"`
	BaseURL       string   `json:"base_url"`
	ProjectID     int      `json:"project_id"`
	Query         string   `json:"query"`
	SortBy        string   `json:"sort_by"`
	SortDirection string   `json:"sort_direction"`
	Limit         int      `json:"limit"`
	Offset        int      `json:"offset"`
	PaymentStatus []string `json:"payment_status"`
	WebhookStatus []string `json:"webhook_status"`
	Currency      []string `json:"currency"`
	Network       []string `json:"network"`
	CreatedBy     []string `json:"created_by"`
	DateFrom      string   `json:"date_from"`
	DateTo        string   `json:"date_to"`
}

type paymentSearchResponse struct {
	Data       []paymentSearchItem `json:"data"`
	TotalCount int64               `json:"totalCount"`
}

type paymentSearchItem struct {
	ProjectName   string  `json:"projectName"`
	ReferenceID   string  `json:"referenceId"`
	PaymentStatus string  `json:"paymentStatus"`
	WebhookStatus string  `json:"webhookStatus"`
	Currency      string  `json:"currency"`
	Network       string  `json:"network"`
	AmountUSD     float64 `json:"amountInUSD"`
	Amount        float64 `json:"amount"`
	CreatedAt     string  `json:"createdAt"`
	InvoiceID     string  `json:"invoiceId"`
	CustomerID    string  `json:"customerId"`
	Email         string  `json:"email"`
	TxHash        string  `json:"txHash"`
}

func (t *payramPaymentSearchTool) Invoke(ctx context.Context, raw json.RawMessage) (protocol.CallResult, *protocol.ResponseError) {
	var args paymentSearchArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "invalid arguments"}
		}
	}

	token, base, errResp := resolveAnalyticsAuth(ctx, args.Token, args.BaseURL)
	if errResp != nil {
		return protocol.CallResult{}, errResp
	}

	sortBy := strings.TrimSpace(args.SortBy)
	if sortBy == "" {
		sortBy = "createdAt"
	}
	sortDir := strings.ToUpper(strings.TrimSpace(args.SortDirection))
	if sortDir == "" {
		sortDir = "DESC"
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 100
	}
	offset := args.Offset
	if offset < 0 {
		offset = 0
	}

	payload := map[string]any{
		"query":         strings.TrimSpace(args.Query),
		"sortBy":        sortBy,
		"sortDirection": sortDir,
		"limit":         limit,
		"offset":        offset,
	}

	if len(args.PaymentStatus) > 0 {
		payload["paymentStatus"] = args.PaymentStatus
	}
	if len(args.WebhookStatus) > 0 {
		payload["webhookStatus"] = args.WebhookStatus
	}
	if len(args.Currency) > 0 {
		payload["currency"] = args.Currency
	}
	if len(args.Network) > 0 {
		payload["network"] = args.Network
	}
	if len(args.CreatedBy) > 0 {
		payload["createdBy"] = args.CreatedBy
	}

	if v := strings.TrimSpace(args.DateFrom); v != "" {
		payload["dateFrom"] = v
	}
	if v := strings.TrimSpace(args.DateTo); v != "" {
		payload["dateTo"] = v
	}

	target := "all"
	if args.ProjectID > 0 {
		target = fmt.Sprintf("%d", args.ProjectID)
	}

	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s/api/v1/external-platform/%s/payment/search", base, target)
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

	var payloadResp paymentSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&payloadResp); err != nil {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32603, Message: fmt.Sprintf("decode response: %v", err)}
	}

	if len(payloadResp.Data) == 0 {
		scope := "all projects"
		if args.ProjectID > 0 {
			scope = fmt.Sprintf("project %d", args.ProjectID)
		}
		return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: fmt.Sprintf("No payments matched the filters for %s (totalCount=%d).", scope, payloadResp.TotalCount)}}}, nil
	}

	limitRender := len(payloadResp.Data)
	if limitRender > 5 {
		limitRender = 5
	}

	scope := "all projects"
	if args.ProjectID > 0 {
		scope = fmt.Sprintf("project %d", args.ProjectID)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Payment search for %s: totalCount=%d\n", scope, payloadResp.TotalCount)
	for i := 0; i < limitRender; i++ {
		item := payloadResp.Data[i]
		fmt.Fprintf(&b, "- ref: %s | status: %s | webhook: %s | %s %0.4f %s | project: %s | created: %s\n", strings.TrimSpace(item.ReferenceID), strings.TrimSpace(item.PaymentStatus), strings.TrimSpace(item.WebhookStatus), strings.TrimSpace(item.Currency), item.Amount, strings.TrimSpace(item.Network), strings.TrimSpace(item.ProjectName), strings.TrimSpace(item.CreatedAt))
	}
	if len(payloadResp.Data) > limitRender {
		fmt.Fprintf(&b, "...and %d more", len(payloadResp.Data)-limitRender)
	}

	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: strings.TrimSpace(b.String())}}}, nil
}

func defaultSlice(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
