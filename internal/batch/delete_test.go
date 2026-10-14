package batch

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ales/yahoo-mail-mcp/internal/config"
	"github.com/ales/yahoo-mail-mcp/internal/mail"
)

func deleteOps(t *testing.T, deleted *[]uint32) mailOperations {
	t.Helper()
	return mailOperations{
		trashFolder: func(context.Context, config.Account) (string, error) { return "Trash", nil },
		getRange: func(_ context.Context, _ config.Account, folder string, uid, validity uint32, _ int64, _ int) (mail.Body, error) {
			return mail.Body{Message: mail.Message{UID: uid, UIDValidity: validity, Subject: "preview"}}, nil
		},
		permanentlyDelete: func(_ context.Context, _ config.Account, folder string, uid, validity uint32) error {
			if folder != "Trash" || validity != 7 {
				t.Fatalf("wrong delete arguments: %s %d %d", folder, uid, validity)
			}
			*deleted = append(*deleted, uid)
			return nil
		},
	}
}

func TestBatchDeleteValidation(t *testing.T) {
	in := Input{Account: "personal", Folder: "Trash", UIDValidity: 7, UIDs: []uint32{5}, Action: "delete"}
	if err := validate(in); err != nil {
		t.Fatalf("delete plan rejected: %v", err)
	}
	in.Target = "Archive"
	if err := validate(in); err == nil {
		t.Fatal("delete plan accepted a target folder")
	}
}

func TestBatchDeleteRequiresDeletePermission(t *testing.T) {
	var deleted []uint32
	dir := filepath.Join(t.TempDir(), "journal")
	a := config.Account{Alias: "personal", AllowOrganize: true}
	in := Input{Account: a.Alias, Folder: "Trash", UIDValidity: 7, UIDs: []uint32{5}, Action: "delete"}
	if _, err := prepare(context.Background(), a, dir, in, deleteOps(t, &deleted)); err == nil || err.Error() != "delete permission is disabled for account" {
		t.Fatalf("delete without allow_delete: %v", err)
	}
}

func TestBatchDeleteOnlyInTrash(t *testing.T) {
	var deleted []uint32
	dir := filepath.Join(t.TempDir(), "journal")
	a := config.Account{Alias: "personal", AllowDelete: true}
	in := Input{Account: a.Alias, Folder: "INBOX", UIDValidity: 7, UIDs: []uint32{5}, Action: "delete"}
	if _, err := prepare(context.Background(), a, dir, in, deleteOps(t, &deleted)); err == nil || err.Error() != "permanent deletion is allowed only in the Trash folder" {
		t.Fatalf("delete outside Trash: %v", err)
	}
}

func TestBatchDeleteExecutesOnceAfterConfirmation(t *testing.T) {
	var deleted []uint32
	ops := deleteOps(t, &deleted)
	dir := filepath.Join(t.TempDir(), "journal")
	a := config.Account{Alias: "personal", AllowDelete: true}
	in := Input{Account: a.Alias, Folder: "Trash", UIDValidity: 7, UIDs: []uint32{5, 6}, Action: "delete"}
	preview, err := prepare(context.Background(), a, dir, in, ops)
	if err != nil || len(preview.Messages) != 2 {
		t.Fatalf("delete preview: %+v, %v", preview, err)
	}
	if _, err := execute(context.Background(), a, dir, preview.Token, false, ops); err == nil || len(deleted) != 0 {
		t.Fatalf("delete ran without confirmation: %v, deleted=%v", err, deleted)
	}
	withoutPermission := config.Account{Alias: "personal", AllowOrganize: true}
	if _, err := execute(context.Background(), withoutPermission, dir, preview.Token, true, ops); err == nil || len(deleted) != 0 {
		t.Fatalf("delete ran after permission was revoked: %v, deleted=%v", err, deleted)
	}
	out, err := execute(context.Background(), a, dir, preview.Token, true, ops)
	if err != nil || out.Status != "completed" || !reflect.DeepEqual(deleted, []uint32{5, 6}) {
		t.Fatalf("delete execution: %+v, deleted=%v, err=%v", out, deleted, err)
	}
	if _, err := execute(context.Background(), a, dir, preview.Token, true, ops); err != nil || len(deleted) != 2 {
		t.Fatalf("delete replay repeated work: %v, deleted=%v", err, deleted)
	}
}
