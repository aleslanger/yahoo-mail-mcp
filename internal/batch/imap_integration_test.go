package batch

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aleslanger/yahoo-mail-mcp/internal/config"
	"github.com/aleslanger/yahoo-mail-mcp/internal/imaptest"
	"github.com/aleslanger/yahoo-mail-mcp/internal/mail"
	"github.com/emersion/go-imap/v2"
)

func batchIMAP(t *testing.T, withMove bool) (context.Context, config.Account, func(string) uint32) {
	t.Helper()
	srv := imaptest.New(t, imaptest.Options{Alias: "batch", Address: "batch@example.com", Password: "batch-secret", Folders: []string{"INBOX", "Archive", "Trash"}, Move: withMove})
	a := srv.Account
	a.AllowOrganize = true
	appendMessage := func(subject string) uint32 {
		return srv.Append(t, "INBOX", "From: sender@example.com\r\nTo: batch@example.com\r\nSubject: "+subject+"\r\n\r\nBody\r\n")
	}
	return mail.WithDialTLS(context.Background(), srv.Dial), a, appendMessage
}

func TestBatchRealIMAPMoveConcurrentChange(t *testing.T) {
	ctx, a, appendMessage := batchIMAP(t, true)
	first, second := appendMessage("first"), appendMessage("second")
	inbox, err := mail.Status(ctx, a, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "journal")
	preview, err := Prepare(ctx, a, dir, Input{Account: a.Alias, Folder: "INBOX", UIDValidity: inbox.UIDValidity, UIDs: []uint32{first, second}, Action: "move", Target: "Archive"})
	if err != nil || len(preview.Messages) != 2 || preview.Messages[0].UID != first {
		t.Fatalf("preview: %+v, %v", preview, err)
	}
	if _, err := Execute(ctx, a, dir, preview.Token, false); err == nil {
		t.Fatal("execution without confirmation")
	}
	if _, err := mail.CopyOrMove(ctx, a, "INBOX", "Archive", first, inbox.UIDValidity, true); err != nil {
		t.Fatalf("external move: %v", err)
	}
	out, err := Execute(ctx, a, dir, preview.Token, true)
	if err != nil || out.Status != "partial_or_uncertain" || out.Items[0].Status != "uncertain" || out.Items[1].Status != "completed" {
		t.Fatalf("execution after mailbox change: %+v, %v", out, err)
	}
	beforeReplay, err := mail.Status(ctx, a, "Archive")
	if err != nil || beforeReplay.Messages != 2 {
		t.Fatalf("archive after execution: %+v, %v", beforeReplay, err)
	}
	replayed, err := Execute(ctx, a, dir, preview.Token, true)
	afterReplay, statusErr := mail.Status(ctx, a, "Archive")
	if err != nil || statusErr != nil || replayed.Items[0].Status != "uncertain" || afterReplay.Messages != beforeReplay.Messages {
		t.Fatalf("replay changed mailbox: %+v, %+v, %v, %v", replayed, afterReplay, err, statusErr)
	}
}

func TestBatchRealIMAPFlagAndMoveGate(t *testing.T) {
	ctx, a, appendMessage := batchIMAP(t, false)
	uid := appendMessage("flag")
	inbox, err := mail.Status(ctx, a, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "journal")
	if _, err := Prepare(ctx, a, dir, Input{Account: a.Alias, Folder: "INBOX", UIDValidity: inbox.UIDValidity, UIDs: []uint32{uid}, Action: "move", Target: "Archive"}); err == nil {
		t.Fatal("MOVE preview accepted without capability")
	}
	preview, err := Prepare(ctx, a, dir, Input{Account: a.Alias, Folder: "INBOX", UIDValidity: inbox.UIDValidity, UIDs: []uint32{uid}, Action: "mark_read"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Execute(ctx, a, dir, preview.Token, true)
	status, statusErr := mail.Status(ctx, a, "INBOX")
	if err != nil || statusErr != nil || out.Status != "completed" || status.Unseen != 0 {
		t.Fatalf("mark-read postcondition: %+v, %+v, %v, %v", out, status, err, statusErr)
	}
}

func TestBatchRealIMAPPermanentDeleteOnlyExpungesPlannedUID(t *testing.T) {
	ctx, a, appendMessage := batchIMAP(t, true)
	a.AllowDelete = true
	inbox, err := mail.Status(ctx, a, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	for _, subject := range []string{"keep", "delete", "marked"} {
		uid := appendMessage(subject)
		if _, err := mail.CopyOrMove(ctx, a, "INBOX", "Trash", uid, inbox.UIDValidity, true); err != nil {
			t.Fatalf("move %s to Trash: %v", subject, err)
		}
	}
	inboxUID := appendMessage("inbox")
	trashUIDs := func() map[string]uint32 {
		t.Helper()
		page, err := mail.List(ctx, a, "Trash", 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		bySubject := map[string]uint32{}
		for _, message := range page.Messages {
			bySubject[message.Subject] = message.UID
		}
		return bySubject
	}
	before := trashUIDs()
	trash, err := mail.Status(ctx, a, "Trash")
	if err != nil {
		t.Fatal(err)
	}
	if err := mail.ChangeFlag(ctx, a, "Trash", before["marked"], trash.UIDValidity, imap.FlagDeleted, true); err != nil {
		t.Fatalf("mark unrelated Trash message deleted: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "journal")
	if _, err := Prepare(ctx, a, dir, Input{Account: a.Alias, Folder: "INBOX", UIDValidity: inbox.UIDValidity, UIDs: []uint32{inboxUID}, Action: "delete"}); err == nil {
		t.Fatal("permanent delete preview accepted INBOX")
	}
	preview, err := Prepare(ctx, a, dir, Input{Account: a.Alias, Folder: "Trash", UIDValidity: trash.UIDValidity, UIDs: []uint32{before["delete"]}, Action: "delete"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Execute(ctx, a, dir, preview.Token, true)
	if err != nil || out.Status != "completed" {
		t.Fatalf("permanent delete: %+v, %v", out, err)
	}
	after := trashUIDs()
	if _, ok := after["delete"]; ok {
		t.Fatal("planned message still in Trash")
	}
	if after["keep"] != before["keep"] || after["marked"] != before["marked"] {
		t.Fatalf("unplanned Trash messages changed: before=%v after=%v", before, after)
	}
}
