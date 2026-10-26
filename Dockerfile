# syntax=docker/dockerfile:1.28

############################
# Build stage
############################
FROM golang:1.27.3-trixie AS builder
WORKDIR /src

# Cache deps
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# Copy source
COPY . .

# Build 3 binaries (agent, chat, mcp)
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -o /out/payram-analytics-agent ./cmd/agent

RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -o /out/payram-analytics-chat ./cmd/chat-api

RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -o /out/payram-analytics-mcp ./cmd/mcp-server

RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -o /out/payram-analytics-telegram-bot ./cmd/telegram-bot

############################
# Runtime stage
############################
FROM debian:trixie-slim
WORKDIR /app
RUN apt-get update && apt-get upgrade -y && apt-get install -y ca-certificates curl && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/payram-analytics-agent /app/agent
COPY --from=builder /out/payram-analytics-chat  /app/chat
COPY --from=builder /out/payram-analytics-mcp   /app/mcp
COPY --from=builder /out/payram-analytics-telegram-bot /app/telegram-bot

# Persist updates + releases
VOLUME ["/var/lib/payram-mcp"]

# Admin + Chat + MCP
EXPOSE 9900 2358 3333

# Agent should run supervisor and manage chat+mcp from /var/lib/payram-mcp/current/...
ENTRYPOINT ["/app/agent"]
