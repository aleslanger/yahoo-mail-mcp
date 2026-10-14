package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/ales/yahoo-mail-mcp/internal/config"
	"github.com/ales/yahoo-mail-mcp/internal/mail"
	"github.com/ales/yahoo-mail-mcp/internal/rules"
	"github.com/ales/yahoo-mail-mcp/internal/send"
	"github.com/ales/yahoo-mail-mcp/internal/server"
	"github.com/ales/yahoo-mail-mcp/internal/storage"
)

const version = "0.1.0"

func main() {
	if runtime.GOOS == "windows" {
		fmt.Fprintln(os.Stderr, "Windows is not supported: private credential, journal and index files rely on POSIX file permissions")
		os.Exit(2)
	}
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: yahoo-mcp serve|serve-http|http-token|sync <alias> [folder...]|rules-run [--apply]|config-validate|storage-status|accounts-list|account-add <alias> <address>|account-remove <alias>|account-test <alias>|smtp-test <alias>|version")
		os.Exit(2)
	}
	if os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	if os.Args[1] == "http-token" {
		token, err := config.CreateHTTPToken()
		if err != nil {
			fmt.Fprintln(os.Stderr, "HTTP token:", err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "Created ~/.config/yahoo-mail-mcp/http-token; configure your MCP client with this bearer token:")
		fmt.Println(token)
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
	case "storage-status":
		if len(os.Args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: yahoo-mcp storage-status")
			os.Exit(2)
		}
		journal, err := storage.Inspect(cfg.JournalDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "journal storage:", err)
			os.Exit(1)
		}
		downloads, err := storage.Inspect(cfg.DownloadDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "download storage:", err)
			os.Exit(1)
		}
		alerts := storage.Alerts(journal, downloads)
		if err := json.NewEncoder(os.Stdout).Encode(struct {
			Journal   storage.Usage `json:"journal"`
			Downloads storage.Usage `json:"downloads"`
			Alerts    []string      `json:"alerts"`
		}{Journal: journal, Downloads: downloads, Alerts: alerts}); err != nil {
			fmt.Fprintln(os.Stderr, "cannot write storage status")
			os.Exit(1)
		}
		if len(alerts) != 0 {
			os.Exit(3)
		}
	case "accounts-list":
		for _, a := range cfg.Accounts {
			fmt.Printf("%s\t%s\torganize=%t\tsend=%t\n", a.Alias, a.Address, a.AllowOrganize, a.AllowSend)
		}
	case "account-add":
		if len(os.Args) != 4 {
			fmt.Fprintln(os.Stderr, "usage: yahoo-mcp account-add <alias> <address>")
			os.Exit(2)
		}
		if _, exists := cfg.Account(os.Args[2]); exists {
			fmt.Fprintln(os.Stderr, "account alias already exists")
			os.Exit(1)
		}
		cfg.Accounts = append(cfg.Accounts, config.Account{Alias: os.Args[2], Address: os.Args[3]})
		if err := config.Save(path, cfg); err != nil {
			fmt.Fprintln(os.Stderr, "account add:", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "account %s added with read-only permissions; configure its app password out of band\n", os.Args[2])
	case "account-remove":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: yahoo-mcp account-remove <alias>")
			os.Exit(2)
		}
		index := -1
		for i, a := range cfg.Accounts {
			if a.Alias == os.Args[2] {
				index = i
			}
		}
		if index < 0 {
			fmt.Fprintln(os.Stderr, "unknown account alias")
			os.Exit(1)
		}
		cfg.Accounts = append(cfg.Accounts[:index], cfg.Accounts[index+1:]...)
		if err := config.Save(path, cfg); err != nil {
			fmt.Fprintln(os.Stderr, "account remove:", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "account %s removed from configuration; any private password file remains for manual cleanup\n", os.Args[2])
	case "account-test":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: yahoo-mcp account-test <alias>")
			os.Exit(2)
		}
		alias := os.Args[2]
		account, ok := cfg.Account(alias)
		if !ok {
			fmt.Fprintln(os.Stderr, "account test: unknown account alias")
			os.Exit(2)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		caps, err := mail.Capabilities(ctx, account)
		cancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "account test:", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "IMAP login OK for %s; capabilities: %d\n", alias, len(caps))
	case "smtp-test":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: yahoo-mcp smtp-test <alias>")
			os.Exit(2)
		}
		alias := os.Args[2]
		account, ok := cfg.Account(alias)
		if !ok {
			fmt.Fprintln(os.Stderr, "SMTP test: unknown account alias")
			os.Exit(2)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		err := send.TestConnection(ctx, account)
		cancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "SMTP test:", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "SMTP login OK for %s; no message sent\n", alias)
	case "serve":
		logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
		if err := server.Run(context.Background(), cfg, logger); err != nil {
			logger.Error("server stopped", "error", err)
			os.Exit(1)
		}
	case "rules-run":
		if err := runRules(cfg, os.Args[2:], os.Stdout, rules.RealOperations()); err != nil {
			fmt.Fprintln(os.Stderr, "rules-run:", err)
			os.Exit(1)
		}
	case "sync":
		if err := runSync(cfg, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sync:", err)
			os.Exit(1)
		}
	case "serve-http":
		logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
		token, err := config.HTTPToken()
		if err != nil {
			fmt.Fprintln(os.Stderr, "HTTP token:", err)
			os.Exit(1)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		err = server.RunHTTP(ctx, cfg, logger, token)
		stop()
		if err != nil {
			logger.Error("HTTP server stopped", "error", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown command")
		os.Exit(2)
	}
}
