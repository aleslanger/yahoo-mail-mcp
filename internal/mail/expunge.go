package mail

import (
	"context"
	"errors"
	"fmt"

	"github.com/aleslanger/yahoo-mail-mcp/internal/config"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// TrashFolder resolves the account's unique Trash folder.
func TrashFolder(ctx context.Context, a config.Account) (string, error) {
	folders, err := ListFolders(ctx, a)
	if err != nil {
		return "", err
	}
	return resolveTrashFolder(folders, a.TrashFolder)
}

// PermanentlyDelete expunges one message, but only from the account's Trash folder.
func PermanentlyDelete(ctx context.Context, a config.Account, folder string, uid, validity uint32) error {
	if !a.AllowDelete {
		return errors.New("delete permission is disabled for account")
	}
	if err := validateUID(uid); err != nil {
		return err
	}
	trash, err := TrashFolder(ctx, a)
	if err != nil {
		return err
	}
	if folder != trash {
		return errors.New("permanent deletion is allowed only in the Trash folder")
	}
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		return err
	}
	defer closeFn()
	return expungeUID(c, folder, uid, validity, "message", "Trash")
}

// expungeUID removes exactly one UID with UID EXPUNGE, so other messages already
// marked \Deleted in the folder are never expunged, then verifies its absence.
func expungeUID(c *imapclient.Client, folder string, uid, validity uint32, noun, place string) error {
	if !c.Caps().Has(imap.CapUIDPlus) && !c.Caps().Has(imap.CapIMAP4rev2) {
		return fmt.Errorf("%s deletion requires UID EXPUNGE", noun)
	}
	sel, err := selectFolder(c, folder, false)
	if err != nil {
		return err
	}
	if err := checkValidity(sel.UIDValidity, validity); err != nil {
		return err
	}
	if err := validateUID(uid); err != nil {
		return err
	}
	set := imap.UIDSetNum(imap.UID(uid))
	found, err := c.Fetch(set, &imap.FetchOptions{UID: true}).Collect()
	if err != nil || len(found) != 1 || found[0].UID != imap.UID(uid) {
		return fmt.Errorf("%s not found", noun)
	}
	if err := c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}, Silent: true}, nil).Close(); err != nil {
		return fmt.Errorf("%s deletion outcome uncertain; inspect %s", noun, place)
	}
	if err := c.UIDExpunge(set).Close(); err != nil {
		return fmt.Errorf("%s deletion outcome uncertain; inspect %s", noun, place)
	}
	after, err := c.Fetch(set, &imap.FetchOptions{UID: true}).Collect()
	if err != nil || len(after) != 0 {
		return fmt.Errorf("%s deletion postcondition uncertain; inspect %s", noun, place)
	}
	return nil
}
