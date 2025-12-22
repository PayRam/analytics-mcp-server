package mcp

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"

	"github.com/payram/payram-analytics-mcp-server/internal/protocol"
)

// Tool defines the behavior of a single MCP tool.
type Tool interface {
	Descriptor() protocol.ToolDescriptor
	Invoke(ctx context.Context, raw json.RawMessage) (protocol.CallResult, *protocol.ResponseError)
}

// Toolbox stores and dispatches tools by name.
type Toolbox struct {
	tools map[string]Tool
}

var toolDebugEnabled = isToolDebugEnabled()

// NewToolbox constructs a toolbox with the provided tools.
func NewToolbox(tools ...Tool) *Toolbox {
	m := make(map[string]Tool, len(tools))
	for _, t := range tools {
		desc := t.Descriptor()
		m[desc.Name] = t
	}
	return &Toolbox{tools: m}
}

// Describe returns all tool descriptors.
func (tb *Toolbox) Describe() []protocol.ToolDescriptor {
	list := make([]protocol.ToolDescriptor, 0, len(tb.tools))
	for _, t := range tb.tools {
		list = append(list, t.Descriptor())
	}
	return list
}

// Call invokes a named tool.
func (tb *Toolbox) Call(ctx context.Context, name string, args json.RawMessage) (protocol.CallResult, *protocol.ResponseError) {
	tool, ok := tb.tools[name]
	if !ok {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32601, Message: "tool not found"}
	}

	if toolDebugEnabled {
		log.Printf("[mcp-debug] tool=%s args=%s", name, truncateArgs(args))
	}
	return tool.Invoke(ctx, args)
}

func truncateArgs(args json.RawMessage) string {
	const max = 2048
	if len(args) == 0 {
		return "{}"
	}
	s := string(args)
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

func isToolDebugEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("PAYRAM_MCP_DEBUG")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}
