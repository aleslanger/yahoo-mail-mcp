package mail

import (
	"testing"

	"github.com/emersion/go-imap/v2"
)

func TestBuildFolderTree(t *testing.T) {
	folders := []Folder{
		{Name: "Projects/2026/Plans", Delimiter: "/"},
		{Name: "INBOX", Delimiter: "/"},
		{Name: "Projects/2026", Delimiter: "/", Attributes: []imap.MailboxAttr{imap.MailboxAttrSubscribed}},
		{Name: "Projects/2025", Delimiter: "/"},
	}
	roots := BuildFolderTree(folders)
	if len(roots) != 2 || roots[0].Path != "INBOX" || roots[1].Path != "Projects" {
		t.Fatalf("incorrect roots: %+v", roots)
	}
	project := roots[1]
	if len(project.Children) != 2 || project.Children[0].Path != "Projects/2025" || project.Children[1].Path != "Projects/2026" {
		t.Fatalf("incorrect project tree: %+v", project)
	}
	if len(project.Children[1].Children) != 1 || project.Children[1].Children[0].Path != "Projects/2026/Plans" || len(project.Children[1].Attributes) != 1 {
		t.Fatalf("missing nested folder or attributes: %+v", project.Children[1])
	}
	flat := FlattenFolderTree(roots)
	if len(flat) != 5 || flat[4].Path != "Projects/2026/Plans" || flat[4].Parent != "Projects/2026" || flat[4].Depth != 2 {
		t.Fatalf("incorrect flat hierarchy: %+v", flat)
	}
}
