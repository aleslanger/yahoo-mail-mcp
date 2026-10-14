package send

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

type Attachment struct {
	Filename      string `json:"filename"`
	ContentType   string `json:"content_type,omitempty"`
	ContentBase64 string `json:"content_base64"`
}

type Input struct {
	IdempotencyKey string       `json:"idempotency_key"`
	To             []string     `json:"to"`
	Cc             []string     `json:"cc,omitempty"`
	Bcc            []string     `json:"bcc,omitempty"`
	Subject        string       `json:"subject"`
	Text           string       `json:"text,omitempty"`
	HTML           string       `json:"html,omitempty"`
	Attachments    []Attachment `json:"attachments,omitempty"`
	InReplyTo      string       `json:"in_reply_to,omitempty"`
	References     []string     `json:"references,omitempty"`
}

var messageIDPattern = regexp.MustCompile(`^<[^<>[:space:]@]+@[^<>[:space:]@]+>$`)

type Preview struct {
	Digest          string   `json:"digest"`
	From            string   `json:"from"`
	To              []string `json:"to"`
	Cc              []string `json:"cc,omitempty"`
	Bcc             []string `json:"bcc,omitempty"`
	Subject         string   `json:"subject"`
	InReplyTo       string   `json:"in_reply_to,omitempty"`
	References      []string `json:"references,omitempty"`
	AttachmentCount int      `json:"attachment_count"`
	AttachmentNames []string `json:"attachment_names,omitempty"`
	MIMEBytes       int      `json:"mime_bytes"`
}

func Prepare(from string, in Input) (Preview, error) {
	p, err := prepare(from, in)
	if err != nil {
		return Preview{}, err
	}
	names := make([]string, 0, len(in.Attachments))
	for _, attachment := range in.Attachments {
		names = append(names, attachment.Filename)
	}
	return Preview{Digest: p.digest, From: from, To: p.to, Cc: p.cc, Bcc: p.bcc, Subject: in.Subject, InReplyTo: in.InReplyTo, References: in.References, AttachmentCount: len(in.Attachments), AttachmentNames: names, MIMEBytes: len(p.message)}, nil
}

// ComposeDraft builds a bounded MIME message without opening an SMTP connection.
// Draft operations use the same address, header and body validation as sending.
func ComposeDraft(from string, in Input) ([]byte, error) {
	if in.IdempotencyKey == "" {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, errors.New("cannot generate draft identifier")
		}
		in.IdempotencyKey = "draft-" + hex.EncodeToString(nonce[:])
	}
	p, err := prepare(from, in)
	if err != nil {
		return nil, err
	}
	return p.message, nil
}

type prepared struct {
	to, cc, bcc     []string
	envelope        []string
	message         []byte
	digest          string
	messageID       string
	attachmentNames []string
}

func prepare(from string, in Input) (prepared, error) {
	if !keyPattern.MatchString(in.IdempotencyKey) {
		return prepared{}, errors.New("invalid idempotency_key")
	}
	if len(in.To) == 0 || len(in.To)+len(in.Cc)+len(in.Bcc) > 50 {
		return prepared{}, errors.New("recipient count must be 1..50 with at least one To address")
	}
	if len(in.Subject) > 512 || strings.ContainsAny(in.Subject, "\x00\r\n") {
		return prepared{}, errors.New("invalid subject")
	}
	if (in.Text == "" && in.HTML == "") || len(in.Text) > 131072 || len(in.HTML) > 131072 {
		return prepared{}, errors.New("message body must be present and at most 128 KiB per part")
	}
	if len(in.Attachments) > 10 {
		return prepared{}, errors.New("at most 10 attachments are allowed")
	}
	if in.InReplyTo != "" && (!messageIDPattern.MatchString(in.InReplyTo) || len(in.InReplyTo) > 255) {
		return prepared{}, errors.New("invalid in_reply_to message ID")
	}
	if len(in.References) > 20 {
		return prepared{}, errors.New("at most 20 references are allowed")
	}
	for _, id := range in.References {
		if !messageIDPattern.MatchString(id) || len(id) > 255 {
			return prepared{}, errors.New("invalid references message ID")
		}
	}
	type decodedAttachment struct {
		filename, contentType string
		data                  []byte
	}
	attachments := make([]decodedAttachment, 0, len(in.Attachments))
	totalAttachmentBytes := 0
	for _, item := range in.Attachments {
		if item.Filename == "" || len(item.Filename) > 200 || strings.ContainsAny(item.Filename, "\x00\r\n/\\") || item.Filename == "." || item.Filename == ".." {
			return prepared{}, errors.New("invalid attachment filename")
		}
		if len(item.ContentBase64) > 2*1048576 {
			return prepared{}, errors.New("attachment exceeds 1 MiB decoded limit")
		}
		data, err := base64.StdEncoding.DecodeString(item.ContentBase64)
		if err != nil || len(data) > 1048576 {
			return prepared{}, errors.New("invalid or oversized attachment content")
		}
		totalAttachmentBytes += len(data)
		if totalAttachmentBytes > 5*1048576 {
			return prepared{}, errors.New("total attachment size exceeds 5 MiB")
		}
		typ := item.ContentType
		if typ == "" {
			typ = "application/octet-stream"
		}
		parsed, params, err := mime.ParseMediaType(typ)
		if err != nil || parsed == "" || len(params) != 0 || strings.ContainsAny(typ, "\x00\r\n") {
			return prepared{}, errors.New("invalid attachment content type")
		}
		attachments = append(attachments, decodedAttachment{item.Filename, parsed, data})
	}
	fromAddr, err := parseAddress(from)
	if err != nil {
		return prepared{}, errors.New("invalid configured sender address")
	}
	p := prepared{}
	for _, group := range []struct {
		input  []string
		output *[]string
	}{{in.To, &p.to}, {in.Cc, &p.cc}, {in.Bcc, &p.bcc}} {
		for _, raw := range group.input {
			addr, err := parseAddress(raw)
			if err != nil {
				return prepared{}, errors.New("invalid recipient address")
			}
			*group.output = append(*group.output, addr)
			p.envelope = append(p.envelope, addr)
		}
	}
	canonical, _ := json.Marshal(struct {
		From                string
		To, Cc, Bcc         []string
		Subject, Text, HTML string
		Attachments         []Attachment
		InReplyTo           string
		References          []string
	}{fromAddr, p.to, p.cc, p.bcc, in.Subject, in.Text, in.HTML, in.Attachments, in.InReplyTo, in.References})
	digest := sha256.Sum256(canonical)
	p.digest = hex.EncodeToString(digest[:])
	parts := strings.Split(fromAddr, "@")
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return prepared{}, errors.New("cannot generate message ID")
	}
	p.messageID = fmt.Sprintf("<%x@%s>", random, parts[1])
	var buf bytes.Buffer
	writeHeader(&buf, "From", fromAddr)
	writeHeader(&buf, "To", strings.Join(p.to, ", "))
	if len(p.cc) > 0 {
		writeHeader(&buf, "Cc", strings.Join(p.cc, ", "))
	}
	writeHeader(&buf, "Subject", mime.QEncoding.Encode("utf-8", in.Subject))
	writeHeader(&buf, "Date", time.Now().Format(time.RFC1123Z))
	writeHeader(&buf, "Message-ID", p.messageID)
	if in.InReplyTo != "" {
		writeHeader(&buf, "In-Reply-To", in.InReplyTo)
	}
	if len(in.References) > 0 {
		writeHeader(&buf, "References", strings.Join(in.References, " "))
	}
	writeHeader(&buf, "MIME-Version", "1.0")
	if len(attachments) > 0 {
		mixed := multipart.NewWriter(&buf)
		writeHeader(&buf, "Content-Type", "multipart/mixed; boundary="+mixed.Boundary())
		buf.WriteString("\r\n")
		if in.Text != "" && in.HTML != "" {
			var altBody bytes.Buffer
			alt := multipart.NewWriter(&altBody)
			bodyHeader := map[string][]string{"Content-Type": {"multipart/alternative; boundary=" + alt.Boundary()}}
			part, err := mixed.CreatePart(bodyHeader)
			if err != nil {
				return prepared{}, err
			}
			for _, bodyPart := range []struct{ typ, body string }{{"text/plain", in.Text}, {"text/html", in.HTML}} {
				if err := writeTextPart(alt, bodyPart.typ, bodyPart.body); err != nil {
					return prepared{}, err
				}
			}
			if err := alt.Close(); err != nil {
				return prepared{}, err
			}
			if _, err := part.Write(altBody.Bytes()); err != nil {
				return prepared{}, err
			}
		} else {
			typ, body := "text/plain", in.Text
			if body == "" {
				typ, body = "text/html", in.HTML
			}
			if err := writeTextPart(mixed, typ, body); err != nil {
				return prepared{}, err
			}
		}
		for _, item := range attachments {
			h := map[string][]string{
				"Content-Type":              {mime.FormatMediaType(item.contentType, map[string]string{"name": item.filename})},
				"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": item.filename})},
				"Content-Transfer-Encoding": {"base64"},
			}
			part, err := mixed.CreatePart(h)
			if err != nil {
				return prepared{}, err
			}
			encoded := make([]byte, base64.StdEncoding.EncodedLen(len(item.data)))
			base64.StdEncoding.Encode(encoded, item.data)
			for len(encoded) > 76 {
				if _, err := part.Write(encoded[:76]); err != nil {
					return prepared{}, err
				}
				if _, err := part.Write([]byte("\r\n")); err != nil {
					return prepared{}, err
				}
				encoded = encoded[76:]
			}
			if _, err := part.Write(encoded); err != nil {
				return prepared{}, err
			}
		}
		if err := mixed.Close(); err != nil {
			return prepared{}, err
		}
	} else if in.Text != "" && in.HTML != "" {
		multi := multipart.NewWriter(&buf)
		writeHeader(&buf, "Content-Type", "multipart/alternative; boundary="+multi.Boundary())
		buf.WriteString("\r\n")
		for _, part := range []struct{ typ, body string }{{"text/plain", in.Text}, {"text/html", in.HTML}} {
			h := make(map[string][]string)
			h["Content-Type"] = []string{part.typ + "; charset=utf-8"}
			h["Content-Transfer-Encoding"] = []string{"quoted-printable"}
			w, err := multi.CreatePart(h)
			if err != nil {
				return prepared{}, err
			}
			q := quotedprintable.NewWriter(w)
			if _, err := q.Write([]byte(part.body)); err != nil {
				return prepared{}, err
			}
			if err := q.Close(); err != nil {
				return prepared{}, err
			}
		}
		if err := multi.Close(); err != nil {
			return prepared{}, err
		}
	} else {
		typ, body := "text/plain", in.Text
		if body == "" {
			typ, body = "text/html", in.HTML
		}
		writeHeader(&buf, "Content-Type", typ+"; charset=utf-8")
		writeHeader(&buf, "Content-Transfer-Encoding", "quoted-printable")
		buf.WriteString("\r\n")
		q := quotedprintable.NewWriter(&buf)
		if _, err := q.Write([]byte(body)); err != nil {
			return prepared{}, err
		}
		if err := q.Close(); err != nil {
			return prepared{}, err
		}
	}
	p.message = buf.Bytes()
	return p, nil
}

func writeTextPart(writer *multipart.Writer, typ, body string) error {
	h := map[string][]string{"Content-Type": {typ + "; charset=utf-8"}, "Content-Transfer-Encoding": {"quoted-printable"}}
	part, err := writer.CreatePart(h)
	if err != nil {
		return err
	}
	q := quotedprintable.NewWriter(part)
	if _, err := q.Write([]byte(body)); err != nil {
		return err
	}
	return q.Close()
}

func parseAddress(raw string) (string, error) {
	if strings.ContainsAny(raw, "\x00\r\n") || len(raw) > 320 {
		return "", errors.New("invalid address")
	}
	a, err := mail.ParseAddress(raw)
	if err != nil || a == nil || a.Address != raw || strings.Count(a.Address, "@") != 1 {
		return "", errors.New("invalid address")
	}
	for _, r := range raw {
		if r > 127 {
			return "", errors.New("SMTPUTF8 address unsupported")
		}
	}
	return a.Address, nil
}

func writeHeader(buf *bytes.Buffer, name, value string) {
	buf.WriteString(name)
	buf.WriteString(": ")
	buf.WriteString(value)
	buf.WriteString("\r\n")
}
