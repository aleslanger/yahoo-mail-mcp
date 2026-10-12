package main

import (
	"context"
	"fmt"
	"github.com/ales/yahoo-mail-mcp/internal/config"
	"github.com/ales/yahoo-mail-mcp/internal/server"
	"log/slog"
	"os"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: yahoo-mcp serve|config-validate|version")
		os.Exit(2)
	}
	if os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	path := os.Getenv("YAHOO_MCP_CONFIG")
	if path == "" {
		fmt.Fprintln(os.Stderr, "YAHOO_MCP_CONFIG is required")
		os.Exit(2)
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration:", err)
		os.Exit(1)
	}
	switch os.Args[1] {
	case "config-validate":
		fmt.Fprintf(os.Stderr, "configuration valid: %d account(s)\n", len(cfg.Accounts))
	case "serve":
		logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
		if err := server.Run(context.Background(), cfg, logger); err != nil {
			logger.Error("server stopped", "error", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown command")
		os.Exit(2)
	}
}
