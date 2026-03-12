package main

import (
	"flag"
	"log"

	"github.com/joho/godotenv"
	"github.com/payram/payram-analytics-mcp-server/internal/app"
)

func main() {
	_ = godotenv.Load()

	httpAddr := flag.String("http", ":3333", "MCP HTTP listen address (e.g., :3333)")
	flag.Parse()

	app.InitMCPAuth()

	log.Printf("mcp-server server listening on %s", *httpAddr)
	if err := app.RunMCPHTTP(*httpAddr); err != nil {
		log.Fatalf("MCP server error: %v", err)
	}
}
