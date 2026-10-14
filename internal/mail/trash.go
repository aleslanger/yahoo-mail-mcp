package mail

import (
	"context"
	"errors"
	"strings"

	"github.com/aleslanger/yahoo-mail-mcp/internal/config"
	"github.com/emersion/go-imap/v2"
)

func resolveTrashFolder(folders []Folder, override string) (string, error) {
	if override != "" {
		for _, folder := range folders {
			if folder.Name == override && selectableTrash(folder) {
				return override, nil
			}
		}
		return "", errors.New("configured trash_folder is unavailable, unselectable or a protected system folder")
	}
	special := []string{}
	fallback := []string{}
	for _, folder := range folders {
		if !selectableTrash(folder) {
			continue
		}
		for _, attr := range folder.Attributes {
			if attr == imap.MailboxAttrTrash {
				special = append(special, folder.Name)
			}
		}
		base := folder.Name
		if folder.Delimiter != "" {
			parts := strings.Split(base, folder.Delimiter)
			base = parts[len(parts)-1]
		}
		if strings.EqualFold(base, "Trash") || strings.EqualFold(base, "Deleted Items") || strings.EqualFold(base, "Deleted Messages") {
			fallback = append(fallback, folder.Name)
		}
	}
	if len(special) == 1 {
		return special[0], nil
	}
	if len(special) > 1 || len(fallback) != 1 {
		return "", errors.New("Trash folder is missing or ambiguous; configure trash_folder explicitly")
	}
	return fallback[0], nil
}

func selectableTrash(folder Folder) bool {
	if strings.EqualFold(folder.Name, "INBOX") {
		return false
	}
	for _, attr := range folder.Attributes {
		switch attr {
		case imap.MailboxAttrNoSelect, imap.MailboxAttrSent, imap.MailboxAttrDrafts, imap.MailboxAttrJunk, imap.MailboxAttrArchive, imap.MailboxAttrAll:
			return false
		}
	}
	return true
}

func MoveToTrash(ctx context.Context, a config.Account, source string, uid, validity uint32) (any, error) {
	if err := validateUID(uid); err != nil {
		return nil, err
	}
	folders, err := ListFolders(ctx, a)
	if err != nil {
		return nil, err
	}
	target, err := resolveTrashFolder(folders, a.TrashFolder)
	if err != nil {
		return nil, err
	}
	if source == target {
		return nil, errors.New("message is already in Trash")
	}
	return CopyOrMove(ctx, a, source, target, uid, validity, true)
}
