#!/usr/bin/env bash
set -euo pipefail

ENV_FILE=".env"

# Basic color helpers (no-op if tput unavailable or not a TTY)
if command -v tput >/dev/null 2>&1 && [[ -t 1 ]]; then
  BOLD="$(tput bold)"; RED="$(tput setaf 1)"; GREEN="$(tput setaf 2)"; YELLOW="$(tput setaf 3)"; CYAN="$(tput setaf 6)"; RESET="$(tput sgr0)"
else
  BOLD=""; RED=""; GREEN=""; YELLOW=""; CYAN=""; RESET=""
fi

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

HOST_AGENT_HOME=${HOST_AGENT_HOME:-/tmp/payram-mcp}
RUN_IMAGE=${RUN_IMAGE:-payram-analytics-agent:dev}

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

echo
echo "After it starts, test:"
echo '  curl -s http://127.0.0.1:2358/health'
echo '  curl -s http://127.0.0.1:3333/health'
echo '  curl -s -H "X-MCP-Key: agent_secret" http://127.0.0.1:9900/admin/version'
echo '  curl -s -H "X-MCP-Key: agent_secret" "http://127.0.0.1:9900/admin/update/available?channel=stable"'
