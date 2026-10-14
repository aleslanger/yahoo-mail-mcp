package mail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/ales/yahoo-mail-mcp/internal/config"
	msgmail "github.com/emersion/go-message/mail"
)

const maxRawSave = 16 << 20
const maxAttachmentSave = 10 << 20

type SavedAttachment struct {
	Metadata AttachmentInfo `json:"metadata"`
	Path     string         `json:"path"`
}

func privateDownloadDir(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("download_dir must be an absolute private directory")
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return errors.New("cannot create download_dir")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("download_dir must be a private directory with mode 0700")
	}
	return nil
}

func SaveAttachment(ctx context.Context, a config.Account, folder string, uid, validity uint32, index int, downloadDir string) (SavedAttachment, error) {
	if index < 1 || index > 100 {
		return SavedAttachment{}, errors.New("attachment index must be 1..100")
	}
	if err := validateUID(uid); err != nil {
		return SavedAttachment{}, err
	}
	if err := privateDownloadDir(downloadDir); err != nil {
		return SavedAttachment{}, err
	}
	rawFile, _, err := fetchRawToTemp(ctx, a, folder, uid, validity, downloadDir)
	if err != nil {
		return SavedAttachment{}, err
	}
	defer os.Remove(rawFile.Name())
	defer rawFile.Close()
	reader, err := msgmail.CreateReader(rawFile)
	if err != nil && reader == nil {
		return SavedAttachment{}, errors.New("malformed MIME message")
	}
	defer reader.Close()
	for current := 1; current <= 100; current++ {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil && part == nil {
			return SavedAttachment{}, errors.New("malformed MIME part")
		}
		if current != index {
			if _, err := io.Copy(io.Discard, part.Body); err != nil {
				return SavedAttachment{}, errors.New("MIME part read failed")
			}
			continue
		}
		header, ok := part.Header.(*msgmail.AttachmentHeader)
		if !ok {
			return SavedAttachment{}, errors.New("selected MIME part is not an attachment")
		}
		output, err := os.CreateTemp(downloadDir, "attachment-*")
		if err != nil {
			return SavedAttachment{}, errors.New("cannot create attachment file")
		}
		name := output.Name()
		keep := false
		defer func() {
			_ = output.Close()
			if !keep {
				_ = os.Remove(name)
			}
		}()
		n, err := io.Copy(output, io.LimitReader(part.Body, maxAttachmentSave+1))
		if err != nil || n > maxAttachmentSave {
			return SavedAttachment{}, errors.New("attachment read failed or exceeds 10 MiB")
		}
		if err := output.Sync(); err != nil {
			return SavedAttachment{}, errors.New("attachment file sync failed")
		}
		if err := output.Close(); err != nil {
			return SavedAttachment{}, errors.New("attachment file close failed")
		}
		filename, _ := header.Filename()
		if filename == "" {
			filename = fmt.Sprintf("part-%d", index)
		}
		typ, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if typ == "" {
			typ = "application/octet-stream"
		}
		disp, _, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		keep = true
		return SavedAttachment{Metadata: AttachmentInfo{Index: index, Filename: filename, ContentType: typ, ContentID: part.Header.Get("Content-ID"), Size: n, Inline: strings.EqualFold(disp, "inline")}, Path: name}, nil
	}
	return SavedAttachment{}, errors.New("attachment index not found")
}
