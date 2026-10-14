package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/ales/yahoo-mail-mcp/internal/config"
	_ "github.com/emersion/go-message/charset"
	msgmail "github.com/emersion/go-message/mail"
	"io"
	"mime"
	"os"
	"strings"
)

type AttachmentInfo struct {
	Index       int    `json:"index"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	ContentID   string `json:"content_id,omitempty"`
	Size        int64  `json:"size"`
	Inline      bool   `json:"inline"`
}
type ParsedMessage struct {
	Message       Message          `json:"message"`
	Plain         string           `json:"plain,omitempty"`
	HTML          string           `json:"html,omitempty"`
	Attachments   []AttachmentInfo `json:"attachments"`
	BodyTruncated bool             `json:"body_truncated"`
}

func ReadParsed(ctx context.Context, a config.Account, folder string, uid, validity uint32) (ParsedMessage, error) {
	raw, err := Get(ctx, a, folder, uid, validity, 262144)
	if err != nil {
		return ParsedMessage{}, err
	}
	if raw.Truncated {
		return ParsedMessage{}, errors.New("message exceeds 256 KiB MIME parsing limit; use email_get for a bounded raw prefix")
	}
	b, err := base64.StdEncoding.DecodeString(raw.RawBase64)
	if err != nil {
		return ParsedMessage{}, errors.New("invalid raw MIME encoding")
	}
	parsed, err := ParseMIME(b)
	if err != nil {
		return ParsedMessage{}, err
	}
	parsed.Message = raw.Message
	return parsed, nil
}

func ParseMIME(raw []byte) (ParsedMessage, error) {
	if len(raw) > 262144 {
		return ParsedMessage{}, errors.New("MIME input exceeds 256 KiB")
	}
	return parseMIMEReader(bytes.NewReader(raw))
}

// ReadParsedLarge parses bounded MIME data without returning its raw bytes.
func ReadParsedLarge(ctx context.Context, a config.Account, folder string, uid, validity uint32) (ParsedMessage, error) {
	rawFile, message, err := fetchRawToTemp(ctx, a, folder, uid, validity, os.TempDir())
	if err != nil {
		return ParsedMessage{}, err
	}
	defer os.Remove(rawFile.Name())
	defer rawFile.Close()
	parsed, err := parseMIMEReader(rawFile)
	if err != nil {
		return ParsedMessage{}, err
	}
	parsed.Message = message
	return parsed, nil
}

func parseMIMEReader(source io.Reader) (ParsedMessage, error) {
	reader, err := msgmail.CreateReader(source)
	if err != nil && reader == nil {
		return ParsedMessage{}, errors.New("malformed MIME message")
	}
	defer reader.Close()
	out := ParsedMessage{Attachments: []AttachmentInfo{}}
	for index := 0; ; index++ {
		part, err := reader.NextPart()
		if err == io.EOF {
			return out, nil
		}
		if index >= 100 {
			return ParsedMessage{}, errors.New("MIME part count exceeds 100")
		}
		if err != nil && part == nil {
			return ParsedMessage{}, errors.New("malformed MIME part")
		}
		contentType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if contentType == "" {
			contentType = "text/plain"
		}
		limit := int64(65536)
		body, readErr := io.ReadAll(io.LimitReader(part.Body, limit+1))
		if readErr != nil {
			return ParsedMessage{}, errors.New("MIME part read failed")
		}
		truncated := len(body) > int(limit)
		var remaining int64
		if truncated {
			remaining, readErr = io.Copy(io.Discard, part.Body)
			if readErr != nil {
				return ParsedMessage{}, errors.New("MIME part read failed")
			}
		}
		if _, ok := part.Header.(*msgmail.InlineHeader); ok && strings.HasPrefix(contentType, "text/") {
			if truncated {
				body = body[:limit]
				out.BodyTruncated = true
			}
			switch contentType {
			case "text/plain":
				if out.Plain == "" {
					out.Plain = string(body)
				}
			case "text/html":
				if out.HTML == "" {
					out.HTML = string(body)
				}
			}
		} else {
			name := ""
			if h, ok := part.Header.(*msgmail.AttachmentHeader); ok {
				name, _ = h.Filename()
			}
			if name == "" {
				name = fmt.Sprintf("part-%d", index+1)
			}
			disp, _, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
			out.Attachments = append(out.Attachments, AttachmentInfo{Index: index + 1, Filename: name, ContentType: contentType, ContentID: part.Header.Get("Content-ID"), Size: int64(len(body)) + remaining, Inline: disp == "inline"})
		}
	}
}

type AttachmentData struct {
	Metadata      AttachmentInfo `json:"metadata"`
	ContentBase64 string         `json:"content_base64"`
}

func GetAttachment(ctx context.Context, a config.Account, folder string, uid, validity uint32, index int) (AttachmentData, error) {
	if index < 1 || index > 100 {
		return AttachmentData{}, errors.New("attachment index must be 1..100")
	}
	raw, err := Get(ctx, a, folder, uid, validity, 262144)
	if err != nil {
		return AttachmentData{}, err
	}
	if raw.Truncated {
		return AttachmentData{}, errors.New("message exceeds 256 KiB attachment retrieval limit")
	}
	b, err := base64.StdEncoding.DecodeString(raw.RawBase64)
	if err != nil {
		return AttachmentData{}, errors.New("invalid raw MIME encoding")
	}
	reader, err := msgmail.CreateReader(bytes.NewReader(b))
	if err != nil && reader == nil {
		return AttachmentData{}, errors.New("malformed MIME message")
	}
	defer reader.Close()
	for current := 1; current <= 100; current++ {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil && part == nil {
			return AttachmentData{}, errors.New("malformed MIME part")
		}
		if current != index {
			_, _ = io.Copy(io.Discard, part.Body)
			continue
		}
		header, ok := part.Header.(*msgmail.AttachmentHeader)
		if !ok {
			return AttachmentData{}, errors.New("selected MIME part is not an attachment")
		}
		content, err := io.ReadAll(io.LimitReader(part.Body, 65537))
		if err != nil {
			return AttachmentData{}, errors.New("attachment read failed")
		}
		if len(content) > 65536 {
			return AttachmentData{}, errors.New("attachment exceeds 64 KiB output limit")
		}
		name, _ := header.Filename()
		if name == "" {
			name = fmt.Sprintf("part-%d", index)
		}
		typ, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if typ == "" {
			typ = "application/octet-stream"
		}
		disp, _, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		meta := AttachmentInfo{Index: index, Filename: name, ContentType: typ, ContentID: part.Header.Get("Content-ID"), Size: int64(len(content)), Inline: disp == "inline"}
		return AttachmentData{Metadata: meta, ContentBase64: base64.StdEncoding.EncodeToString(content)}, nil
	}
	return AttachmentData{}, errors.New("attachment index not found")
}
