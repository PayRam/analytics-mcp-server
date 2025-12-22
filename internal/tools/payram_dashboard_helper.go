package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/payram/payram-analytics-mcp-server/internal/protocol"
)

// PayramDashboardHelper returns guided answers for common dashboard workflows.
type PayramDashboardHelper struct{}

// PayramDashboard creates a dashboard helper tool.
func PayramDashboard() *PayramDashboardHelper {
	return &PayramDashboardHelper{}
}

func (t *PayramDashboardHelper) Descriptor() protocol.ToolDescriptor {
	return protocol.ToolDescriptor{
		Name:        "payram_dashboard_helper",
		Description: "Guided answers for PayRam dashboard: purpose, sections, setup, sweeping, projects, API keys, webhooks, payment links, payouts, referrals.",
		InputSchema: &protocol.JSONSchema{
			Type: "object",
			Properties: map[string]protocol.JSONSchema{
				"topic": {
					Type:        "string",
					Enum:        []string{"purpose", "sections", "setup_payments", "fund_sweeping_evm_tron", "fund_sweeping_btc", "create_project", "api_key", "webhooks", "payment_link", "payouts", "referrals"},
					Description: "Topic to explain",
				},
				"detail": {
					Type:        "boolean",
					Description: "If true, return stepwise guidance",
				},
			},
			Required: []string{"topic"},
		},
	}
}

type dashboardArgs struct {
	Topic  string `json:"topic"`
	Detail bool   `json:"detail"`
}

// Invoke routes dashboard explanations by topic.
func (t *PayramDashboardHelper) Invoke(ctx context.Context, raw json.RawMessage) (protocol.CallResult, *protocol.ResponseError) {
	_ = ctx
	var args dashboardArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "invalid arguments"}
		}
	}

	topic := strings.ToLower(strings.TrimSpace(args.Topic))
	builder, ok := dashboardTopics()[topic]
	if !ok {
		return protocol.CallResult{}, &protocol.ResponseError{Code: -32602, Message: "unknown topic"}
	}

	text := builder(args.Detail)
	return protocol.CallResult{Content: []protocol.ContentPart{{Type: "text", Text: text}}}, nil
}

// dashboardTopics maps topic names to builders.
func dashboardTopics() map[string]func(detail bool) string {
	return map[string]func(detail bool) string{
		"purpose": func(bool) string {
			return compact("PayRam dashboard centralizes setup (nodes, wallets), payments, payouts, analytics, and referrals. It is the control plane for self-hosted PayRam to monitor health, create links, manage teams, and track revenue.", refs("features/analytics-and-reporting.md", "onboarding-guide/introduction.md"))
		},
		"sections": func(detail bool) string {
			body := "Core areas:\n" +
				"- Dashboard/Analytics: revenue, customers, transaction health (Analytics & Reporting).\n" +
				"- Payments: payment links, payment status, hosted pages (Payment Links, Testing Payment Links).\n" +
				"- Wallet Management: deposit wallets, hot wallet, cold wallet references (Wallet Integration, Hot Wallet Setup).\n" +
				"- Settings → Accounts/Projects/API Keys, Integrations (nodes, SMTP), Webhooks.\n" +
				"- Withdraw → Payouts (Address Book, approvals, OTP).\n" +
				"- Growth → Referrals (campaigns, event keys).\n" +
				"- Tools/Logs: restart guidance in script usage for stuck blocks."
			return compact(body, refs(
				"features/analytics-and-reporting.md",
				"features/payment-links.md",
				"onboarding-guide/wallet-integration.md",
				"onboarding-guide/hot-wallet-setup.md",
				"onboarding-guide/node-details-configuration.md",
				"features/payouts.md",
				"faqs/referral-faqs.md",
				"script/script-usage.md",
			))
		},
		"setup_payments": func(detail bool) string {
			if !detail {
				return compact("Setup flow: root account → node details → wallets (master + cold) → hot wallet (EVM/Tron) → optionally Fiat onramp → test payment link.", refs(
					"onboarding-guide/root-account-setup.md",
					"onboarding-guide/node-details-configuration.md",
					"onboarding-guide/wallet-integration.md",
					"onboarding-guide/hot-wallet-setup.md",
					"onboarding-guide/testing-payment-links.md",
				))
			}
			steps := []string{
				// "Create root admin at /signup (Root Account Setup).",
				"Configure nodes in Settings → Integrations → Node Details for chosen networks (Node Details Configuration).",
				"Add wallets: deploy fund sweeper for EVM/Base, Tron; add BTC seed + cold wallet (Wallet Integration).",
				"Add hot wallet: EVM hot wallet (covers Base/Ethereum) and Tron; fund with gas (Hot Wallet Setup).",
				// "Optional: enable TransFi in Settings → Payment Channels for fiat onramp (Fiat Onramp).",
				"Generate a test payment link under Payments to verify deposit addresses (Testing Payment Links).",
			}
			return compact(joinSteps(steps), refs(
				"onboarding-guide/root-account-setup.md",
				"onboarding-guide/node-details-configuration.md",
				"onboarding-guide/wallet-integration.md",
				"onboarding-guide/hot-wallet-setup.md",
				"onboarding-guide/testing-payment-links.md",
				"features/fiat-onramp.md",
			))
		},
		"fund_sweeping_evm_tron": func(detail bool) string {
			if !detail {
				return compact("EVM/Base/Tron use SmartSweep contracts: deploy via wallet setup, ensure hot wallet gas, sweeps auto-trigger per thresholds; logs visible in SmartSweep dashboard.", refs("features/smartsweep.md", "onboarding-guide/funds-sweeping.md", "onboarding-guide/hot-wallet-setup.md"))
			}
			steps := []string{
				"Deploy sweep contract per chain during wallet setup (Wallet Integration for EVM/Tron).",
				"Fund hot wallet for the chain; no sweep without gas (Hot Wallet Setup).",
				"Ensure SmartSweep thresholds: min balance to activate, amount/address-count/time triggers (Hot Wallet Setup).",
				"Sweeps run automatically; review in SmartSweep dashboard; payout cold wallet receives funds (SmartSweep, Funds Sweeping).",
			}
			return compact(joinSteps(steps), refs("features/smartsweep.md", "onboarding-guide/wallet-integration.md", "onboarding-guide/hot-wallet-setup.md", "onboarding-guide/funds-sweeping.md"))
		},
		"fund_sweeping_btc": func(detail bool) string {
			if !detail {
				return compact("BTC sweeping is manual via PayRam mobile app: link app with QR, login, link same seed, approve batches in Signing Requests, sweep to cold wallet.", refs("onboarding-guide/bitcoin-funds-sweep-guide.md", "onboarding-guide/funds-sweeping.md"))
			}
			steps := []string{
				"Install mobile app, scan dashboard QR in Settings → Accounts to pair (Bitcoin Funds Sweep Guide).",
				"Login with root creds, set app passcode (Bitcoin Funds Sweep Guide).",
				"Link wallet using the same BTC seed used in wallet setup (Wallet Integration BTC tab + Bitcoin Funds Sweep Guide).",
				"Open Signing Requests → Pending, approve batch sweeps; monitor In Progress and History for completion (Bitcoin Funds Sweep Guide).",
			}
			return compact(joinSteps(steps), refs("onboarding-guide/bitcoin-funds-sweep-guide.md", "onboarding-guide/wallet-integration.md", "onboarding-guide/funds-sweeping.md"))
		},
		"create_project": func(detail bool) string {
			steps := []string{
				"Settings → Accounts → New Project (Multi-brand Setup).",
				"Fill name, website, success/redirection URLs, branding (logo/colors/social).",
				"Configure project payment options and wallet; each project gets its own API keys and webhooks.",
				"Use project switcher to view analytics and payments per project; assign roles via User Management.",
			}
			return compact(joinSteps(steps), refs("features/multi-brand-setup.md", "features/user-management.md"))
		},
		"api_key": func(detail bool) string {
			steps := []string{
				"Open Settings → Accounts → select project → API Keys (Payment APIs).",
				"Copy existing key or Add New; keys are project-scoped; toggle to deactivate.",
				"Use key in `API-Key` header for REST calls (Payment APIs, API Integration FAQ).",
			}
			return compact(joinSteps(steps), refs("features/payment-apis.md", "faqs/api-integration-faqs.md", "features/multi-brand-setup.md"))
		},
		"webhooks": func(detail bool) string {
			steps := []string{
				"Set project webhookEndpoint in Settings → Accounts → Project (Configuration FAQ).",
				"PayRam sends GET with JSON body; verify API-Key header equals project key (API Integration FAQ).",
				"Respond 200 OK after validating reference_id/amount/payment_state.",
			}
			return compact(joinSteps(steps), refs("faqs/api-integration-faqs.md", "faqs/configuration-faqs.md"))
		},
		"payment_link": func(detail bool) string {
			if !detail {
				return compact("Payments → Create Payment Link: add member/email, choose project, amount, generate link; customer pays choosing coin/network; track status under Payments.", refs("features/payment-links.md", "onboarding-guide/testing-payment-links.md"))
			}
			steps := []string{
				"Go to Payments → Create Payment Link (Payment Links, Testing Payment Links).",
				"Add member (email) and select project; if first time, add new member.",
				"Enter amount in USD, Generate Payment Link; share resulting URL (DM/email/QR).",
				"Customer selects coin/network, pays; status updates after confirmations; troubleshoot blank address by restart script (Testing Payment Links).",
			}
			return compact(joinSteps(steps), refs("features/payment-links.md", "onboarding-guide/testing-payment-links.md", "script/script-usage.md"))
		},
		"payouts": func(detail bool) string {
			if !detail {
				return compact("Withdraw → Payouts: add recipient to Address Book, create payout, admin approves via OTP (SMTP required), track in dashboard; APIs available for automation.", refs("features/payouts.md"))
			}
			steps := []string{
				"Ensure SMTP configured in Settings → Integrations → Email Servers (Payouts).",
				"Withdraw → Payouts → Add recipient to Address Book (network, address, notes) with OTP verification.",
				"Create Payout: choose recipient and amount; submit request.",
				"Admin approves with OTP; then funds sent; monitor request list (Payouts).",
			}
			return compact(joinSteps(steps), refs("features/payouts.md"))
		},
		"referrals": func(detail bool) string {
			steps := []string{
				"Growth → Campaigns → Create New Campaign with name, budget, duration, reward events; get event_key (Referral FAQ).",
				"Embed referral dashboard iframe via referral-auth API; users log in there.",
				"Link referee to referrer by POST /api/v1/referral/referee with API key, email, referrer code, referenceID.",
				"Log rewardable events via POST /api/v1/referral/event-log with eventKey and referenceID (amount optional).",
			}
			return compact(joinSteps(steps), refs("faqs/referral-faqs.md"))
		},
	}
}

func compact(body string, refs string) string {
	body = strings.TrimSpace(body)
	if refs == "" {
		return body
	}
	return fmt.Sprintf("%s\nSources: %s", body, refs)
}

func joinSteps(steps []string) string {
	for i := range steps {
		steps[i] = fmt.Sprintf("%d) %s", i+1, steps[i])
	}
	return strings.Join(steps, "\n")
}

func refs(paths ...string) string {
	if len(paths) == 0 {
		return ""
	}
	return strings.Join(paths, "; ")
}
