package mail

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/aleslanger/yahoo-mail-mcp/internal/config"
	"github.com/emersion/go-imap/v2"
)

// fetchRawToTemp keeps large MIME out of MCP responses and caps temporary data.
// The caller owns the returned file and must close and remove it.
func fetchRawToTemp(ctx context.Context, a config.Account, folder string, uid, validity uint32, dir string) (*os.File, Message, error) {
	if err := validateUID(uid); err != nil {
		return nil, Message{}, err
	}
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		return nil, Message{}, err
	}
	defer closeFn()
	sel, err := selectFolder(c, folder, true)
	if err != nil {
		return nil, Message{}, err
	}
	if err := checkValidity(sel.UIDValidity, validity); err != nil {
		return nil, Message{}, err
	}
	set := imap.UIDSetNum(imap.UID(uid))
	meta, err := c.Fetch(set, &imap.FetchOptions{UID: true, Envelope: true, Flags: true, RFC822Size: true}).Collect()
	if err != nil || len(meta) != 1 || meta[0].UID != imap.UID(uid) {
		return nil, Message{}, errors.New("message not found")
	}
	if meta[0].RFC822Size < 1 || meta[0].RFC822Size > maxRawSave {
		return nil, Message{}, errors.New("message exceeds 16 MiB MIME processing limit")
	}
	rawFile, err := os.CreateTemp(dir, ".mime-*")
	if err != nil {
		return nil, Message{}, errors.New("cannot create private temporary MIME file")
	}
	keep := false
	defer func() {
		if !keep {
			_ = rawFile.Close()
			_ = os.Remove(rawFile.Name())
		}
	}()
	for offset := int64(0); offset < meta[0].RFC822Size; {
		if err := ctx.Err(); err != nil {
			return nil, Message{}, err
		}
		size := int64(262144)
		if remaining := meta[0].RFC822Size - offset; remaining < size {
			size = remaining
		}
		section := &imap.FetchItemBodySection{Peek: true, Partial: &imap.SectionPartial{Offset: offset, Size: size}}
		parts, err := c.Fetch(set, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}}).Collect()
		if err != nil || len(parts) != 1 || parts[0].UID != imap.UID(uid) || len(parts[0].BodySection) != 1 || int64(len(parts[0].BodySection[0].Bytes)) != size {
			return nil, Message{}, errors.New("message changed or MIME range retrieval failed")
		}
		if _, err := rawFile.Write(parts[0].BodySection[0].Bytes); err != nil {
			return nil, Message{}, errors.New("cannot save temporary MIME data")
		}
		offset += size
	}
	if _, err := rawFile.Seek(0, io.SeekStart); err != nil {
		return nil, Message{}, errors.New("cannot read temporary MIME data")
	}
	keep = true
	return rawFile, toMessage(meta[0], sel.UIDValidity), nil
}
