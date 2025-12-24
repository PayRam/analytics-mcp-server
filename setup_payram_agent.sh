#!/usr/bin/env bash
set -euo pipefail

ENV_FILE=".env"
WITH_TELEGRAM=true
ADD_TELEGRAM_CHAT_IDS=""
SKIP_START=false
AUTO_UPDATE=true

usage() {
  cat <<EOF
Usage: $0 [--no-telegram] [--add-telegram-chat-ids "123,456"] [--env-file FILE] [--no-start]

Flags:
  --with-telegram               (default) Telegram bot setup enabled
  --no-telegram                 Disable Telegram setup
  --add-telegram-chat-ids IDs   Comma-separated chat IDs to append to the allowlist (can be used standalone)
  --allow-telegram-chat IDs     (deprecated alias of --add-telegram-chat-ids)
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
    --no-telegram)
      WITH_TELEGRAM=false
      shift
      ;;
    --add-telegram-chat-ids|--allow-telegram-chat)
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

step=0
say_header() {
  echo
  echo "${BOLD}${CYAN}==> $1${RESET}"
}

say_info() {
  echo "${CYAN}INFO${RESET}: $1"
}

say_warn() {
  echo "${YELLOW}WARN${RESET}: $1" >&2
}

say_error() {
  echo "${RED}ERROR${RESET}: $1" >&2
}

next_step() {
  step=$((step + 1))
  echo
  echo "${BOLD}${CYAN}[$step]${RESET} $1"
}

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

prompt_default() {
  local label="$1"
  local def="$2"
  local secret="${3:-false}"
  local val=""

  if [[ "$secret" == "true" ]]; then
    read -r -s -p "${BOLD}${label}${RESET} [default: ${def}]: " val
    echo
  else
    read -r -p "${BOLD}${label}${RESET} [default: ${def}]: " val
  fi

  if [[ -z "$val" ]]; then
    val="$def"
  fi
  printf '%s' "$val"
}

prompt_yes_no() {
  local label="$1"
  local def="$2" # y or n
  local val=""
  local prompt_def="Y/n"
  if [[ "$def" == "n" ]]; then
    prompt_def="y/N"
  fi
  read -r -p "${BOLD}${label} (${prompt_def})${RESET} " val
  val="$(echo "$val" | tr '[:upper:]' '[:lower:]' | xargs)"
  if [[ -z "$val" ]]; then
    [[ "$def" == "y" ]] && return 0 || return 1
  fi
  [[ "$val" == "y" || "$val" == "yes" ]] && return 0
  return 1
}

cleanup_var() {
  # Strip CR/LF to avoid multi-line .env entries
  local v="$1"
  v="${v//$'\r'/}"
  v="${v//$'\n'/}"
  printf '%s' "$v"
}

# Fast path: append Telegram chat IDs without running the full wizard
if [[ -n "$ADD_TELEGRAM_CHAT_IDS" ]]; then
  step=0
  say_header "PayRam Analytics MCP Setup"
  say_info "Fast path: appending Telegram chat IDs only"

  HOST_AGENT_HOME=${HOST_AGENT_HOME:-"$PWD/.agent-home"}
  STATE_DIR="$HOST_AGENT_HOME/state"
  SECRETS_PATH="$STATE_DIR/secrets.json"
  mkdir -p "$STATE_DIR"
  if [[ ! -w "$STATE_DIR" ]]; then
    say_error "cannot write to $STATE_DIR. Set HOST_AGENT_HOME to a writable path and rerun."
    exit 1
  fi

  ADD_TELEGRAM_CHAT_IDS_CLEAN="${ADD_TELEGRAM_CHAT_IDS// /}"

  existing_allowed_raw="$(read_json_array "$SECRETS_PATH" "telegram_allowed_chats")"
  existing_allowed_csv="$(echo "$existing_allowed_raw" | tr -d '"' | tr ' ' ',' | sed 's/,\+/,/g; s/^,//; s/,$//')"
  merged_allowed_csv="$(merge_chat_ids "$existing_allowed_csv" "$ADD_TELEGRAM_CHAT_IDS_CLEAN")"

  bot_token="$(read_json_value "$SECRETS_PATH" "telegram_bot_token")"
  base_url="$(read_json_value "$SECRETS_PATH" "telegram_base_url")"
  dash_user="$(read_json_value "$SECRETS_PATH" "payram_dashboard_username")"
  dash_pass="$(read_json_value "$SECRETS_PATH" "payram_dashboard_password")"

  {
    echo '{'
    first=true
    if [[ -n "$bot_token" ]]; then
      echo "  \"telegram_bot_token\": \"$(escape_json "$bot_token")\""; first=false
    fi
    if [[ -n "$base_url" ]]; then
      $first || echo ,
      echo "  \"telegram_base_url\": \"$(escape_json "$base_url")\""; first=false
    fi
    if [[ -n "$dash_user" ]]; then
      $first || echo ,
      echo "  \"payram_dashboard_username\": \"$(escape_json "$dash_user")\""; first=false
    fi
    if [[ -n "$dash_pass" ]]; then
      $first || echo ,
      echo "  \"payram_dashboard_password\": \"$(escape_json "$dash_pass")\""; first=false
    fi
    if [[ -n "$merged_allowed_csv" ]]; then
      $first || echo ,
      echo -n "  \"telegram_allowed_chats\": ["
      IFS=',' read -r -a arr <<<"$merged_allowed_csv"
      for i in "${!arr[@]}"; do
        [[ $i -gt 0 ]] && echo -n ", "
        echo -n "\"$(escape_json "${arr[$i]}")\""
      done
      echo ' ]'
    else
      echo
    fi
    echo '}'
  } > "$SECRETS_PATH.tmp"

  chmod 600 "$SECRETS_PATH.tmp" 2>/dev/null || true
  mv "$SECRETS_PATH.tmp" "$SECRETS_PATH"

  say_info "Appended chat IDs: $ADD_TELEGRAM_CHAT_IDS"
  echo "  Secrets: $SECRETS_PATH"

  if command -v docker >/dev/null 2>&1 && docker ps --format '{{.Names}}' | grep -q '^payram-analytics-agent$'; then
    if prompt_yes_no "Restart running container payram-analytics-agent now to apply chat IDs?" "y"; then
      docker restart payram-analytics-agent || say_warn "Restart failed; restart manually."
    else
      say_warn "Remember to restart payram-analytics-agent to apply chat IDs."
    fi
  else
    say_warn "No running container named payram-analytics-agent detected; start/restart it to apply chat IDs."
  fi

  exit 0
fi

http_post_raw() {
  # Minimal HTTP POST using bash /dev/tcp for http:// URLs only.
  # Args: url payload
  local url="$1" payload="$2"
  if [[ ! "$url" =~ ^http://([^/:]+)(:([0-9]+))?(/.*)?$ ]]; then
    return 1
  fi
  local host="${BASH_REMATCH[1]}"
  local port="${BASH_REMATCH[3]:-80}"
  local path="${BASH_REMATCH[4]:-/}"

  # Open TCP connection
  exec 3<>"/dev/tcp/${host}/${port}" 2>/dev/null || return 1

  printf 'POST %s HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s' \
    "$path" "$host" "${#payload}" "$payload" >&3

  local status_line
  IFS= read -r status_line <&3 || true
  local status="$(echo "$status_line" | awk '{print $2}')"
  local body
  body="$(cat <&3)"
  exec 3>&-

  echo "$status"
  printf '%s' "$body"
}

# JSON helpers (minimal, shell-only)
escape_json() {
  printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}

read_json_value() {
  local file="$1" key="$2"
  if [[ ! -f "$file" ]]; then
    return
  fi
  tr '\n' ' ' <"$file" | sed -n "s/.*\"$key\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" | head -n1
}

read_json_array() {
  local file="$1" key="$2"
  if [[ ! -f "$file" ]]; then
    return
  fi
  tr '\n' ' ' <"$file" | sed -n "s/.*\"$key\"[[:space:]]*:[[:space:]]*\[\([^]]*\)\].*/\1/p" | head -n1
}

merge_chat_ids() {
  # Args: existing_csv new_csv -> outputs unique CSV
  local existing="$1" incoming="$2"
  local combined="$existing,$incoming"
  local out=""
  local seen=""
  IFS=',' read -r -a parts <<<"$combined"
  for p in "${parts[@]}"; do
    p="$(echo "$p" | xargs)"
    [[ -z "$p" ]] && continue
    if [[ ",$seen," != *,"$p",* ]]; then
      seen+="$p,"
      out+="$p,"
    fi
  done
  out="${out%,}"
  printf '%s' "$out"
}

test_payram_login() {
  local base="$1"
  local user="$2"
  local pass="$3"

  local url="${base%/}/api/v1/signin"
  local payload="{\"email\":\"$user\",\"password\":\"$pass\"}"

  local status body
  if command -v curl >/dev/null 2>&1; then
    local response
    response="$(curl -sS -w '\n%{http_code}' -X POST "$url" -H 'Content-Type: application/json' -d "$payload" 2>&1)" || {
      say_error "Login request failed; check base URL/connectivity"
      exit 1
    }
    status="${response##*$'\n'}"
    body="${response%$'\n'*}"
  else
    status="$(http_post_raw "$url" "$payload" | head -n1)" || {
      say_warn "curl not found and raw http failed; skipping login test."
      return
    }
    body="$(http_post_raw "$url" "$payload" | tail -n +2)"
  fi

  if [[ "$status" != "200" && "$status" != "201" ]]; then
    say_error "Login failed (HTTP $status). Response: ${body:0:200}"
    exit 1
  fi

  say_info "Login succeeded"
}

require_cmd() {
  local cmd="$1"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    say_error "Required command not found: $cmd"
    exit 1
  fi
}

generate_token() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 24
    return
  fi
  if command -v python3 >/dev/null 2>&1; then
    python3 - <<'PY'
import secrets
print(secrets.token_hex(24))
PY
    return
  fi
  date +%s%N | sha256sum | awk '{print $1}'
}

validate_http_url() {
  local label="$1"
  local url="$2"
  if [[ ! "$url" =~ ^https?:// ]]; then
    say_error "$label must start with http:// or https://"
    exit 1
  fi
}

say_header "PayRam Analytics MCP Setup"
say_info "This wizard writes ${ENV_FILE} and (optionally) ${BOLD}${HOST_AGENT_HOME:-./.agent-home}/state/secrets.json${RESET}."
say_info "Secrets are not echoed after entry."

# --- Inputs (requested flow) ---
next_step "PayRam server"
PAYRAM_ANALYTICS_BASE_URL="$(prompt "PAYRAM_ANALYTICS_BASE_URL" "PayRam server url (e.g., http://10.0.0.5:8080 or https://payram.example.com:8443)")"
PAYRAM_ANALYTICS_BASE_URL="$(cleanup_var "$PAYRAM_ANALYTICS_BASE_URL")"
validate_http_url "PayRam server url" "$PAYRAM_ANALYTICS_BASE_URL"

next_step "PayRam dashboard admin credentials"
PAYRAM_DASHBOARD_USERNAME="$(prompt "PAYRAM_DASHBOARD_USERNAME" "Admin username")"
PAYRAM_DASHBOARD_USERNAME="$(cleanup_var "$PAYRAM_DASHBOARD_USERNAME")"
PAYRAM_DASHBOARD_PASSWORD="$(prompt "PAYRAM_DASHBOARD_PASSWORD" "Admin password" true)"
PAYRAM_DASHBOARD_PASSWORD="$(cleanup_var "$PAYRAM_DASHBOARD_PASSWORD")"
test_payram_login "$PAYRAM_ANALYTICS_BASE_URL" "$PAYRAM_DASHBOARD_USERNAME" "$PAYRAM_DASHBOARD_PASSWORD"

next_step "OpenAI key (optional)"
OPENAI_API_KEY="$(prompt "OPENAI_API_KEY" "OpenAI API key (leave empty to set later via agent secrets)" true true)"
OPENAI_API_KEY="$(cleanup_var "$OPENAI_API_KEY")"

next_step "Telegram bot (optional)"
TELEGRAM_BOT_TOKEN=""
TELEGRAM_WEBHOOK_BASE_URL=""
TELEGRAM_ALLOWED_CHAT_IDS=""

if [[ "$WITH_TELEGRAM" == "true" ]]; then
  TELEGRAM_BOT_TOKEN="$(prompt "TELEGRAM_BOT_TOKEN" "Telegram bot token from BotFather" true)"
else
  TELEGRAM_BOT_TOKEN="$(prompt "TELEGRAM_BOT_TOKEN" "Telegram bot token from BotFather (leave empty to skip Telegram setup)" true true)"
fi
TELEGRAM_BOT_TOKEN="$(cleanup_var "$TELEGRAM_BOT_TOKEN")"

if [[ -n "$TELEGRAM_BOT_TOKEN" ]]; then
  TELEGRAM_WEBHOOK_BASE_URL=""  # webhook not prompted; default to polling
  echo
  TELEGRAM_ALLOWED_CHAT_IDS="$(prompt "TELEGRAM_ALLOWED_CHAT_IDS" "Telegram chat id(s) allowed to use the bot (comma-separated, leave empty if unknown)" false true)"
fi

next_step "Auto-update"
if prompt_yes_no "Enable updates (check/apply via agent admin endpoints)" "y"; then
  AUTO_UPDATE=true
else
  AUTO_UPDATE=false
fi

if [[ -n "$ADD_TELEGRAM_CHAT_IDS" ]]; then
  if [[ -n "$TELEGRAM_ALLOWED_CHAT_IDS" ]]; then
    TELEGRAM_ALLOWED_CHAT_IDS+=" ,$ADD_TELEGRAM_CHAT_IDS"
  else
    TELEGRAM_ALLOWED_CHAT_IDS="$ADD_TELEGRAM_CHAT_IDS"
  fi
fi
TELEGRAM_ALLOWED_CHAT_IDS="$(cleanup_var "$TELEGRAM_ALLOWED_CHAT_IDS")"
ADD_TELEGRAM_CHAT_IDS="$(cleanup_var "$ADD_TELEGRAM_CHAT_IDS")"

# --- Constants you requested ---

PAYRAM_AGENT_LISTEN_ADDR=":9900"
PAYRAM_CHAT_PORT="2358"
PAYRAM_MCP_PORT="3333"
MCP_SERVER_URL="http://127.0.0.1:3333/"

# Fixed model (no prompt)
OPENAI_MODEL="gpt-4o-mini"

# Chat API left open (relies on loopback binding / network controls)
CHAT_API_KEY=""

# Admin token (generated automatically)
PAYRAM_AGENT_ADMIN_TOKEN="$(generate_token)"

PAYRAM_CORE_URL="$PAYRAM_ANALYTICS_BASE_URL"

PAYRAM_AGENT_HOME="/var/lib/payram-mcp"
PAYRAM_AGENT_UPDATE_BASE_URL="https://raw.githubusercontent.com/PayRam/analytics-mcp-server/main/updates"
PAYRAM_AGENT_UPDATE_CHANNEL="stable"
PAYRAM_AGENT_UPDATE_PUBKEY_B64="j+5NTEoMyKaFGIRHZPGnwdQF+DR0m3jpLB4trXLp4eg="
PAYRAM_AGENT_IGNORE_COMPAT="true"

if [[ "$AUTO_UPDATE" != "true" ]]; then
  PAYRAM_AGENT_UPDATE_BASE_URL=""
  PAYRAM_AGENT_UPDATE_PUBKEY_B64=""
fi

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
  say_warn "Could not resolve PayRam server host '$PAYRAM_HOST' to an IPv4. Keeping default allowlist only."
fi

CHAT_API_ALLOWLIST="$PAYRAM_AGENT_ADMIN_ALLOWLIST"

# --- Write .env ---
cat > "$ENV_FILE" <<EOF
# =====================
# Payram / Analytics
# =====================
PAYRAM_ANALYTICS_BASE_URL=$PAYRAM_ANALYTICS_BASE_URL
PAYRAM_ANALYTICS_TOKEN=
PAYRAM_DASHBOARD_USERNAME=$PAYRAM_DASHBOARD_USERNAME
PAYRAM_DASHBOARD_PASSWORD=$PAYRAM_DASHBOARD_PASSWORD

OPENAI_API_KEY=$OPENAI_API_KEY
OPENAI_MODEL=$OPENAI_MODEL

# =====================
# Agent
# =====================
PAYRAM_AGENT_LISTEN_ADDR=$PAYRAM_AGENT_LISTEN_ADDR
PAYRAM_AGENT_ADMIN_ALLOWLIST=$PAYRAM_AGENT_ADMIN_ALLOWLIST
PAYRAM_AGENT_ADMIN_TOKEN=$PAYRAM_AGENT_ADMIN_TOKEN
PAYRAM_CORE_URL=$PAYRAM_CORE_URL

# =====================
# Child services
# =====================
PAYRAM_CHAT_PORT=$PAYRAM_CHAT_PORT
PAYRAM_MCP_PORT=$PAYRAM_MCP_PORT
MCP_SERVER_URL=$MCP_SERVER_URL

# Chat API (left open by default; rely on loopback binding / allowlist)
CHAT_API_KEY=$CHAT_API_KEY
CHAT_API_ALLOWLIST=$CHAT_API_ALLOWLIST

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
# Bot bypass token intentionally left empty (chat stays open / allowlist-driven)
CHAT_API_BOT_BYPASS_TOKEN=

# =====================
# Telegram (optional)
# =====================
TELEGRAM_ENABLED=${WITH_TELEGRAM}
TELEGRAM_BOT_TOKEN=${TELEGRAM_BOT_TOKEN}
TELEGRAM_WEBHOOK_BASE_URL=${TELEGRAM_WEBHOOK_BASE_URL}
TELEGRAM_ALLOWED_CHAT_IDS=${TELEGRAM_ALLOWED_CHAT_IDS}
EOF

chmod 600 "$ENV_FILE" 2>/dev/null || true

echo
echo "${GREEN}✅ Wrote $ENV_FILE (chmod 600)${RESET}"
echo

# --- Print the final URLs you care about ---
if [[ -n "$PAYRAM_AGENT_UPDATE_BASE_URL" ]]; then
  MANIFEST_URL="$PAYRAM_AGENT_UPDATE_BASE_URL/$PAYRAM_AGENT_UPDATE_CHANNEL/manifest.json"
  SIG_URL="$PAYRAM_AGENT_UPDATE_BASE_URL/$PAYRAM_AGENT_UPDATE_CHANNEL/manifest.json.sig"

  echo "${CYAN}Updates:${RESET} enabled (channel=$PAYRAM_AGENT_UPDATE_CHANNEL)"
  echo "${CYAN}Update manifest URL:${RESET}"
  echo "  $MANIFEST_URL"
  echo "${CYAN}Signature URL:${RESET}"
  echo "  $SIG_URL"
  echo
else
  echo "${CYAN}Updates:${RESET} disabled"
  echo
fi

HOST_AGENT_HOME=${HOST_AGENT_HOME:-"$PWD/.agent-home"}
RUN_IMAGE=${RUN_IMAGE:-payram-analytics-agent:dev}

echo "Using HOST_AGENT_HOME=$HOST_AGENT_HOME"

# --- Persist secrets into state/secrets.json (host path) ---
if [[ -n "$PAYRAM_DASHBOARD_USERNAME" || -n "$PAYRAM_DASHBOARD_PASSWORD" || -n "$TELEGRAM_BOT_TOKEN" || -n "$TELEGRAM_ALLOWED_CHAT_IDS" || -n "$ADD_TELEGRAM_CHAT_IDS" ]]; then
  STATE_DIR="$HOST_AGENT_HOME/state"
  SECRETS_PATH="$STATE_DIR/secrets.json"
  mkdir -p "$STATE_DIR"
  if [[ ! -w "$STATE_DIR" ]]; then
    echo "${RED}ERROR${RESET}: cannot write to $STATE_DIR. Set HOST_AGENT_HOME to a writable path (e.g., export HOST_AGENT_HOME=\"$PWD/.agent-home\") and rerun." >&2
    exit 1
  fi

  echo "${CYAN}Writing secrets to${RESET} $SECRETS_PATH"
  TELEGRAM_ALLOWED_CHAT_IDS_CLEAN="${TELEGRAM_ALLOWED_CHAT_IDS// /}"
  ADD_TELEGRAM_CHAT_IDS_CLEAN="${ADD_TELEGRAM_CHAT_IDS// /}"

  existing_allowed_raw="$(read_json_array "$SECRETS_PATH" "telegram_allowed_chats")"
  existing_allowed_csv="$(echo "$existing_allowed_raw" | tr -d '"' | tr ' ' ',' | sed 's/,\+/,/g; s/^,//; s/,$//')"
  merged_allowed_csv="$(merge_chat_ids "$existing_allowed_csv" "$TELEGRAM_ALLOWED_CHAT_IDS_CLEAN,$ADD_TELEGRAM_CHAT_IDS_CLEAN")"

  existing_bot_token="$(read_json_value "$SECRETS_PATH" "telegram_bot_token")"
  existing_base_url="$(read_json_value "$SECRETS_PATH" "telegram_base_url")"
  existing_dash_user="$(read_json_value "$SECRETS_PATH" "payram_dashboard_username")"
  existing_dash_pass="$(read_json_value "$SECRETS_PATH" "payram_dashboard_password")"

  new_bot_token="${TELEGRAM_BOT_TOKEN:-$existing_bot_token}"
  new_base_url="${TELEGRAM_WEBHOOK_BASE_URL:-$existing_base_url}"
  new_dash_user="${PAYRAM_DASHBOARD_USERNAME:-$existing_dash_user}"
  new_dash_pass="${PAYRAM_DASHBOARD_PASSWORD:-$existing_dash_pass}"

  {
    echo '{'
    first=true
    if [[ -n "$new_bot_token" ]]; then
      echo "  \"telegram_bot_token\": \"$(escape_json "$new_bot_token")\""; first=false
    fi
    if [[ -n "$new_base_url" ]]; then
      $first || echo ,
      echo "  \"telegram_base_url\": \"$(escape_json "$new_base_url")\""; first=false
    fi
    if [[ -n "$new_dash_user" ]]; then
      $first || echo ,
      echo "  \"payram_dashboard_username\": \"$(escape_json "$new_dash_user")\""; first=false
    fi
    if [[ -n "$new_dash_pass" ]]; then
      $first || echo ,
      echo "  \"payram_dashboard_password\": \"$(escape_json "$new_dash_pass")\""; first=false
    fi
    if [[ -n "$merged_allowed_csv" ]]; then
      $first || echo ,
      echo -n "  \"telegram_allowed_chats\": ["
      IFS=',' read -r -a arr <<<"$merged_allowed_csv"
      for i in "${!arr[@]}"; do
        [[ $i -gt 0 ]] && echo -n ", "
        echo -n "\"$(escape_json "${arr[$i]}")\""
      done
      echo ' ]'
    else
      echo
    fi
    echo '}'
  } > "$SECRETS_PATH.tmp"

  chmod 600 "$SECRETS_PATH.tmp" 2>/dev/null || true
  mv "$SECRETS_PATH.tmp" "$SECRETS_PATH"
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

if [[ -n "$ADD_TELEGRAM_CHAT_IDS" ]]; then
  echo
  say_info "Telegram chat IDs appended. If a container is already running, restart is needed to load changes."
  if docker ps --format '{{.Names}}' | grep -q '^payram-analytics-agent$'; then
    if prompt_yes_no "Restart running container now?" "y"; then
      docker restart payram-analytics-agent || say_warn "Restart failed; please restart manually."
    else
      say_warn "Remember to restart payram-analytics-agent to apply updated chat IDs."
    fi
  else
    say_warn "No running container named payram-analytics-agent detected. Start or restart manually to apply chat ID changes."
  fi
fi

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
echo '  curl -s -H "X-MCP-Key: ${PAYRAM_AGENT_ADMIN_TOKEN}" http://127.0.0.1:9900/admin/version'
if [[ -n "$PAYRAM_AGENT_UPDATE_BASE_URL" ]]; then
  echo '  curl -s -H "X-MCP-Key: ${PAYRAM_AGENT_ADMIN_TOKEN}" "http://127.0.0.1:9900/admin/update/available?channel=stable"'
fi

echo
echo "${CYAN}Summary${RESET}"
echo "  PayRam base:     $PAYRAM_ANALYTICS_BASE_URL"
echo "  Agent API:       http://127.0.0.1:9900"
echo "  Chat API:        http://127.0.0.1:2358 (model=$OPENAI_MODEL)"
echo "  MCP Server:      http://127.0.0.1:3333"
if [[ -n "$TELEGRAM_BOT_TOKEN" ]]; then
  if [[ -n "$TELEGRAM_WEBHOOK_BASE_URL" ]]; then
    echo "  Telegram:        enabled (webhook)"
  else
    echo "  Telegram:        enabled (polling)"
  fi
else
  echo "  Telegram:        disabled"
fi
echo "  Admin token:     stored in $ENV_FILE (header X-MCP-Key)"
say_warn "Chat API is open by default; keep docker ports bound to 127.0.0.1 or add your own network controls."
