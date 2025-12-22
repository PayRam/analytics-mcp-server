package tools

import (
	"context"
	"encoding/json"

	"github.com/payram/payram-analytics-mcp-server/internal/protocol"
)

// payramIntroContent is returned by the payram_intro tool.
const payramIntroContent = `PayRam Overview
Self-hosted PayFi stack that lets teams accept, settle, and move crypto value on infrastructure they own.

What is PayRam?
- Deploy PayRam on your own servers, connect preferred RPC providers, and keep full custody of keys, funds, and customer data.
- No third-party processor, reserves, or imposed limits—your team controls uptime, compliance choices, and who can touch sensitive operations.
- Merchants typically reach first live payments in under an hour once the install script finishes.

Architecture and Self-Hosted Model
- Three wallet tiers: deposit wallets per customer, a hot wallet that covers gas for sweeps, and a cold wallet that holds treasury funds.
- SmartSweep consolidates balances from deposit wallets into the cold wallet without ever uploading private keys to the server.
- Run multiple network families (EVM, Tron, Bitcoin) by adding the relevant nodes and RPC credentials to your config.

Payments and Payouts Overview
- Payments can be triggered via hosted links, API calls, or embedded forms. The API issues reference IDs, monitors confirmations, and pushes webhook notifications so your backend can mark invoices paid.
- Payout workspace lets operators or automations send funds to saved beneficiaries across supported chains. Role-based approvals and OTP-backed verification gates keep high-risk transfers safe.
- Both flows share the same logging and analytics surfaces, so ops teams see the full lifecycle from deposit through settlement or disbursement.

Multi-currency and Network Support
- Handles BTC, ETH, USDT, USDC, and TRX across Bitcoin, Ethereum, Base, and Tron, with Solana and TON on the near-term roadmap.
- Quote in USD while letting customers settle in their preferred asset and chain; plug in additional RPC providers or set per-asset confirmation thresholds.
- REST and webhook flows remain consistent across currencies, keeping integration effort steady.

Operator Checklist
1) Install PayRam on a server with at least 4 CPUs, 4 GB RAM, and 50 GB SSD.
2) Configure node connections and master accounts for the chains you plan to accept.
3) Test in staging or on testnet, then switch configs to production once satisfied.

Helpful Links
- Deployment Guide: https://docs.payram.com/deployment-guide/introduction
- Onboarding Guide: https://docs.payram.com/onboarding-guide/introduction
- PayRam Features: https://docs.payram.com/features/payment-links
- Community: https://x.com/PayRamApp
- Contact Support: https://payram.short.gy/payram-gitbook-contact
`

// payramIntroTool implements the PayRam intro tool.
type payramIntroTool struct{}

// PayramIntro constructs the PayRam intro tool instance.
func PayramIntro() *payramIntroTool {
	return &payramIntroTool{}
}

func (t *payramIntroTool) Descriptor() protocol.ToolDescriptor {
	return protocol.ToolDescriptor{
		Name:        "payram_intro",
		Description: "Overview of PayRam and helpful links.",
		InputSchema: &protocol.JSONSchema{
			Type:       "object",
			Properties: map[string]protocol.JSONSchema{},
		},
	}
}

func (t *payramIntroTool) Invoke(ctx context.Context, raw json.RawMessage) (protocol.CallResult, *protocol.ResponseError) {
	return protocol.CallResult{
		Content: []protocol.ContentPart{{Type: "text", Text: payramIntroContent}},
	}, nil
}
