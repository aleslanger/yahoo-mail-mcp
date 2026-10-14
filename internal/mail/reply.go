package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"mime"
	"net/mail"
	"regexp"
	"strings"

	"github.com/aleslanger/yahoo-mail-mcp/internal/config"
)

var replyMessageID = regexp.MustCompile(`^<[^<>[:space:]@]+@[^<>[:space:]@]+>$`)

type ReplyContext struct {
	To         []string `json:"to"`
	Cc         []string `json:"cc,omitempty"`
	Subject    string   `json:"subject"`
	InReplyTo  string   `json:"in_reply_to"`
	References []string `json:"references"`
}

func ReplyToMessage(ctx context.Context, a config.Account, folder string, uid, validity uint32, replyAll bool) (ReplyContext, error) {
	raw, err := GetRange(ctx, a, folder, uid, validity, 0, 65536)
	if err != nil {
		return ReplyContext{}, err
	}
	data, err := base64.StdEncoding.DecodeString(raw.RawBase64)
	if err != nil {
		return ReplyContext{}, errors.New("invalid source MIME encoding")
	}
	if !bytes.Contains(data, []byte("\r\n\r\n")) && !bytes.Contains(data, []byte("\n\n")) {
		return ReplyContext{}, errors.New("source headers exceed 64 KiB or are malformed")
	}
	message, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return ReplyContext{}, errors.New("source message headers are malformed")
	}
	id := strings.TrimSpace(message.Header.Get("Message-ID"))
	if len(id) > 255 || !replyMessageID.MatchString(id) {
		return ReplyContext{}, errors.New("source message has no valid Message-ID")
	}
	addressList := func(value string) ([]string, error) {
		if value == "" {
			return nil, nil
		}
		parsed, err := mail.ParseAddressList(value)
		if err != nil {
			return nil, errors.New("source recipient headers are malformed")
		}
		out := make([]string, 0, len(parsed))
		for _, address := range parsed {
			out = append(out, address.Address)
		}
		return out, nil
	}
	respondTo := message.Header.Get("Reply-To")
	if respondTo == "" {
		respondTo = message.Header.Get("From")
	}
	to, err := addressList(respondTo)
	if err != nil || len(to) == 0 {
		return ReplyContext{}, errors.New("source message has no valid reply address")
	}
	cc := []string{}
	if replyAll {
		for _, field := range []string{"To", "Cc"} {
			others, err := addressList(message.Header.Get(field))
			if err != nil {
				return ReplyContext{}, err
			}
			cc = append(cc, others...)
		}
	}
	seen := map[string]bool{}
	unique := func(addresses []string) []string {
		out := make([]string, 0, len(addresses))
		for _, address := range addresses {
			key := strings.ToLower(address)
			if !seen[key] {
				seen[key] = true
				out = append(out, address)
			}
		}
		return out
	}
	to = unique(to)
	// A self-addressed source still has a valid reply recipient. Suppress the
	// account address only from reply-all Cc, where it would be redundant.
	seen[strings.ToLower(a.Address)] = true
	cc = unique(cc)
	if len(to) == 0 || len(to)+len(cc) > 50 {
		return ReplyContext{}, errors.New("reply recipient count must be 1..50")
	}
	references := strings.Fields(message.Header.Get("References"))
	if len(references) > 19 {
		references = references[len(references)-19:]
	}
	for _, reference := range references {
		if len(reference) > 255 || !replyMessageID.MatchString(reference) {
			return ReplyContext{}, errors.New("source References header is malformed")
		}
	}
	references = append(references, id)
	subject, err := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
	if err != nil {
		return ReplyContext{}, errors.New("source subject is malformed")
	}
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}
	if len(subject) > 512 || strings.ContainsAny(subject, "\x00\r\n") {
		return ReplyContext{}, errors.New("reply subject is invalid or too long")
	}
	return ReplyContext{To: to, Cc: cc, Subject: subject, InReplyTo: id, References: references}, nil
}
