package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/payram/payram-analytics-mcp-server/internal/protocol"
)

// payramPaymentLinkTool creates a payment link for a project.
type payramPaymentLinkTool struct {
	client *http.Client
}

// PayramPaymentLink constructs the tool.
func PayramPaymentLink() *payramPaymentLinkTool {
	return &payramPaymentLinkTool{client: &http.Client{Timeout: 15 * time.Second}}
}

func (t *payramPaymentLinkTool) Descriptor() protocol.ToolDescriptor {
	return protocol.ToolDescriptor{
		Name:        "payram_payment_link",
		Description: "Create a payment link for a project. Accepts project name or ID; auto-selects when only one project exists.",
		InputSchema: &protocol.JSONSchema{
			Type: "object",
			Properties: map[string]protocol.JSONSchema{
				"token":          {Type: "string", Description: "Bearer token override; defaults to PAYRAM_ANALYTICS_TOKEN env"},
				"base_url":       {Type: "string", Description: "API base override; required if PAYRAM_ANALYTICS_BASE_URL env is not set"},
				"project_id":     {Type: "integer", Description: "Project ID; if omitted and only one project exists, it will be auto-selected"},
				"project_name":   {Type: "string", Description: "Project name (alternative to project_id); will be matched case-insensitively"},
				"customer_id":    {Type: "string", Description: "Customer ID (required)"},
				"customer_email": {Type: "string", Description: "Customer email (required)"},
				"amount_usd":     {Type: "number", Description: "Payment amount in USD (required, > 0)"},
			},
		},
	}
}

type paymentLinkArgs struct {
	Token         string  `json:"token"`
	BaseURL       string  `json:"base_url"`
	ProjectID     int     `json:"project_id"`
	ProjectName   string  `json:"project_name"`
	CustomerID    string  `json:"customer_id"`
	CustomerEmail string  `json:"customer_email"`
	AmountUSD     float64 `json:"amount_usd"`
}

type paymentLinkResponse struct {
	PaymentID string `json:"paymentID"`
	Link      string `json:"paymentLink"`
}

func (t *payramPaymentLinkTool) Invoke(ctx context.Context, raw json.RawMessage) (protocol.CallResult, *protocol.ResponseError) {
	var args paymentLinkArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "invalid arguments"}
		}
	}

	token, base, errResp := resolveAnalyticsAuth(ctx, args.Token, args.BaseURL)
	if errResp != nil {
		return protocol.CallResult{}, errResp
	}

	projectID, promptResult, projErr := t.resolveProjectID(ctx, base, token, args.ProjectID, args.ProjectName)
	if projErr != nil {
		return protocol.CallResult{}, projErr
	}
	if promptResult != nil {
		return *promptResult, nil
	}

	if strings.TrimSpace(args.CustomerID) == "" {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "customer_id is required"}
	}
	if strings.TrimSpace(args.CustomerEmail) == "" {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "customer_email is required"}
	}
	if args.AmountUSD <= 0 {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "amount_usd must be > 0"}
	}

	payload := map[string]any{
		"customerID":    strings.TrimSpace(args.CustomerID),
		"customerEmail": strings.TrimSpace(args.CustomerEmail),
		"amountInUSD":   args.AmountUSD,
	}

	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s/api/v1/external-platform/%d/payment", base, projectID)
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
		bodyBytes, _ := io.ReadAll(resp.Body)
		return protocol.CallResult{}, &protocol.ResponseError{Code: resp.StatusCode, Message: fmt.Sprintf("unexpected status: %d, body: %s", resp.StatusCode, string(bodyBytes))}
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32603, Message: fmt.Sprintf("read response: %v", err)}
	}

	// Try to decode the response
	var linkResp paymentLinkResponse
	if err := json.Unmarshal(bodyBytes, &linkResp); err != nil {
		// Return raw response for debugging
		return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: fmt.Sprintf("Payment link API response (parse error):\n%s", string(bodyBytes))}}}, nil
	}

	// Check if we got actual values
	if strings.TrimSpace(linkResp.Link) == "" {
		return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: fmt.Sprintf("Payment link created but no link returned. Raw response:\n%s", string(bodyBytes))}}}, nil
	}

	text := fmt.Sprintf("Payment link created for project %d:\n- customer_id: %s\n- customer_email: %s\n- amount_usd: %.2f\n- payment_id: %s\n- link: %s",
		projectID,
		strings.TrimSpace(args.CustomerID),
		strings.TrimSpace(args.CustomerEmail),
		args.AmountUSD,
		strings.TrimSpace(linkResp.PaymentID),
		strings.TrimSpace(linkResp.Link),
	)

	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: strings.TrimSpace(text)}}}, nil
}

func (t *payramPaymentLinkTool) resolveProjectID(ctx context.Context, base, token string, requested int, name string) (int, *protocol.CallResult, *protocol.ResponseError) {
	if requested > 0 {
		return requested, nil, nil
	}

	projects, err := t.listProjects(ctx, base, token)
	if err != nil {
		return 0, nil, err
	}
	if len(projects) == 0 {
		return 0, nil, &protocol.ResponseError{Code: -32004, Message: "No projects found; provide project_id or project_name"}
	}

	// If project_name provided, find matching project (case-insensitive)
	if strings.TrimSpace(name) != "" {
		normName := strings.ToLower(strings.TrimSpace(name))
		for _, p := range projects {
			if strings.ToLower(strings.TrimSpace(p.Name)) == normName {
				return p.ID, nil, nil
			}
		}
		return 0, nil, &protocol.ResponseError{Code: -32004, Message: fmt.Sprintf("No project found with name: %s", name)}
	}

	if len(projects) == 1 {
		return projects[0].ID, nil, nil
	}

	// Multiple projects: return a prompt listing names/ids for selection and remind required fields.
	var b strings.Builder
	limit := len(projects)
	if limit > 20 {
		limit = 20
	}
	fmt.Fprintf(&b, "Multiple projects found; please choose a project_id and provide amount_usd, customer_email, and customer_id. Showing %d of %d projects:\n", limit, len(projects))
	for i := 0; i < limit; i++ {
		p := projects[i]
		fmt.Fprintf(&b, "- id: %d | name: %s\n", p.ID, strings.TrimSpace(p.Name))
	}
	if len(projects) > limit {
		fmt.Fprintf(&b, "...and %d more", len(projects)-limit)
	}
	return 0, &protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: strings.TrimSpace(b.String())}}}, nil
}

func (t *payramPaymentLinkTool) listProjects(ctx context.Context, base, token string) ([]externalProject, *protocol.ResponseError) {
	url := base + "/api/v1/external-platform/all"
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

	var projects []externalProject
	if err := json.NewDecoder(resp.Body).Decode(&projects); err != nil {
		return nil, &protocol.ResponseError{Code: -32603, Message: fmt.Sprintf("decode projects: %v", err)}
	}

	return projects, nil
}
