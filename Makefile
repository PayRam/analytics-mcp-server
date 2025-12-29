SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

GO ?= go
GOFMT ?= gofmt
BIN_DIR ?= bin
PKG := ./...

# Binaries
BIN_APP    ?= payram-analytics               # combined MCP + Chat API
BIN_MCP    ?= payram-analytics-mcp           # MCP HTTP only
BIN_CHAT   ?= payram-analytics-chat          # Chat API only
BIN_AGENT  ?= payram-analytics-agent         # Supervisor/agent entrypoint
BIN_BOT    ?= payram-analytics-telegram-bot  # Telegram bot
AGENT_HOME ?= $(PWD)/.agent-home
GOFILES := $(shell find . -type f -name '*.go' -not -path './vendor/*')
MCP_SERVER_URL ?= http://localhost:3333/
CHAT_API_PORT ?= 2358
OPENAI_MODEL ?= gpt-4o-mini

.PHONY: help
help:
	@echo "PayRam MCP server automation"
	@echo "Targets:"
	@echo "  make setup                Download module deps (go mod tidy)"
	@echo "  make fmt                  Run go fmt on all packages"
	@echo "  make fmt-check            Check formatting (gofmt -l)"
	@echo "  make vet                  Run go vet"
	@echo "  make test                 Run go test ./..."
	@echo "  make cover                Run tests with coverage report"
	@echo "  make build-app            Build combined app -> $(BIN_DIR)/$(BIN_APP)"
	@echo "  make build-mcp            Build mcp-server binary -> $(BIN_DIR)/$(BIN_MCP)"
	@echo "  make build-chat           Build Chat API binary -> $(BIN_DIR)/$(BIN_CHAT)"
	@echo "  make build-agent          Build supervisor agent -> $(BIN_DIR)/$(BIN_AGENT)"
	@echo "  make build-all            Build app, mcp-server, Chat API, bot, and agent"
	@echo "  make run-app              Run combined app (MCP 3333 + Chat API 2358)"
	@echo "  make run-bot              Run Telegram bot"
	@echo "  make run-mcp              Run mcp-server server on :3333"
	@echo "  make run-chat             Run Chat API on :2358 (requires OPENAI_API_KEY)"
	@echo "  make run-all              Run supervisor with chat, mcp, and bot"
	@echo "  make run-agent            Run supervisor agent on :9900 (PAYRAM_AGENT_HOME=$(AGENT_HOME))"
	@echo "  make precommit            fmt-check + vet + test + build-all"
	@echo "  make commit               Guide an interactive conventional commit"
	@echo "  make clean                Remove build artifacts and coverage files"

.PHONY: setup
setup:
	$(GO) mod tidy

.PHONY: fmt
fmt:
	$(GO) fmt $(PKG)

.PHONY: fmt-check
fmt-check:
	@set -euo pipefail; \
	CHANGED=$$($(GOFMT) -l $(GOFILES)); \
	if [ -n "$$CHANGED" ]; then \
		echo "Files need formatting:"; echo "$$CHANGED"; exit 1; \
	fi

.PHONY: vet
vet:
	$(GO) vet $(PKG)

.PHONY: test
test:
	$(GO) test $(PKG)

.PHONY: cover
cover:
	$(GO) test -coverprofile=coverage.out $(PKG)
	$(GO) tool cover -func=coverage.out

.PHONY: build-app
build-app:
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/$(BIN_APP) .

.PHONY: build-mcp
build-mcp:
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/$(BIN_MCP) cmd/mcp-server/main.go

.PHONY: build-chat
build-chat:
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/$(BIN_CHAT) cmd/chat-api/main.go

.PHONY: build-agent
build-agent:
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/$(BIN_AGENT) cmd/agent/main.go

.PHONY: build-bot
build-bot:
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/$(BIN_BOT) cmd/telegram-bot/main.go

.PHONY: build-all
build-all: build-app build-mcp build-chat build-bot build-agent

.PHONY: run-app
run-app:
	@set -euo pipefail; \
	TMP=$$(mktemp); \
	( $(GO) run ./cmd/telegram-bot & echo $$! > $$TMP ); \
	BOT_PID=$$(cat $$TMP); \
	$(GO) run .; \
	kill $$BOT_PID 2>/dev/null || true; \
	rm -f $$TMP

.PHONY: run-bot
run-bot:
	$(GO) run ./cmd/telegram-bot

.PHONY: run-all
run-all:
	@echo "Stopping any existing agent on port 9900..."
	@-lsof -ti:9900 2>/dev/null | xargs -r kill -9 2>/dev/null || true
	@sleep 1
	@echo "Cleaning binaries and agent home..."
	@rm -f $(BIN_DIR)/$(BIN_MCP) $(BIN_DIR)/$(BIN_CHAT) $(BIN_DIR)/$(BIN_BOT)
	@rm -rf $(AGENT_HOME)/current $(AGENT_HOME)/releases
	@echo "Building fresh binaries..."
	@$(MAKE) build-mcp build-chat build-bot
	@echo "Starting agent..."
	$(MAKE) run-agent

.PHONY: run-mcp
run-mcp:
	$(GO) run cmd/mcp-server/main.go --http :3333

.PHONY: run-chat
run-chat:
	$(GO) run ./cmd/chat-api --port $(CHAT_API_PORT) --mcp $(MCP_SERVER_URL) --openai-model $(OPENAI_MODEL)

.PHONY: run-agent
run-agent: build-mcp build-chat build-bot
	@mkdir -p $(AGENT_HOME)
	PAYRAM_AGENT_HOME=$(AGENT_HOME) \
	PAYRAM_AGENT_SEED_CHAT_SRC=$(BIN_DIR)/$(BIN_CHAT) \
	PAYRAM_AGENT_SEED_MCP_SRC=$(BIN_DIR)/$(BIN_MCP) \
	PAYRAM_AGENT_SEED_BOT_SRC=$(BIN_DIR)/$(BIN_BOT) \
	PAYRAM_AGENT_CHAT_BIN= \
	PAYRAM_AGENT_MCP_BIN= \
	PAYRAM_AGENT_BOT_BIN= \
	$(GO) run ./cmd/agent

.PHONY: precommit
precommit:
	@set -euo pipefail; \
	$(MAKE) fmt-check; \
	$(MAKE) vet; \
	$(MAKE) test; \
	$(MAKE) build-all

.PHONY: commit
commit: precommit
	@set -euo pipefail; \
	TYPE=${TYPE-}; SCOPE=${SCOPE-}; MSG=${MSG-}; BODY=${BODY-}; BR=${BR-}; BREAKING=${BREAKING-}; FOOTER=${FOOTER-}; ADD=${ADD-}; \
	if ! git rev-parse --git-dir >/dev/null 2>&1; then echo "Not a git repo"; exit 1; fi; \
	TYPES="feat fix chore docs refactor perf test build ci revert"; \
	if [[ -z "$$ADD" ]]; then read -p "Stage all changes (git add -A)? [Y/n]: " ADD || true; fi; \
	if [[ -z "$$ADD" || "$$ADD" =~ ^[Yy] ]]; then git add -A; fi; \
	if git diff --cached --quiet; then echo "No staged changes; aborting commit"; exit 1; fi; \
	if [ -z "$$TYPE" ]; then \
		echo "Select commit type:"; i=1; for t in $$TYPES; do echo "  $$i) $$t"; i=$$((i+1)); done; \
		read -p "Choose number: " N || true; TYPE=$$(echo $$TYPES | awk -v n=$$N '{split($$0,a," "); print a[n]}'); \
	fi; \
	if [ -z "$$TYPE" ]; then echo "Commit type required"; exit 1; fi; \
	if [ -z "$$SCOPE" ]; then read -p "Optional scope (e.g., core/tools): " SCOPE || true; fi; \
	while [ -z "$$MSG" ]; do read -p "Short description (<=72 chars): " MSG || true; done; \
	read -p "Body (optional): " BODY || true; \
	read -p "Breaking change? [y/N]: " BR || true; \
	if [[ $${BR:-N} =~ ^(y|Y)$$ ]]; then read -p "Describe breaking change: " BREAKING || true; else BREAKING=""; fi; \
	read -p "Footer (e.g., Closes #123) (optional): " FOOTER || true; \
	HEADER="$$TYPE"; [ -n "$$SCOPE" ] && HEADER="$$HEADER($$SCOPE)"; [ -n "$$BREAKING" ] && HEADER="$$HEADER!"; HEADER="$$HEADER: $$MSG"; \
	MSGFILE=$$(mktemp); echo "$$HEADER" > $$MSGFILE; \
	[ -n "$$BODY" ] && { echo; echo "$$BODY"; } >> $$MSGFILE; \
	[ -n "$$BREAKING" ] && { echo; echo "BREAKING CHANGE: $$BREAKING"; } >> $$MSGFILE; \
	[ -n "$$FOOTER" ] && { echo; echo "$$FOOTER"; } >> $$MSGFILE; \
	if git commit -F $$MSGFILE; then echo "Commit created"; else echo "Commit failed"; fi; \
	rm -f $$MSGFILE

.PHONY: clean
clean:
	rm -rf $(BIN_DIR) coverage.out
