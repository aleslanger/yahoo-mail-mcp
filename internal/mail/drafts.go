package mail

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/ales/yahoo-mail-mcp/internal/config"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type DraftReference struct {
	Folder      string `json:"folder"`
	UID         uint32 `json:"uid"`
	UIDValidity uint32 `json:"uid_validity"`
}

func draftFolder(c *imapclient.Client) (string, error) {
	cmd := c.List("", "*", nil)
	var special, fallback []string
	for item := cmd.Next(); item != nil; item = cmd.Next() {
		for _, attr := range item.Attrs {
			if attr == imap.MailboxAttrDrafts {
				special = append(special, item.Mailbox)
			}
		}
		name := item.Mailbox
		if item.Delim != 0 {
			parts := strings.Split(name, string(item.Delim))
			name = parts[len(parts)-1]
		}
		if strings.EqualFold(name, "Drafts") || strings.EqualFold(name, "Draft") {
			fallback = append(fallback, item.Mailbox)
		}
	}
	if err := cmd.Close(); err != nil {
		return "", errors.New("draft folder discovery failed")
	}
	if len(special) == 1 {
		return special[0], nil
	}
	if len(special) > 1 || len(fallback) != 1 {
		return "", errors.New("draft folder is missing or ambiguous")
	}
	return fallback[0], nil
}

func appendDraft(c *imapclient.Client, folder string, raw []byte) (DraftReference, error) {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return DraftReference{}, errors.New("draft MIME size must be 1..1048576 bytes")
	}
	cmd := c.Append(folder, int64(len(raw)), &imap.AppendOptions{Flags: []imap.Flag{imap.FlagDraft}})
	if _, err := cmd.Write(raw); err != nil {
		_ = cmd.Close()
		return DraftReference{}, errors.New("draft append outcome uncertain; inspect Drafts before retrying")
	}
	if err := cmd.Close(); err != nil {
		return DraftReference{}, errors.New("draft append outcome uncertain; inspect Drafts before retrying")
	}
	data, err := cmd.Wait()
	if err != nil || data.UID == 0 || data.UIDValidity == 0 {
		return DraftReference{}, errors.New("draft append outcome uncertain or server omitted APPENDUID; inspect Drafts before retrying")
	}
	ref := DraftReference{Folder: folder, UID: uint32(data.UID), UIDValidity: data.UIDValidity}
	sel, err := selectFolder(c, folder, true)
	if err != nil || sel.UIDValidity != ref.UIDValidity {
		return ref, errors.New("draft saved but UIDVALIDITY postcondition is uncertain; inspect Drafts")
	}
	found, err := c.Fetch(imap.UIDSetNum(data.UID), &imap.FetchOptions{UID: true}).Collect()
	if err != nil || len(found) != 1 || found[0].UID != data.UID {
		return ref, errors.New("draft saved but destination postcondition is uncertain; inspect Drafts")
	}
	return ref, nil
}

func CreateDraft(ctx context.Context, a config.Account, raw []byte) (DraftReference, error) {
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		return DraftReference{}, err
	}
	defer closeFn()
	if !c.Caps().Has(imap.CapUIDPlus) && !c.Caps().Has(imap.CapIMAP4rev2) {
		return DraftReference{}, errors.New("draft creation requires UIDPLUS or IMAP4rev2 for a stable UID")
	}
	folder, err := draftFolder(c)
	if err != nil {
		return DraftReference{}, err
	}
	return appendDraft(c, folder, raw)
}

func ListDrafts(ctx context.Context, a config.Account, before uint32, limit int) (Page, error) {
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		return Page{}, err
	}
	folder, err := draftFolder(c)
	closeFn()
	if err != nil {
		return Page{}, err
	}
	return List(ctx, a, folder, before, limit)
}

func GetDraft(ctx context.Context, a config.Account, uid, validity uint32) (ParsedMessage, error) {
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		return ParsedMessage{}, err
	}
	folder, err := draftFolder(c)
	closeFn()
	if err != nil {
		return ParsedMessage{}, err
	}
	return ReadParsed(ctx, a, folder, uid, validity)
}

// LoadDraftRaw returns the exact stored MIME bytes for a bounded draft. Bcc is
// not recoverable from MIME headers and must be supplied separately at send time.
func LoadDraftRaw(ctx context.Context, a config.Account, uid, validity uint32) ([]byte, error) {
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		return nil, err
	}
	folder, err := draftFolder(c)
	closeFn()
	if err != nil {
		return nil, err
	}
	first, err := GetRange(ctx, a, folder, uid, validity, 0, 262144)
	if err != nil {
		return nil, err
	}
	if first.TotalSize < 1 || first.TotalSize > 1<<20 {
		return nil, errors.New("draft exceeds 1 MiB raw send limit")
	}
	raw, err := base64.StdEncoding.DecodeString(first.RawBase64)
	if err != nil {
		return nil, errors.New("invalid draft MIME encoding")
	}
	for first.Truncated {
		first, err = GetRange(ctx, a, folder, uid, validity, first.NextOffset, 262144)
		if err != nil {
			return nil, err
		}
		chunk, err := base64.StdEncoding.DecodeString(first.RawBase64)
		if err != nil || len(chunk) == 0 {
			return nil, errors.New("draft MIME continuation failed")
		}
		raw = append(raw, chunk...)
	}
	if int64(len(raw)) != first.TotalSize {
		return nil, errors.New("draft MIME size changed during retrieval")
	}
	return raw, nil
}

func deleteDraft(c *imapclient.Client, folder string, uid, validity uint32) error {
	return expungeUID(c, folder, uid, validity, "draft", "Drafts")
}

func DeleteDraft(ctx context.Context, a config.Account, uid, validity uint32) error {
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		return err
	}
	defer closeFn()
	folder, err := draftFolder(c)
	if err != nil {
		return err
	}
	return deleteDraft(c, folder, uid, validity)
}

func UpdateDraft(ctx context.Context, a config.Account, uid, validity uint32, raw []byte) (DraftReference, error) {
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		return DraftReference{}, err
	}
	defer closeFn()
	if !c.Caps().Has(imap.CapUIDPlus) && !c.Caps().Has(imap.CapIMAP4rev2) {
		return DraftReference{}, errors.New("draft update requires UIDPLUS or IMAP4rev2")
	}
	folder, err := draftFolder(c)
	if err != nil {
		return DraftReference{}, err
	}
	sel, err := selectFolder(c, folder, true)
	if err != nil {
		return DraftReference{}, err
	}
	if err := checkValidity(sel.UIDValidity, validity); err != nil {
		return DraftReference{}, err
	}
	found, err := c.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{UID: true}).Collect()
	if err != nil || len(found) != 1 || found[0].UID != imap.UID(uid) {
		return DraftReference{}, errors.New("draft not found")
	}
	newRef, err := appendDraft(c, folder, raw)
	if err != nil {
		return DraftReference{}, err
	}
	if newRef.UIDValidity != validity {
		return newRef, errors.New("new draft saved but UIDVALIDITY changed; inspect both drafts")
	}
	if err := deleteDraft(c, folder, uid, validity); err != nil {
		return newRef, errors.New("new draft saved but old draft removal is uncertain; inspect both drafts")
	}
	return newRef, nil
}
