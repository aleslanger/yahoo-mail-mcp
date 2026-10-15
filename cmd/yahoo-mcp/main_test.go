package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aleslanger/yahoo-mail-mcp/internal/config"
	"github.com/aleslanger/yahoo-mail-mcp/internal/imaptest"
	"github.com/aleslanger/yahoo-mail-mcp/internal/mail"
	"github.com/aleslanger/yahoo-mail-mcp/internal/version"
)

const reportFixture = "From: Alice <alice@example.com>\r\nTo: account@example.com\r\nSubject: Weekly report\r\nDate: Mon, 12 Oct 2026 10:00:00 +0000\r\n\r\nBody\r\n"

type cliResult struct {
	code   int
	stdout string
	stderr string
}

// runCLI runs one command against a configuration file at configPath.
func runCLI(ctx context.Context, configPath string, args ...string) cliResult {
	var stdout, stderr bytes.Buffer
	env := map[string]string{"YAHOO_MCP_CONFIG": configPath}
	c := cli{ctx: ctx, stdout: &stdout, stderr: &stderr, getenv: func(k string) string { return env[k] }}
	code := c.run("linux", args)
	return cliResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func writeConfig(t *testing.T, cfg config.Config) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	return path
}

func privateTestDir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// closedPort returns a loopback port that was just released, so nothing listens on it.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func fixtureConfig(t *testing.T, folders ...string) (context.Context, config.Config, *imaptest.Server) {
	t.Helper()
	srv := imaptest.New(t, imaptest.Options{Alias: "personal", Address: "account@example.com", Password: "fixture-secret", Folders: folders, Move: true})
	return mail.WithDialTLS(context.Background(), srv.Dial), config.Config{Accounts: []config.Account{srv.Account}}, srv
}

func TestRunRejectsBadInvocations(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.json")
	for _, tc := range []struct {
		name   string
		goos   string
		config string
		args   []string
		code   int
		stderr string
	}{
		{"windows", "windows", missing, []string{"version"}, exitUsage, "Windows is not supported"},
		{"no command", "linux", missing, nil, exitUsage, "usage: yahoo-mcp"},
		{"unknown command", "linux", missing, []string{"frobnicate"}, exitUsage, "unknown command"},
		{"no config variable", "linux", "", []string{"accounts-list"}, exitUsage, "YAHOO_MCP_CONFIG is required"},
		{"unreadable config", "linux", missing, []string{"accounts-list"}, exitFailure, "configuration:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			c := cli{ctx: context.Background(), stdout: &stdout, stderr: &stderr, getenv: func(string) string { return tc.config }}
			if code := c.run(tc.goos, tc.args); code != tc.code || !strings.Contains(stderr.String(), tc.stderr) {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
		})
	}
}

func TestRunVersionNeedsNoConfiguration(t *testing.T) {
	if res := runCLI(context.Background(), "", "version"); res.code != exitOK || strings.TrimSpace(res.stdout) != version.Version {
		t.Fatalf("version: %+v", res)
	}
}

func TestHTTPTokenCommandCreatesTokenOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first := runCLI(context.Background(), "", "http-token")
	if first.code != exitOK || len(strings.TrimSpace(first.stdout)) != 64 {
		t.Fatalf("first token: %+v", first)
	}
	if second := runCLI(context.Background(), "", "http-token"); second.code != exitFailure || second.stdout != "" {
		t.Fatalf("token overwritten: %+v", second)
	}
}

func TestAccountCommandsEditConfiguration(t *testing.T) {
	path := writeConfig(t, config.Config{Accounts: []config.Account{{Alias: "personal", Address: "account@example.com"}}})
	ctx := context.Background()
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"account-add", "work"}, exitUsage},
		{[]string{"account-add", "personal", "other@example.com"}, exitFailure},
		{[]string{"account-add", "work", "work@example.com"}, exitOK},
		{[]string{"account-remove"}, exitUsage},
		{[]string{"account-remove", "missing"}, exitFailure},
		{[]string{"account-remove", "personal"}, exitOK},
	} {
		if res := runCLI(ctx, path, tc.args...); res.code != tc.code {
			t.Fatalf("%v: %+v", tc.args, res)
		}
	}
	res := runCLI(ctx, path, "accounts-list")
	if res.code != exitOK || res.stdout != "work\twork@example.com\torganize=false\tsend=false\n" {
		t.Fatalf("accounts-list: %+v", res)
	}
	if res := runCLI(ctx, path, "config-validate"); res.code != exitOK || !strings.Contains(res.stderr, "1 account(s)") {
		t.Fatalf("config-validate: %+v", res)
	}
}

func TestStorageStatusReportsUsage(t *testing.T) {
	path := writeConfig(t, config.Config{JournalDir: privateTestDir(t, "journal"), DownloadDir: privateTestDir(t, "downloads"), Accounts: []config.Account{{Alias: "personal", Address: "account@example.com"}}})
	res := runCLI(context.Background(), path, "storage-status")
	if res.code != exitOK || !strings.Contains(res.stdout, `"journal"`) || !strings.Contains(res.stdout, `"alerts":[]`) {
		t.Fatalf("storage-status: %+v", res)
	}
	if res := runCLI(context.Background(), path, "storage-status", "extra"); res.code != exitUsage {
		t.Fatalf("storage-status with argument: %+v", res)
	}
}

func TestConnectionTestCommands(t *testing.T) {
	ctx, cfg, _ := fixtureConfig(t, "INBOX")
	cfg.Accounts[0].SMTPHost, cfg.Accounts[0].SMTPPort = "127.0.0.1", closedPort(t)
	path := writeConfig(t, cfg)
	for _, tc := range []struct {
		args   []string
		code   int
		stderr string
	}{
		{[]string{"account-test", "personal"}, exitOK, "IMAP login OK for personal"},
		{[]string{"account-test"}, exitUsage, "usage"},
		{[]string{"account-test", "missing"}, exitUsage, "unknown account alias"},
		{[]string{"smtp-test", "personal"}, exitFailure, "SMTP test:"},
		{[]string{"smtp-test"}, exitUsage, "usage"},
		{[]string{"smtp-test", "missing"}, exitUsage, "unknown account alias"},
	} {
		if res := runCLI(ctx, path, tc.args...); res.code != tc.code || !strings.Contains(res.stderr, tc.stderr) {
			t.Fatalf("%v: %+v", tc.args, res)
		}
	}
}

func TestAccountTestReportsLoginFailure(t *testing.T) {
	ctx, cfg, _ := fixtureConfig(t, "INBOX")
	t.Setenv("YAHOO_MCP_PASSWORD_PERSONAL", "wrong-secret")
	if res := runCLI(ctx, writeConfig(t, cfg), "account-test", "personal"); res.code != exitFailure || !strings.Contains(res.stderr, "account test:") {
		t.Fatalf("wrong password: %+v", res)
	}
}

func TestSyncCommandIndexesEverySelectableFolder(t *testing.T) {
	ctx, cfg, srv := fixtureConfig(t, "INBOX", "Archive")
	srv.Append(t, "INBOX", reportFixture)
	srv.Append(t, "Archive", reportFixture)
	cfg.IndexDir = privateTestDir(t, "index")
	path := writeConfig(t, cfg)
	res := runCLI(ctx, path, "sync", "personal")
	if res.code != exitOK || !strings.Contains(res.stderr, "INBOX: 1 messages indexed") || !strings.Contains(res.stderr, "Archive: 1 messages indexed") {
		t.Fatalf("sync: %+v", res)
	}
	for _, args := range [][]string{{"sync"}, {"sync", "missing"}, {"sync", "personal", "Nonexistent"}} {
		if res := runCLI(ctx, path, args...); res.code != exitFailure {
			t.Fatalf("%v: %+v", args, res)
		}
	}
	cfg.IndexDir = ""
	if res := runCLI(ctx, writeConfig(t, cfg), "sync", "personal"); res.code != exitFailure || !strings.Contains(res.stderr, "index_dir is not configured") {
		t.Fatalf("sync without index_dir: %+v", res)
	}
}

func TestRulesRunDryRunListsCandidates(t *testing.T) {
	ctx, cfg, srv := fixtureConfig(t, "INBOX", "Archive")
	srv.Append(t, "INBOX", reportFixture)
	cfg.Rules = []config.Rule{{Name: "reports", Account: "personal", Folder: "INBOX", Match: config.RuleMatch{Subject: "weekly report"}, Action: "move", TargetFolder: "Archive"}}
	path := writeConfig(t, cfg)
	res := runCLI(ctx, path, "rules-run")
	if res.code != exitOK || !strings.Contains(res.stdout, `"applied": false`) || !strings.Contains(res.stdout, "Weekly report") {
		t.Fatalf("rules-run: %+v", res)
	}
	if res := runCLI(ctx, path, "rules-run", "--apply"); res.code != exitFailure || !strings.Contains(res.stderr, "1 rule(s) failed") {
		t.Fatalf("rules-run --apply without allow_automation: %+v", res)
	}
}

func TestServeHTTPReportsStartupFailures(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	path := writeConfig(t, config.Config{HTTPListen: busy.Addr().String(), Accounts: []config.Account{{Alias: "personal", Address: "account@example.com"}}})
	if res := runCLI(context.Background(), path, "serve-http"); res.code != exitFailure || !strings.Contains(res.stderr, "HTTP token is not configured") {
		t.Fatalf("serve-http without token: %+v", res)
	}
	if res := runCLI(context.Background(), "", "http-token"); res.code != exitOK {
		t.Fatalf("http-token: %+v", res)
	}
	if res := runCLI(context.Background(), path, "serve-http"); res.code != exitFailure || !strings.Contains(res.stderr, "HTTP server stopped") {
		t.Fatalf("serve-http on a busy address: %+v", res)
	}
}
