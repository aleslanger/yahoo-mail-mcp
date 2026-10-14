package send

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/mail"
	"strings"

	"github.com/ales/yahoo-mail-mcp/internal/config"
	msgmail "github.com/emersion/go-message/mail"
)

func prepareStoredDraft(from string, raw []byte, key string, bcc []string) (prepared, string, error) {
	if !keyPattern.MatchString(key) {
		return prepared{}, "", errors.New("invalid idempotency_key")
	}
	if len(raw) == 0 || len(raw) > 1<<20 || bytes.IndexByte(raw, 0) >= 0 || (!bytes.Contains(raw, []byte("\r\n\r\n")) && !bytes.Contains(raw, []byte("\n\n"))) {
		return prepared{}, "", errors.New("invalid or oversized stored draft MIME")
	}
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return prepared{}, "", errors.New("malformed stored draft headers")
	}
	countHeader := func(name string) int {
		count := 0
		for key, values := range message.Header {
			if strings.EqualFold(key, name) {
				count += len(values)
			}
		}
		return count
	}
	if countHeader("From") != 1 || countHeader("To") != 1 || countHeader("Message-ID") != 1 || countHeader("Bcc") != 0 || countHeader("Cc") > 1 || countHeader("Subject") > 1 {
		return prepared{}, "", errors.New("stored draft has duplicate or unsafe address headers")
	}
	address := func(value string) (string, error) {
		parsed, err := mail.ParseAddress(value)
		if err != nil {
			return "", errors.New("invalid stored draft address")
		}
		return parseAddress(parsed.Address)
	}
	fromHeader, err := address(message.Header.Get("From"))
	if err != nil || !strings.EqualFold(fromHeader, from) {
		return prepared{}, "", errors.New("stored draft sender differs from configured account")
	}
	if message.Header.Get("Bcc") != "" {
		return prepared{}, "", errors.New("stored draft contains an unsafe Bcc header")
	}
	parseRecipients := func(value string) ([]string, error) {
		if value == "" {
			return nil, nil
		}
		parsed, err := mail.ParseAddressList(value)
		if err != nil {
			return nil, errors.New("invalid stored draft recipients")
		}
		out := make([]string, 0, len(parsed))
		for _, recipient := range parsed {
			addr, err := parseAddress(recipient.Address)
			if err != nil {
				return nil, errors.New("invalid stored draft recipients")
			}
			out = append(out, addr)
		}
		return out, nil
	}
	to, err := parseRecipients(message.Header.Get("To"))
	if err != nil {
		return prepared{}, "", err
	}
	cc, err := parseRecipients(message.Header.Get("Cc"))
	if err != nil {
		return prepared{}, "", err
	}
	if len(to) == 0 || len(to)+len(cc)+len(bcc) > 50 {
		return prepared{}, "", errors.New("stored draft recipient count must be 1..50")
	}
	for i, recipient := range bcc {
		bcc[i], err = parseAddress(recipient)
		if err != nil {
			return prepared{}, "", errors.New("invalid Bcc recipient")
		}
	}
	id := strings.TrimSpace(message.Header.Get("Message-ID"))
	if len(id) > 255 || !messageIDPattern.MatchString(id) {
		return prepared{}, "", errors.New("stored draft has no valid Message-ID")
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
	if err != nil || len(subject) > 512 || strings.ContainsAny(subject, "\x00\r\n") {
		return prepared{}, "", errors.New("invalid stored draft subject")
	}
	mimeReader, err := msgmail.CreateReader(bytes.NewReader(raw))
	if err != nil && mimeReader == nil {
		return prepared{}, "", errors.New("malformed stored draft MIME")
	}
	defer mimeReader.Close()
	attachmentNames := []string{}
	for index := 0; ; index++ {
		part, err := mimeReader.NextPart()
		if err == io.EOF {
			break
		}
		if index >= 100 || err != nil {
			return prepared{}, "", errors.New("stored draft MIME is malformed or exceeds 100 parts")
		}
		if header, ok := part.Header.(*msgmail.AttachmentHeader); ok {
			name, _ := header.Filename()
			attachmentNames = append(attachmentNames, name)
			if len(attachmentNames) > 10 {
				return prepared{}, "", errors.New("stored draft exceeds 10 attachments")
			}
		}
		if _, err := io.Copy(io.Discard, part.Body); err != nil {
			return prepared{}, "", errors.New("stored draft MIME part read failed")
		}
	}
	envelope := make([]string, 0, len(to)+len(cc)+len(bcc))
	envelope = append(envelope, to...)
	envelope = append(envelope, cc...)
	envelope = append(envelope, bcc...)
	canonical, _ := json.Marshal(struct {
		Raw []byte
		Bcc []string
	}{raw, bcc})
	digest := sha256.Sum256(canonical)
	return prepared{to: to, cc: cc, bcc: bcc, envelope: envelope, message: raw, digest: hex.EncodeToString(digest[:]), messageID: id, attachmentNames: attachmentNames}, subject, nil
}

func PrepareStoredDraft(from string, raw []byte, key string, bcc []string) (Preview, error) {
	p, subject, err := prepareStoredDraft(from, raw, key, append([]string(nil), bcc...))
	if err != nil {
		return Preview{}, err
	}
	return Preview{Digest: p.digest, From: from, To: p.to, Cc: p.cc, Bcc: p.bcc, Subject: subject, AttachmentCount: len(p.attachmentNames), AttachmentNames: p.attachmentNames, MIMEBytes: len(raw)}, nil
}

func SendStoredDraft(ctx context.Context, a config.Account, journalDir string, raw []byte, key string, bcc []string) (Receipt, error) {
	if !a.AllowSend {
		return Receipt{}, errors.New("send permission is disabled for account")
	}
	p, _, err := prepareStoredDraft(a.Address, raw, key, append([]string(nil), bcc...))
	if err != nil {
		return Receipt{}, err
	}
	return sendPrepared(ctx, a, journalDir, key, p)
}
