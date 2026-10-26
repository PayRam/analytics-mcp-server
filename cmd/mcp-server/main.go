package main

import (
	"flag"
	"log"
	"os"

	"github.com/joho/godotenv"
	"github.com/payram/payram-analytics-mcp-server/internal/app"
	"github.com/payram/payram-analytics-mcp-server/internal/listen"
)

func main() {
	_ = godotenv.Load()

	httpAddr := flag.String("http", listen.Addr(os.Getenv("MCP_HTTP_ADDR"), "3333"), "MCP HTTP listen address; 127.0.0.1:3333 unless MCP_HTTP_ADDR or this flag names another")
	flag.Parse()

	app.InitMCPAuth()

	log.Printf("mcp-server server listening on %s", *httpAddr)
	if err := app.RunMCPHTTP(*httpAddr); err != nil {
		log.Fatalf("MCP server error: %v", err)
	}
}
