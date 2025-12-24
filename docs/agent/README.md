# PayRam Agent (Supervisor) API

The agent supervises the Chat API and MCP processes, manages versioned releases, and exposes admin HTTP endpoints for health, status, logs, and safe rollouts.

## Running locally
- Default listen: `:9900` (override `PAYRAM_AGENT_LISTEN_ADDR`).
- Seed and state live under `PAYRAM_AGENT_HOME` (default `/var/lib/payram-mcp`). For local runs use the Make target, which seeds from local builds:
  ```sh
  make run-agent
  # uses PAYRAM_AGENT_HOME=$(PWD)/.agent-home
  # seeds chat/mcp from bin/ and starts supervisor + admin API
  ```

## Authentication
Admin endpoints require the `X-MCP-Key` header and an optional IP allowlist:
- `PAYRAM_AGENT_ADMIN_TOKEN` (required)
- `PAYRAM_AGENT_ADMIN_ALLOWLIST` (comma-separated CIDRs; empty allows any)

Send header `X-MCP-Key: <token>` on `/admin/*` routes.

## API surface
Base URL defaults to `http://localhost:9900`.

| Path | Method | Auth | Purpose |
|------|--------|------|---------|
| `/health` | GET | no | Agent liveness.
| `/version` | GET | no | Agent version info.
| `/admin/version` | GET | yes | Returns agent + child versions.
| `/admin/update/available` | GET | yes | Checks for an update. Reads `channel` query (default `stable`).
| `/admin/update/apply` | POST | yes | Downloads, verifies, switches release, restarts children, health-checks, persists status.
| `/admin/update/rollback` | POST | yes | Switches back to previous release and restarts children.
| `/admin/update/status` | GET | yes | Returns persisted update status (current, previous, last success/error, attempts).
| `/admin/child/status` | GET | yes | Supervisor child status (chat, mcp: pid, restarts, last exit).
| `/admin/child/restart` | POST | yes | Restarts both children.
| `/admin/logs?component=chat|mcp|bot&tail=N` | GET | yes | Recent buffered logs for a component (default tail 200).
| `/admin/secrets/openai` | PUT/DELETE | yes | PUT stores `openai_api_key` (body `{ "openai_api_key": "sk-..." }`); DELETE clears it. Never echoed back.
| `/admin/secrets/status` | GET | yes | Reports if `openai_api_key` is set and its source (`env|state|missing`).

### Telegram add-on (auth bypass + allowlist)
- Chat API trusts requests carrying header `X-Telegram-Bot-Token` that matches `CHAT_API_BOT_BYPASS_TOKEN` (defaults to `TELEGRAM_BOT_TOKEN`), allowing the bot to call `/v1/chat/completions` without the bearer key while retaining IP allowlist protections.
- Allowed chat IDs are stored in `${PAYRAM_AGENT_HOME}/state/secrets.json` under `telegram_allowed_chats`. The setup script can append IDs via `--allow-telegram-chat "123,456"`; restart the container or use `/admin/child/restart` to reload.
- Set `TELEGRAM_ENABLED=true` (the setup script does this with `--with-telegram`) to start the supervised bot child; its logs appear under `component=bot`.

## Update settings
- `PAYRAM_AGENT_UPDATE_BASE_URL` (required): base hosting `<channel>/manifest.json` and `.sig`.
- `PAYRAM_AGENT_UPDATE_PUBKEY_B64` (required): ed25519 pubkey (base64) for manifest verification.
- `PAYRAM_CORE_URL`: used for compatibility checks (unless ignored).
- `PAYRAM_AGENT_IGNORE_COMPAT`: `true/1` to ignore compatibility failures.
- `PAYRAM_AGENT_HEALTH_TIMEOUT_MS`: override post-restart health timeout (default 20s).
- `PAYRAM_AGENT_CHILD_HEALTH_PATH`: override child health path (default `/health`).
- `PAYRAM_CHAT_PORT`, `PAYRAM_MCP_PORT`: ports used for child health checks and defaults injected into children.

## Release layout
- Releases live under `${PAYRAM_AGENT_HOME}/releases/<version>/`.
- Binaries: `payram-analytics-chat`, `payram-analytics-mcp`.
- Compat links: `chat -> payram-analytics-chat`, `mcp -> payram-analytics-mcp`.
- Symlinks: `${PAYRAM_AGENT_HOME}/current` (active), `${PAYRAM_AGENT_HOME}/previous` (last).
- State: `${PAYRAM_AGENT_HOME}/state/update_status.json`.
- Secrets: `${PAYRAM_AGENT_HOME}/state/secrets.json` (never logged or returned).

## Example calls
Check status:
```sh
curl -s -H "X-MCP-Key: $PAYRAM_AGENT_ADMIN_TOKEN" \
  http://localhost:9900/admin/update/status
```

Check availability on `stable`:
```sh
curl -s -H "X-MCP-Key: $PAYRAM_AGENT_ADMIN_TOKEN" \
  "http://localhost:9900/admin/update/available?channel=stable"
```

Apply latest:
```sh
curl -X POST -H "X-MCP-Key: $PAYRAM_AGENT_ADMIN_TOKEN" \
  http://localhost:9900/admin/update/apply
```

Rollback:
```sh
curl -X POST -H "X-MCP-Key: $PAYRAM_AGENT_ADMIN_TOKEN" \
  http://localhost:9900/admin/update/rollback
```

Fetch child logs (last 100 lines of chat):
```sh
curl -s -H "X-MCP-Key: $PAYRAM_AGENT_ADMIN_TOKEN" \
  "http://localhost:9900/admin/logs?component=chat&tail=100"
```
