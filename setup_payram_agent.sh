#!/usr/bin/env bash
set -euo pipefail

ENV_FILE=".env"
WITH_TELEGRAM=false
ADD_TELEGRAM_CHAT_IDS=""
SKIP_START=false

usage() {
  cat <<EOF
Usage: $0 [--with-telegram] [--allow-telegram-chat "123,456"] [--env-file FILE] [--no-start]

Flags:
  --with-telegram           Enable Telegram bot setup (prompts for token, dashboard creds, allowed chat IDs)
  --allow-telegram-chat ID  Comma-separated chat IDs to append to the allowlist (used with or without --with-telegram)
  --env-file FILE           Override env file path (default: .env)
  --no-start                Do not prompt to start docker container
  -h, --help                Show this help
EOF
}

# Basic color helpers (no-op if tput unavailable or not a TTY)
if command -v tput >/dev/null 2>&1 && [[ -t 1 ]]; then
  BOLD="$(tput bold)"; RED="$(tput setaf 1)"; GREEN="$(tput setaf 2)"; YELLOW="$(tput setaf 3)"; CYAN="$(tput setaf 6)"; RESET="$(tput sgr0)"
else
  BOLD=""; RED=""; GREEN=""; YELLOW=""; CYAN=""; RESET=""
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --with-telegram)
      WITH_TELEGRAM=true
      shift
      ;;
    --allow-telegram-chat)
      ADD_TELEGRAM_CHAT_IDS="${2:-}"
      shift 2
      ;;
    --env-file)
      ENV_FILE="${2:-.env}"
      shift 2
      ;;
    --no-start)
      SKIP_START=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown flag: $1" >&2
      usage
      exit 1
      ;;
  esac
done

prompt() {
  local var="$1"
  local label="$2"
  local secret="${3:-false}"
  local allow_empty="${4:-false}"
  local val=""

  if [[ "$secret" == "true" ]]; then
    read -r -s -p "${BOLD}${label}${RESET}: " val
    echo
  else
    read -r -p "${BOLD}${label}${RESET}: " val
  fi

  if [[ -z "$val" && "$allow_empty" != "true" ]]; then
    echo "${RED}ERROR${RESET}: $label cannot be empty" >&2
    exit 1
  fi

  printf '%s' "$val"
}

# --- Inputs ---
PAYRAM_ANALYTICS_BASE_URL="$(prompt "PAYRAM_ANALYTICS_BASE_URL" "Payram server url (e.g., http://10.0.0.5:8080 or https://payram.example.com:8443)")"
OPENAI_API_KEY="$(prompt "OPENAI_API_KEY" "OpenAI API key (leave empty to set later via admin secrets)" true true)"

TELEGRAM_BOT_TOKEN=""
TELEGRAM_WEBHOOK_BASE_URL=""
PAYRAM_DASHBOARD_USERNAME=""
PAYRAM_DASHBOARD_PASSWORD=""
TELEGRAM_ALLOWED_CHAT_IDS=""

if [[ "$WITH_TELEGRAM" == "true" ]]; then
  TELEGRAM_BOT_TOKEN="$(prompt "TELEGRAM_BOT_TOKEN" "Telegram bot token from BotFather" true)"
  TELEGRAM_WEBHOOK_BASE_URL="$(prompt "TELEGRAM_WEBHOOK_BASE_URL" "Public base URL for Telegram webhook (https://bot.example.com) (leave empty for polling)" false true)"
  PAYRAM_DASHBOARD_USERNAME="$(prompt "PAYRAM_DASHBOARD_USERNAME" "PayRam dashboard username")"
  PAYRAM_DASHBOARD_PASSWORD="$(prompt "PAYRAM_DASHBOARD_PASSWORD" "PayRam dashboard password" true)"
  TELEGRAM_ALLOWED_CHAT_IDS="$(prompt "TELEGRAM_ALLOWED_CHAT_IDS" "Allowed Telegram chat IDs (comma-separated, can be empty)" false true)"
fi

if [[ -n "$ADD_TELEGRAM_CHAT_IDS" ]]; then
  if [[ -n "$TELEGRAM_ALLOWED_CHAT_IDS" ]]; then
    TELEGRAM_ALLOWED_CHAT_IDS+=" ,$ADD_TELEGRAM_CHAT_IDS"
  else
    TELEGRAM_ALLOWED_CHAT_IDS="$ADD_TELEGRAM_CHAT_IDS"
  fi
fi

# --- Constants you requested ---

PAYRAM_AGENT_LISTEN_ADDR=":9900"
PAYRAM_CHAT_PORT="2358"
PAYRAM_MCP_PORT="3333"
MCP_URL="http://127.0.0.1:3333"

PAYRAM_CORE_URL="$PAYRAM_ANALYTICS_BASE_URL"

PAYRAM_AGENT_HOME="/var/lib/payram-mcp"
PAYRAM_AGENT_UPDATE_BASE_URL="https://raw.githubusercontent.com/PayRam/analytics-mcp-server/main/updates"
PAYRAM_AGENT_UPDATE_CHANNEL="stable"
PAYRAM_AGENT_UPDATE_PUBKEY_B64="j+5NTEoMyKaFGIRHZPGnwdQF+DR0m3jpLB4trXLp4eg="
PAYRAM_AGENT_IGNORE_COMPAT="true"

# Default allowlist + add resolved Payram server IP (if possible)
BASE_ALLOWLIST="127.0.0.1/32,172.16.0.0/12"

extract_host() {
  # strips scheme, path, query; keeps host (no port)
  echo "$1" | sed -E 's#^[a-zA-Z]+://##' | sed -E 's#/.*$##' | sed -E 's/\?.*$//' | sed -E 's/#.*$//' | sed -E 's/:([0-9]+)$//'
}

PAYRAM_HOST="$(extract_host "$PAYRAM_ANALYTICS_BASE_URL")"

RESOLVED_IP=""
if [[ -n "$PAYRAM_HOST" ]]; then
  # If host is already an IPv4, accept it
  if [[ "$PAYRAM_HOST" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
    RESOLVED_IP="$PAYRAM_HOST"
  else
    # Try to resolve domain -> IPv4 using getent
    if command -v getent >/dev/null 2>&1; then
      RESOLVED_IP="$(getent ahostsv4 "$PAYRAM_HOST" 2>/dev/null | awk '{print $1}' | head -n 1 || true)"
    fi
  fi
fi

PAYRAM_AGENT_ADMIN_ALLOWLIST="$BASE_ALLOWLIST"
if [[ -n "$RESOLVED_IP" ]]; then
  PAYRAM_AGENT_ADMIN_ALLOWLIST="$PAYRAM_AGENT_ADMIN_ALLOWLIST,$RESOLVED_IP/32"
else
  echo "${YELLOW}WARN${RESET}: Could not resolve Payram server host '$PAYRAM_HOST' to an IPv4. Keeping default allowlist only." >&2
fi

# --- Write .env ---
cat > "$ENV_FILE" <<EOF
# =====================
# Payram / Analytics
# =====================
PAYRAM_ANALYTICS_BASE_URL=$PAYRAM_ANALYTICS_BASE_URL
PAYRAM_ANALYTICS_TOKEN=

OPENAI_API_KEY=$OPENAI_API_KEY

# =====================
# Agent
# =====================
PAYRAM_AGENT_LISTEN_ADDR=$PAYRAM_AGENT_LISTEN_ADDR
PAYRAM_AGENT_ADMIN_ALLOWLIST=$PAYRAM_AGENT_ADMIN_ALLOWLIST
PAYRAM_CORE_URL=$PAYRAM_CORE_URL

# =====================
# Child services
# =====================
PAYRAM_CHAT_PORT=$PAYRAM_CHAT_PORT
PAYRAM_MCP_PORT=$PAYRAM_MCP_PORT
MCP_URL=$MCP_URL

# =====================
# Update system
# =====================
PAYRAM_AGENT_HOME=$PAYRAM_AGENT_HOME
PAYRAM_AGENT_UPDATE_BASE_URL=$PAYRAM_AGENT_UPDATE_BASE_URL
PAYRAM_AGENT_UPDATE_CHANNEL=$PAYRAM_AGENT_UPDATE_CHANNEL
PAYRAM_AGENT_UPDATE_PUBKEY_B64=$PAYRAM_AGENT_UPDATE_PUBKEY_B64

# =====================
# Compatibility
# =====================
PAYRAM_AGENT_IGNORE_COMPAT=$PAYRAM_AGENT_IGNORE_COMPAT
PAYRAM_AGENT_PROXY_CHAT=true
CHAT_API_BOT_BYPASS_TOKEN=${TELEGRAM_BOT_TOKEN}

# =====================
# Telegram (optional)
# =====================
TELEGRAM_ENABLED=${WITH_TELEGRAM}
TELEGRAM_BOT_TOKEN=${TELEGRAM_BOT_TOKEN}
TELEGRAM_WEBHOOK_BASE_URL=${TELEGRAM_WEBHOOK_BASE_URL}
TELEGRAM_ALLOWED_CHAT_IDS=${TELEGRAM_ALLOWED_CHAT_IDS}
PAYRAM_DASHBOARD_USERNAME=${PAYRAM_DASHBOARD_USERNAME}
PAYRAM_DASHBOARD_PASSWORD=${PAYRAM_DASHBOARD_PASSWORD}
EOF

echo
echo "${GREEN}✅ Wrote $ENV_FILE${RESET}"
echo

# --- Print the final URLs you care about ---
MANIFEST_URL="$PAYRAM_AGENT_UPDATE_BASE_URL/$PAYRAM_AGENT_UPDATE_CHANNEL/manifest.json"
SIG_URL="$PAYRAM_AGENT_UPDATE_BASE_URL/$PAYRAM_AGENT_UPDATE_CHANNEL/manifest.json.sig"

echo "${CYAN}Update manifest URL:${RESET}"
echo "  $MANIFEST_URL"
echo "${CYAN}Signature URL:${RESET}"
echo "  $SIG_URL"
echo

HOST_AGENT_HOME=${HOST_AGENT_HOME:-"$PWD/.agent-home"}
RUN_IMAGE=${RUN_IMAGE:-payram-analytics-agent:dev}

echo "Using HOST_AGENT_HOME=$HOST_AGENT_HOME"

# --- Persist Telegram secrets into state/secrets.json (host path) ---
if [[ "$WITH_TELEGRAM" == "true" || -n "$ADD_TELEGRAM_CHAT_IDS" ]]; then
  STATE_DIR="$HOST_AGENT_HOME/state"
  SECRETS_PATH="$STATE_DIR/secrets.json"
  mkdir -p "$STATE_DIR"
  if [[ ! -w "$STATE_DIR" ]]; then
    echo "${RED}ERROR${RESET}: cannot write to $STATE_DIR. Set HOST_AGENT_HOME to a writable path (e.g., export HOST_AGENT_HOME=\"$PWD/.agent-home\") and rerun." >&2
    exit 1
  fi

  echo "${CYAN}Writing Telegram secrets to${RESET} $SECRETS_PATH"
  TELEGRAM_ALLOWED_CHAT_IDS_CLEAN="${TELEGRAM_ALLOWED_CHAT_IDS// /}"
  ADD_TELEGRAM_CHAT_IDS_CLEAN="${ADD_TELEGRAM_CHAT_IDS// /}"

  TELEGRAM_BOT_TOKEN="$TELEGRAM_BOT_TOKEN" \
  TELEGRAM_WEBHOOK_BASE_URL="$TELEGRAM_WEBHOOK_BASE_URL" \
  PAYRAM_DASHBOARD_USERNAME="$PAYRAM_DASHBOARD_USERNAME" \
  PAYRAM_DASHBOARD_PASSWORD="$PAYRAM_DASHBOARD_PASSWORD" \
  TELEGRAM_ALLOWED_CHAT_IDS_CLEAN="$TELEGRAM_ALLOWED_CHAT_IDS_CLEAN" \
  ADD_TELEGRAM_CHAT_IDS_CLEAN="$ADD_TELEGRAM_CHAT_IDS_CLEAN" \
  python3 - "$SECRETS_PATH" <<'PY'
import json, os, sys

secrets_path = sys.argv[1]

bot_token = os.environ.get("TELEGRAM_BOT_TOKEN", "")
webhook_base = os.environ.get("TELEGRAM_WEBHOOK_BASE_URL", "")
dash_user = os.environ.get("PAYRAM_DASHBOARD_USERNAME", "")
dash_pass = os.environ.get("PAYRAM_DASHBOARD_PASSWORD", "")
allowed_raw = os.environ.get("TELEGRAM_ALLOWED_CHAT_IDS_CLEAN", "")
append_raw = os.environ.get("ADD_TELEGRAM_CHAT_IDS_CLEAN", "")

def parse_ids(raw):
  return [v for v in [p.strip() for p in raw.split(',')] if v]

allowed = parse_ids(allowed_raw)
allowed += parse_ids(append_raw)

data = {}
if os.path.exists(secrets_path):
  try:
    with open(secrets_path, 'r', encoding='utf-8') as f:
      data = json.load(f)
  except Exception:
    data = {}

if bot_token:
  data["telegram_bot_token"] = bot_token
if webhook_base:
  data["telegram_base_url"] = webhook_base
if allowed:
  existing = data.get("telegram_allowed_chats", []) or []
  merged = []
  seen = set()
  for v in list(existing) + allowed:
    v = str(v).strip()
    if not v or v in seen:
      continue
    seen.add(v)
    merged.append(v)
  data["telegram_allowed_chats"] = merged
if dash_user:
  data["payram_dashboard_username"] = dash_user
if dash_pass:
  data["payram_dashboard_password"] = dash_pass

os.makedirs(os.path.dirname(secrets_path), exist_ok=True)
tmp = secrets_path + '.tmp'
with open(tmp, 'w', encoding='utf-8') as f:
  json.dump(data, f, indent=2)
os.chmod(tmp, 0o600)
os.replace(tmp, secrets_path)
PY
fi

echo "Run docker with:"
cat <<CMD
docker run --rm -d \
  --name payram-analytics-agent \
  --add-host=host.docker.internal:host-gateway \
  -p 127.0.0.1:9900:9900 \
  -p 127.0.0.1:2358:2358 \
  -p 127.0.0.1:3333:3333 \
  -v ${HOST_AGENT_HOME}:/var/lib/payram-mcp \
  --env-file ${ENV_FILE} \
  ${RUN_IMAGE}
CMD

if [[ "$SKIP_START" != "true" ]]; then
  read -r -p "${BOLD}Start docker container now? (Y/n)${RESET} " START_NOW
  if [[ -z "$START_NOW" || "$START_NOW" =~ ^[Yy]$ ]]; then
    if ! command -v docker >/dev/null 2>&1; then
      echo "${RED}ERROR${RESET}: docker not found in PATH" >&2
      exit 1
    fi
    mkdir -p "$HOST_AGENT_HOME"
    echo "${CYAN}Starting container...${RESET}"
    docker run --rm -d \
      --name payram-analytics-agent \
      --add-host=host.docker.internal:host-gateway \
      -p 127.0.0.1:9900:9900 \
      -p 127.0.0.1:2358:2358 \
      -p 127.0.0.1:3333:3333 \
      -v "${HOST_AGENT_HOME}:/var/lib/payram-mcp" \
      --env-file "${ENV_FILE}" \
      "${RUN_IMAGE}"
    echo "${GREEN}Container started in background. View logs with: docker logs -f payram-analytics-agent${RESET}"
  fi
else
  echo "${YELLOW}Skipping docker start (per --no-start).${RESET}"
fi

echo
echo "After it starts, test:"
echo '  curl -s http://127.0.0.1:2358/health'
echo '  curl -s http://127.0.0.1:3333/health'
echo '  curl -s -H "X-MCP-Key: agent_secret" http://127.0.0.1:9900/admin/version'
echo '  curl -s -H "X-MCP-Key: agent_secret" "http://127.0.0.1:9900/admin/update/available?channel=stable"'
