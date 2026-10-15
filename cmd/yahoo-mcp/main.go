package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/aleslanger/yahoo-mail-mcp/internal/config"
	"github.com/aleslanger/yahoo-mail-mcp/internal/mail"
	"github.com/aleslanger/yahoo-mail-mcp/internal/rules"
	"github.com/aleslanger/yahoo-mail-mcp/internal/send"
	"github.com/aleslanger/yahoo-mail-mcp/internal/server"
	"github.com/aleslanger/yahoo-mail-mcp/internal/storage"
	"github.com/aleslanger/yahoo-mail-mcp/internal/version"
)

// Exit codes: usage errors and failures are distinguished for scripts;
// storage-status uses exitAlert when a review threshold is exceeded.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
	exitAlert   = 3
)

const connectionTestTimeout = 45 * time.Second

const usage = "usage: yahoo-mcp serve|serve-http|http-token|sync <alias> [folder...]|rules-run [--apply]|config-validate|storage-status|accounts-list|account-add <alias> <address>|account-remove <alias>|account-test <alias>|smtp-test <alias>|version"

// cli carries the process environment so commands can be tested in-process.
type cli struct {
	ctx    context.Context
	stdout io.Writer
	stderr io.Writer
	getenv func(string) string
}

type command func(c cli, cfg config.Config, path string, args []string) int

var commands = map[string]command{
	"config-validate": configValidate,
	"storage-status":  storageStatus,
	"accounts-list":   accountsList,
	"account-add":     accountAdd,
	"account-remove":  accountRemove,
	"account-test":    accountTest,
	"smtp-test":       smtpTest,
	"serve":           serve,
	"serve-http":      serveHTTP,
	"rules-run":       rulesRun,
	"sync":            syncCommand,
}

func main() {
	c := cli{ctx: context.Background(), stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv}
	os.Exit(c.run(runtime.GOOS, os.Args[1:]))
}

func (c cli) run(goos string, args []string) int {
	if goos == "windows" {
		fmt.Fprintln(c.stderr, "Windows is not supported: private credential, journal and index files rely on POSIX file permissions")
		return exitUsage
	}
	if len(args) < 1 {
		fmt.Fprintln(c.stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(c.stdout, version.Version)
		return exitOK
	case "http-token":
		return c.httpToken()
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintln(c.stderr, "unknown command")
		return exitUsage
	}
	path := c.getenv("YAHOO_MCP_CONFIG")
	if path == "" {
		fmt.Fprintln(c.stderr, "YAHOO_MCP_CONFIG is required")
		return exitUsage
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(c.stderr, "configuration:", err)
		return exitFailure
	}
	return cmd(c, cfg, path, args[1:])
}

func (c cli) httpToken() int {
	token, err := config.CreateHTTPToken()
	if err != nil {
		fmt.Fprintln(c.stderr, "HTTP token:", err)
		return exitFailure
	}
	fmt.Fprintln(c.stderr, "Created ~/.config/yahoo-mail-mcp/http-token; configure your MCP client with this bearer token:")
	fmt.Fprintln(c.stdout, token)
	return exitOK
}

// fail prints a prefixed error and returns the failure exit code.
func (c cli) fail(prefix string, err error) int {
	fmt.Fprintln(c.stderr, prefix+":", err)
	return exitFailure
}

func (c cli) usageError(text string) int {
	fmt.Fprintln(c.stderr, text)
	return exitUsage
}

func configValidate(c cli, cfg config.Config, _ string, _ []string) int {
	fmt.Fprintf(c.stderr, "configuration valid: %d account(s)\n", len(cfg.Accounts))
	return exitOK
}

func storageStatus(c cli, cfg config.Config, _ string, args []string) int {
	if len(args) != 0 {
		return c.usageError("usage: yahoo-mcp storage-status")
	}
	journal, err := storage.Inspect(cfg.JournalDir)
	if err != nil {
		return c.fail("journal storage", err)
	}
	downloads, err := storage.Inspect(cfg.DownloadDir)
	if err != nil {
		return c.fail("download storage", err)
	}
	alerts := storage.Alerts(journal, downloads)
	if err := json.NewEncoder(c.stdout).Encode(struct {
		Journal   storage.Usage `json:"journal"`
		Downloads storage.Usage `json:"downloads"`
		Alerts    []string      `json:"alerts"`
	}{Journal: journal, Downloads: downloads, Alerts: alerts}); err != nil {
		fmt.Fprintln(c.stderr, "cannot write storage status")
		return exitFailure
	}
	if len(alerts) != 0 {
		return exitAlert
	}
	return exitOK
}

func accountsList(c cli, cfg config.Config, _ string, _ []string) int {
	for _, a := range cfg.Accounts {
		fmt.Fprintf(c.stdout, "%s\t%s\torganize=%t\tsend=%t\n", a.Alias, a.Address, a.AllowOrganize, a.AllowSend)
	}
	return exitOK
}

func accountAdd(c cli, cfg config.Config, path string, args []string) int {
	if len(args) != 2 {
		return c.usageError("usage: yahoo-mcp account-add <alias> <address>")
	}
	if _, exists := cfg.Account(args[0]); exists {
		fmt.Fprintln(c.stderr, "account alias already exists")
		return exitFailure
	}
	cfg.Accounts = append(cfg.Accounts, config.Account{Alias: args[0], Address: args[1]})
	if err := config.Save(path, cfg); err != nil {
		return c.fail("account add", err)
	}
	fmt.Fprintf(c.stderr, "account %s added with read-only permissions; configure its app password out of band\n", args[0])
	return exitOK
}

func accountRemove(c cli, cfg config.Config, path string, args []string) int {
	if len(args) != 1 {
		return c.usageError("usage: yahoo-mcp account-remove <alias>")
	}
	remaining := make([]config.Account, 0, len(cfg.Accounts))
	for _, a := range cfg.Accounts {
		if a.Alias != args[0] {
			remaining = append(remaining, a)
		}
	}
	if len(remaining) == len(cfg.Accounts) {
		fmt.Fprintln(c.stderr, "unknown account alias")
		return exitFailure
	}
	cfg.Accounts = remaining
	if err := config.Save(path, cfg); err != nil {
		return c.fail("account remove", err)
	}
	fmt.Fprintf(c.stderr, "account %s removed from configuration; any private password file remains for manual cleanup\n", args[0])
	return exitOK
}

func accountTest(c cli, cfg config.Config, _ string, args []string) int {
	if len(args) != 1 {
		return c.usageError("usage: yahoo-mcp account-test <alias>")
	}
	account, ok := cfg.Account(args[0])
	if !ok {
		return c.usageError("account test: unknown account alias")
	}
	ctx, cancel := context.WithTimeout(c.ctx, connectionTestTimeout)
	caps, err := mail.Capabilities(ctx, account)
	cancel()
	if err != nil {
		return c.fail("account test", err)
	}
	fmt.Fprintf(c.stderr, "IMAP login OK for %s; capabilities: %d\n", args[0], len(caps))
	return exitOK
}

func smtpTest(c cli, cfg config.Config, _ string, args []string) int {
	if len(args) != 1 {
		return c.usageError("usage: yahoo-mcp smtp-test <alias>")
	}
	account, ok := cfg.Account(args[0])
	if !ok {
		return c.usageError("SMTP test: unknown account alias")
	}
	ctx, cancel := context.WithTimeout(c.ctx, connectionTestTimeout)
	err := send.TestConnection(ctx, account)
	cancel()
	if err != nil {
		return c.fail("SMTP test", err)
	}
	fmt.Fprintf(c.stderr, "SMTP login OK for %s; no message sent\n", args[0])
	return exitOK
}

func serve(c cli, cfg config.Config, _ string, _ []string) int {
	logger := slog.New(slog.NewJSONHandler(c.stderr, nil))
	if err := server.Run(c.ctx, cfg, logger); err != nil {
		logger.Error("server stopped", "error", err)
		return exitFailure
	}
	return exitOK
}

func serveHTTP(c cli, cfg config.Config, _ string, _ []string) int {
	logger := slog.New(slog.NewJSONHandler(c.stderr, nil))
	token, err := config.HTTPToken()
	if err != nil {
		return c.fail("HTTP token", err)
	}
	ctx, stop := signal.NotifyContext(c.ctx, os.Interrupt, syscall.SIGTERM)
	err = server.RunHTTP(ctx, cfg, logger, token)
	stop()
	if err != nil {
		logger.Error("HTTP server stopped", "error", err)
		return exitFailure
	}
	return exitOK
}

func rulesRun(c cli, cfg config.Config, _ string, args []string) int {
	if err := runRules(c.ctx, cfg, args, c.stdout, rules.RealOperations()); err != nil {
		return c.fail("rules-run", err)
	}
	return exitOK
}

func syncCommand(c cli, cfg config.Config, _ string, args []string) int {
	if err := runSync(c.ctx, cfg, args, c.stderr); err != nil {
		return c.fail("sync", err)
	}
	return exitOK
}
