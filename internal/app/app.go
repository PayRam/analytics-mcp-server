package app

import (
	"github.com/payram/payram-analytics-mcp-server/internal/mcp"
	"github.com/payram/payram-analytics-mcp-server/internal/tools"
)

// NewToolbox builds the shared PayRam MCP toolbox.
func NewToolbox() *mcp.Toolbox {
	return mcp.NewToolbox(
		// Core info tools
		tools.PayramIntro(),
		tools.PayramDocs(),
		tools.PayramDashboard(),

		// Generic discovery and fetch tools
		tools.PayramDiscoverAnalytics(),
		tools.PayramFetchGraphData(),

		// Summary and metrics tools
		tools.PayramPaymentsSummary(),
		tools.PayramNumbersSummary(),
		tools.PayramTransactionCounts(),
		tools.PayramDailyStats(),

		// Distribution and breakdown tools
		tools.PayramDepositDistribution(),
		tools.PayramCurrencyBreakdown(),
		tools.PayramPayingUsers(),
		tools.PayramUserGrowth(),

		// Transaction tools
		tools.PayramRecentTransactions(),

		// Project-level analytics
		tools.PayramProjectsSummary(),

		// Comparison and analysis tools
		tools.PayramComparePeriods(),
	)
}

// NewMCPServer constructs an MCP server with the shared toolbox.
func NewMCPServer() *mcp.Server {
	return mcp.NewServer(NewToolbox())
}

// RunMCPHTTP starts the MCP HTTP server on the provided address.
func RunMCPHTTP(addr string) error {
	return mcp.RunHTTP(NewMCPServer(), addr)
}
