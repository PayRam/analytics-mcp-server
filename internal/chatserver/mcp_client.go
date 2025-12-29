package chatserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/payram/payram-analytics-mcp-server/internal/protocol"
)

// MCPClient issues JSON-RPC calls to the existing MCP server over HTTP.
type MCPClient struct {
	baseURL    string
	httpClient *http.Client
	counter    uint64
	auth       func(ctx context.Context) (string, error)
}

// NewMCPClient builds a client with a sane timeout.
func NewMCPClient(baseURL string) *MCPClient {
	trimmed := baseURL
	if !strings.HasSuffix(trimmed, "/") {
		trimmed += "/"
	}
	return &MCPClient{
		baseURL: trimmed,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *MCPClient) nextID() any {
	return atomic.AddUint64(&c.counter, 1)
}

func (c *MCPClient) do(ctx context.Context, method string, params any) (protocol.Response, error) {
	var resp protocol.Response

	payload := protocol.Request{
		JSONRPC: "2.0",
		ID:      c.nextID(),
		Method:  method,
		Params:  mustRaw(params),
	}

	buf, err := json.Marshal(payload)
	if err != nil {
		return resp, fmt.Errorf("encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(buf))
	if err != nil {
		return resp, fmt.Errorf("build http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.auth != nil {
		tok, err := c.auth(ctx)
		if err != nil {
			return resp, fmt.Errorf("auth token: %w", err)
		}
		tok = strings.TrimSpace(tok)
		if tok != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tok)
		}
	}

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return resp, fmt.Errorf("call mcp server: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return resp, fmt.Errorf("mcp server returned status %d", httpResp.StatusCode)
	}

	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return resp, fmt.Errorf("decode response: %w", err)
	}

	if resp.Error != nil {
		return resp, errors.New(resp.Error.Message)
	}

	return resp, nil
}

// SetAuthProvider allows attaching a bearer token for MCP calls.
func (c *MCPClient) SetAuthProvider(fn func(ctx context.Context) (string, error)) {
	c.auth = fn
}

// ListTools fetches the advertised tools from the MCP server.
func (c *MCPClient) ListTools(ctx context.Context) ([]protocol.ToolDescriptor, error) {
	resp, err := c.do(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("marshal list result: %w", err)
	}
	var result protocol.ListResult
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		return nil, fmt.Errorf("unmarshal list result: %w", err)
	}
	return result.Tools, nil
}

// CallTool invokes a tool and returns the structured result.
func (c *MCPClient) CallTool(ctx context.Context, name string, args map[string]any) (protocol.CallResult, error) {
	resp, err := c.do(ctx, "tools/call", protocol.CallParams{Name: name, Args: mustRaw(args)})
	if err != nil {
		return protocol.CallResult{}, err
	}
	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		return protocol.CallResult{}, fmt.Errorf("marshal call result: %w", err)
	}
	var result protocol.CallResult
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		return protocol.CallResult{}, fmt.Errorf("unmarshal call result: %w", err)
	}
	return result, nil
}

func mustRaw(v any) json.RawMessage {
	if v == nil {
		return json.RawMessage(`null`)
	}
	b, _ := json.Marshal(v)
	return json.RawMessage(b)
}
