package main

import (
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/payram/payram-analytics-mcp-server/internal/chatapi"
	"github.com/payram/payram-analytics-mcp-server/internal/logging"
	"github.com/payram/payram-analytics-mcp-server/internal/version"
	"github.com/sirupsen/logrus"
)

func main() {
	_ = godotenv.Load()
	logger, cleanup, err := logging.New("chat-api")
	if err != nil {
		panic(err)
	}
	defer cleanup()

	port := envOr("CHAT_API_PORT", "2358")
	apiKey := envOr("CHAT_API_KEY", "")
	allowlist := envOr("CHAT_API_ALLOWLIST", envOr("PAYRAM_AGENT_ADMIN_ALLOWLIST", ""))
	openaiKey := envOr("OPENAI_API_KEY", "")
	openaiModel := envOr("OPENAI_MODEL", "gpt-4o-mini")
	openaiBase := envOr("OPENAI_BASE_URL", "https://api.openai.com/v1")
	mcpURL := envOr("MCP_SERVER_URL", "http://localhost:3333/")

	flag.StringVar(&port, "port", port, "port to listen on")
	flag.StringVar(&apiKey, "api-key", apiKey, "chat API bearer key")
	flag.StringVar(&allowlist, "allowlist", allowlist, "comma-separated CIDR allowlist for chat api")
	flag.StringVar(&openaiKey, "openai-key", openaiKey, "OpenAI API key")
	flag.StringVar(&openaiModel, "openai-model", openaiModel, "OpenAI model")
	flag.StringVar(&openaiBase, "openai-base", openaiBase, "OpenAI base URL")
	flag.StringVar(&mcpURL, "mcp", mcpURL, "MCP server URL (HTTP)")
	flag.Parse()

	if strings.TrimSpace(openaiKey) == "" {
		logger.Warn("OPENAI_API_KEY not set; chat API will fail OpenAI calls until configured")
	}

	h := chatapi.NewHandler(logger, apiKey, allowlist, openaiKey, openaiModel, openaiBase, mcpURL)
	mux := http.NewServeMux()
	h.Register(mux)
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(version.Get())
	})

	handler := logRequests(logger, mux)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	logger.Infof("Chat API listening on :%s (model=%s mcp=%s)", port, openaiModel, mcpURL)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("server error: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *responseRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func logRequests(logger *logrus.Entry, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		dur := time.Since(start).Round(time.Millisecond)
		logger.WithFields(logrus.Fields{
			"method": r.Method,
			"path":   r.URL.Path,
			"status": rec.status,
			"bytes":  rec.bytes,
			"dur":    dur,
		}).Info("request")
	})
}
