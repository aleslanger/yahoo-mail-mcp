package send

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/ales/yahoo-mail-mcp/internal/config"
)

func TestJournalSurvivesProcessExit(t *testing.T) {
	account := config.Account{Alias: "fixture", Address: "fixture@example.com", SMTPHost: "smtp.test", SMTPPort: 465, AllowSend: true}
	input := Input{IdempotencyKey: "process-exit-123", To: []string{"recipient@example.com"}, Subject: "process exit", Text: "Synthetic test message"}
	if stage := os.Getenv("YAHOO_MCP_CRASH_CHILD_STAGE"); stage != "" {
		p, err := prepare(account.Address, input)
		if err != nil {
			os.Exit(18)
		}
		j, err := newJournal(os.Getenv("YAHOO_MCP_CRASH_JOURNAL_DIR"), account.Alias, input.IdempotencyKey)
		if err != nil {
			os.Exit(18)
		}
		if _, existed, err := j.begin(p.digest, p.messageID); err != nil || existed {
			os.Exit(18)
		}
		if stage == "confirmed" {
			if err := j.confirm(p.digest, p.messageID); err != nil {
				os.Exit(18)
			}
		}
		os.Exit(17)
	}
	for _, stage := range []string{"uncertain", "confirmed"} {
		t.Run(stage, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "journal")
			child := exec.Command(os.Args[0], "-test.run=^TestJournalSurvivesProcessExit$")
			child.Env = append(os.Environ(), "YAHOO_MCP_CRASH_CHILD_STAGE="+stage, "YAHOO_MCP_CRASH_JOURNAL_DIR="+dir)
			err := child.Run()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 17 {
				t.Fatalf("child did not exit at the journal boundary: %v", err)
			}
			var dials atomic.Int32
			ctx := context.WithValue(context.Background(), dialKey{}, dialFunc(func(context.Context, string, *tls.Config) (net.Conn, error) {
				dials.Add(1)
				return nil, errors.New("unexpected SMTP dial")
			}))
			t.Setenv("YAHOO_MCP_PASSWORD_FIXTURE", "fixture-secret")
			receipt, err := Send(ctx, account, dir, input)
			if err != nil || receipt.Status != stage || dials.Load() != 0 {
				t.Fatalf("replay after process exit: status=%s err=%v dials=%d", receipt.Status, err, dials.Load())
			}
		})
	}
}
