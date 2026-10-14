//go:build linux

package send

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/ales/yahoo-mail-mcp/internal/config"
)

func TestJournalFileSizeFailureLeavesReplayUncertain(t *testing.T) {
	account := config.Account{Alias: "fixture", Address: "fixture@example.com", SMTPHost: "smtp.test", SMTPPort: 465, AllowSend: true}
	input := Input{IdempotencyKey: "disk-failure-123", To: []string{"recipient@example.com"}, Subject: "disk failure", Text: "Synthetic test message"}
	if stage := os.Getenv("YAHOO_MCP_DISK_CHILD_STAGE"); stage != "" {
		p, err := prepare(account.Address, input)
		if err != nil {
			os.Exit(18)
		}
		j, err := newJournal(os.Getenv("YAHOO_MCP_DISK_JOURNAL_DIR"), account.Alias, input.IdempotencyKey)
		if err != nil {
			os.Exit(18)
		}
		if stage == "confirm" {
			if _, existed, err := j.begin(p.digest, p.messageID); err != nil || existed {
				os.Exit(18)
			}
		}
		signal.Ignore(syscall.SIGXFSZ)
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 0, Max: 0}); err != nil {
			os.Exit(18)
		}
		switch stage {
		case "begin":
			_, _, err = j.begin(p.digest, p.messageID)
		case "confirm":
			err = j.confirm(p.digest, p.messageID)
		default:
			os.Exit(18)
		}
		if err == nil {
			os.Exit(18)
		}
		os.Exit(17)
	}
	for _, stage := range []string{"begin", "confirm"} {
		t.Run(stage, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "journal")
			child := exec.Command(os.Args[0], "-test.run=^TestJournalFileSizeFailureLeavesReplayUncertain$")
			child.Env = append(os.Environ(), "YAHOO_MCP_DISK_CHILD_STAGE="+stage, "YAHOO_MCP_DISK_JOURNAL_DIR="+dir)
			err := child.Run()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 17 {
				t.Fatalf("child did not hit file-size limit: %v", err)
			}
			var dials atomic.Int32
			ctx := context.WithValue(context.Background(), dialKey{}, dialFunc(func(context.Context, string, *tls.Config) (net.Conn, error) {
				dials.Add(1)
				return nil, errors.New("unexpected SMTP dial")
			}))
			t.Setenv("YAHOO_MCP_PASSWORD_FIXTURE", "fixture-secret")
			receipt, err := Send(ctx, account, dir, input)
			if err != nil || receipt.Status != "uncertain" || dials.Load() != 0 {
				t.Fatalf("replay after file-size failure: status=%s err=%v dials=%d", receipt.Status, err, dials.Load())
			}
		})
	}
}
