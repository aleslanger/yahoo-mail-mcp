package mail

import (
	"testing"

	"github.com/emersion/go-imap/v2"
)

func TestResolveTrashFolder(t *testing.T) {
	folders := []Folder{{Name: "Trash"}, {Name: "Archive"}}
	if name, err := resolveTrashFolder(folders, ""); err != nil || name != "Trash" {
		t.Fatalf("fallback trash: %q, %v", name, err)
	}
	folders = append(folders, Folder{Name: "Deleted Items"})
	if _, err := resolveTrashFolder(folders, ""); err == nil {
		t.Fatal("ambiguous fallback accepted")
	}
	if name, err := resolveTrashFolder(folders, "Deleted Items"); err != nil || name != "Deleted Items" {
		t.Fatalf("configured trash: %q, %v", name, err)
	}
	folders = append(folders, Folder{Name: "Special", Attributes: []imap.MailboxAttr{imap.MailboxAttrTrash}})
	if name, err := resolveTrashFolder(folders, ""); err != nil || name != "Special" {
		t.Fatalf("special-use trash: %q, %v", name, err)
	}
	folders = append(folders, Folder{Name: "Second", Attributes: []imap.MailboxAttr{imap.MailboxAttrTrash}})
	if _, err := resolveTrashFolder(folders, ""); err == nil {
		t.Fatal("ambiguous special-use Trash accepted")
	}
	if _, err := resolveTrashFolder([]Folder{{Name: "Trash", Attributes: []imap.MailboxAttr{imap.MailboxAttrNoSelect}}}, "Trash"); err == nil {
		t.Fatal("unselectable Trash accepted")
	}
	if _, err := resolveTrashFolder([]Folder{{Name: "INBOX"}}, "INBOX"); err == nil {
		t.Fatal("INBOX accepted as Trash override")
	}
	if _, err := resolveTrashFolder([]Folder{{Name: "Sent", Attributes: []imap.MailboxAttr{imap.MailboxAttrSent}}}, "Sent"); err == nil {
		t.Fatal("Sent folder accepted as Trash override")
	}
}
