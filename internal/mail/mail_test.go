package mail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aleslanger/yahoo-mail-mcp/internal/config"
	"github.com/emersion/go-imap/v2/imapclient"
)

func TestMessageReferenceValidation(t *testing.T) {
	if !errors.Is(checkValidity(10, 11), ErrStale) {
		t.Fatal("stale UIDVALIDITY accepted")
	}
	if err := checkValidity(10, 0); err == nil {
		t.Fatal("missing UIDVALIDITY accepted")
	}
	if err := checkValidity(10, 10); err != nil {
		t.Fatal(err)
	}
	if err := validateUID(0); err == nil {
		t.Fatal("zero UID accepted")
	}
}

func TestIMAPConnectionCapacityRejectsBeforeDial(t *testing.T) {
	t.Setenv("YAHOO_MCP_PASSWORD_FIXTURE", "fixture-secret")
	for range maxConcurrentIMAPConnections {
		imapConnectionSlots <- struct{}{}
	}
	defer func() {
		for range maxConcurrentIMAPConnections {
			<-imapConnectionSlots
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	dialed := false
	ctx = WithDialTLS(ctx, func(string, *imapclient.Options) (*imapclient.Client, error) {
		dialed = true
		return nil, errors.New("unexpected dial")
	})
	_, _, err := connect(ctx, config.Account{Alias: "fixture", IMAPHost: "imap.test", IMAPPort: 993})
	if err == nil || err.Error() != "IMAP connection capacity unavailable" || dialed {
		t.Fatalf("capacity failure: %v, dialed=%t", err, dialed)
	}
}
func TestRejectFolderInjection(t *testing.T) {
	for _, name := range []string{"", "INBOX\r\nDELETE Other", "a\x00b"} {
		if err := folderName(name); err == nil {
			t.Fatalf("accepted folder %q", name)
		}
	}
}

func TestAdvancedSearchValidation(t *testing.T) {
	for _, filter := range []SearchFilter{
		{Since: "2026-02-30"},
		{Before: "2026/10/13"},
		{Since: "2026-10-13", Before: "2026-10-12"},
		{To: "person@example.com\r\nUID SEARCH ALL"},
	} {
		if _, err := searchCriteria(filter); err == nil {
			t.Fatalf("accepted invalid search: %+v", filter)
		}
	}
}

func TestPrivateDownloadDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "downloads")
	if err := privateDownloadDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := privateDownloadDir(dir); err == nil {
		t.Fatal("accepted accessible download directory")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := privateDownloadDir(link); err == nil {
		t.Fatal("accepted symlink download directory")
	}
}
