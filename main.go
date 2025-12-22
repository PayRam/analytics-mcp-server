package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/payram/payram-analytics-mcp-server/internal/app"
	"github.com/payram/payram-analytics-mcp-server/internal/chatapi"
	"github.com/sirupsen/logrus"
)

func main() {
	_ = godotenv.Load()

	// Flags / env
	mcpAddr := flag.String("mcp-http", envOr("MCP_HTTP_ADDR", ":3333"), "MCP HTTP listen address (e.g., :3333)")
	chatPort := flag.String("chat-port", envOr("CHAT_API_PORT", "2358"), "Chat API port")
	chatAPIKey := flag.String("chat-api-key", envOr("CHAT_API_KEY", ""), "Chat API bearer key")
	chatAllowlist := flag.String("chat-allowlist", envOr("CHAT_API_ALLOWLIST", envOr("PAYRAM_AGENT_ADMIN_ALLOWLIST", "")), "comma-separated CIDR allowlist for chat api")
	openaiKey := flag.String("openai-key", envOr("OPENAI_API_KEY", ""), "OpenAI API key")
	openaiModel := flag.String("openai-model", envOr("OPENAI_MODEL", "gpt-4o-mini"), "OpenAI model")
	openaiBase := flag.String("openai-base", envOr("OPENAI_BASE_URL", "https://api.openai.com/v1"), "OpenAI base URL")
	disableChat := flag.Bool("no-chat", false, "Disable chat API server")
	flag.Parse()

	if !*disableChat && strings.TrimSpace(*openaiKey) == "" {
		log.Printf("WARN: OPENAI_API_KEY not set; chat API will fail OpenAI calls until configured")
	}

	// Launch MCP HTTP server
	mcpErrCh := make(chan error, 1)
	go func() {
		log.Printf("MCP server listening on %s", *mcpAddr)
		if err := app.RunMCPHTTP(*mcpAddr); err != nil {
			mcpErrCh <- fmt.Errorf("mcp server: %w", err)
		}
	}()

	// Optionally launch chat API server (still pointing to MCP HTTP at the same port)
	if !*disableChat {
		chatErrCh := make(chan error, 1)
		go func() {
			logger := logrus.New().WithField("component", "chat-api")
			mcpURL := envOr("MCP_SERVER_URL", fmt.Sprintf("http://localhost%s/", strings.TrimPrefix(*mcpAddr, "")))
			h := chatapi.NewHandler(logger, *chatAPIKey, *chatAllowlist, *openaiKey, *openaiModel, *openaiBase, mcpURL)
			mux := http.NewServeMux()
			h.Register(mux)

			srv := &http.Server{
				Addr:              ":" + strings.TrimPrefix(*chatPort, ":"),
				Handler:           mux,
				ReadHeaderTimeout: 5 * time.Second,
			}
			logger.Infof("Chat API listening on :%s (model=%s mcp=%s)", strings.TrimPrefix(*chatPort, ":"), *openaiModel, mcpURL)
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				chatErrCh <- fmt.Errorf("chat api: %w", err)
			}
		}()

		// Wait for either server to error
		select {
		case err := <-mcpErrCh:
			log.Fatalf("MCP server error: %v", err)
		case err := <-chatErrCh:
			log.Fatalf("Chat API error: %v", err)
		}
	} else {
		// Only MCP server running
		if err := <-mcpErrCh; err != nil {
			log.Fatalf("MCP server error: %v", err)
		}
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
