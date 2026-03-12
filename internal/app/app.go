package app

import (
	"os"
	"strings"

	"github.com/payram/payram-analytics-mcp-server/internal/mcp"
	"github.com/payram/payram-analytics-mcp-server/internal/payramauth"
	"github.com/payram/payram-analytics-mcp-server/internal/tools"
)

// InitMCPAuth configures auto-login token management when credentials are available.
// Reads USER_EMAIL / USER_PASSWORD (and PAYRAM_DASHBOARD_USERNAME / PAYRAM_DASHBOARD_PASSWORD
// as aliases) together with PAYRAM_ANALYTICS_BASE_URL.
// If all three are set, tools will automatically obtain and refresh bearer tokens.
func InitMCPAuth() {
	base := strings.TrimSpace(os.Getenv("PAYRAM_ANALYTICS_BASE_URL"))
	email := firstNonEmpty(
		os.Getenv("USER_EMAIL"),
		os.Getenv("PAYRAM_DASHBOARD_USERNAME"),
	)
	password := firstNonEmpty(
		os.Getenv("USER_PASSWORD"),
		os.Getenv("PAYRAM_DASHBOARD_PASSWORD"),
	)
	if base != "" && email != "" && password != "" {
		tm := payramauth.NewTokenManager(base, email, password)
		tools.SetTokenProvider(tm)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

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
		tools.PayramPaymentSearch(),
		tools.PayramPaymentLink(),
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
		tools.PayramProjectsList(),
		tools.PayramPaymentSummary(),
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
