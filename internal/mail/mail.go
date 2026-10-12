package mail

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/ales/yahoo-mail-mcp/internal/config"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrStale = errors.New("UIDVALIDITY changed; refresh message references")
var ErrUnsupported = errors.New("server does not advertise a safe MOVE capability")

const maxPage = 50

type Folder struct {
	Name       string             `json:"name"`
	Delimiter  string             `json:"delimiter"`
	Attributes []imap.MailboxAttr `json:"attributes"`
}
type Message struct {
	UID         uint32      `json:"uid"`
	UIDValidity uint32      `json:"uid_validity"`
	Subject     string      `json:"subject"`
	From        string      `json:"from"`
	Date        string      `json:"date"`
	Size        int64       `json:"size"`
	Flags       []imap.Flag `json:"flags"`
}
type Page struct {
	Folder        string    `json:"folder"`
	UIDValidity   uint32    `json:"uid_validity"`
	Messages      []Message `json:"messages"`
	NextBeforeUID uint32    `json:"next_before_uid,omitempty"`
	HasMore       bool      `json:"has_more"`
}
type Body struct {
	Message   Message `json:"message"`
	RawBase64 string  `json:"raw_base64"`
	Truncated bool    `json:"truncated"`
	TotalSize int64   `json:"total_size"`
}

func connect(ctx context.Context, a config.Account) (*imapclient.Client, func(), error) {
	p, err := config.Password(a.Alias)
	if err != nil {
		return nil, nil, err
	}
	addr := net.JoinHostPort(a.IMAPHost, fmt.Sprint(a.IMAPPort))
	c, err := imapclient.DialTLS(addr, &imapclient.Options{Dialer: &net.Dialer{Timeout: 10 * time.Second}})
	if err != nil {
		return nil, nil, errors.New("IMAP TLS connection failed")
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	done := make(chan struct{})
	var once sync.Once
	closeClient := func() { once.Do(func() { _ = c.Close() }) }
	go func() {
		select {
		case <-deadlineCtx.Done():
			closeClient()
		case <-done:
		}
	}()
	closeFn := func() { close(done); cancel(); closeClient() }
	if err := c.Login(a.Address, p).Wait(); err != nil {
		closeFn()
		return nil, nil, errors.New("IMAP authentication failed")
	}
	return c, closeFn, nil
}
func selectFolder(c *imapclient.Client, folder string, readonly bool) (*imap.SelectData, error) {
	if folder == "" || strings.ContainsAny(folder, "\x00\r\n") {
		return nil, errors.New("invalid folder name")
	}
	s, err := c.Select(folder, &imap.SelectOptions{ReadOnly: readonly}).Wait()
	if err != nil {
		return nil, errors.New("folder selection failed")
	}
	if s.UIDValidity == 0 {
		return nil, errors.New("server omitted UIDVALIDITY")
	}
	return s, nil
}
func checkValidity(actual, expected uint32) error {
	if expected == 0 {
		return errors.New("uid_validity is required")
	}
	if actual != expected {
		return ErrStale
	}
	return nil
}
func validateUID(uid uint32) error {
	if uid == 0 {
		return errors.New("UID must be positive")
	}
	return nil
}
func folderName(folder string) error {
	if folder == "" || strings.ContainsAny(folder, "\x00\r\n") {
		return errors.New("invalid folder name")
	}
	return nil
}
func address(a *imap.Address) string {
	if a == nil {
		return ""
	}
	return a.Mailbox + "@" + a.Host
}
func toMessage(m *imapclient.FetchMessageBuffer, validity uint32) Message {
	out := Message{UID: uint32(m.UID), UIDValidity: validity, Size: m.RFC822Size, Flags: m.Flags}
	if m.Envelope != nil {
		out.Subject = m.Envelope.Subject
		out.Date = m.Envelope.Date.Format(time.RFC3339)
		if len(m.Envelope.From) > 0 {
			out.From = address(&m.Envelope.From[0])
		}
	}
	return out
}
func ListFolders(ctx context.Context, a config.Account) ([]Folder, error) {
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	cmd := c.List("", "*", nil)
	out := make([]Folder, 0, 32)
	for r := cmd.Next(); r != nil; r = cmd.Next() {
		if len(out) >= 1000 {
			return nil, errors.New("folder count exceeds limit")
		}
		out = append(out, Folder{Name: r.Mailbox, Delimiter: string(r.Delim), Attributes: r.Attrs})
	}
	if err := cmd.Close(); err != nil {
		return nil, errors.New("folder listing failed")
	}
	return out, nil
}
func Capabilities(ctx context.Context, a config.Account) ([]string, error) {
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	caps, err := c.Capability().Wait()
	if err != nil {
		return nil, errors.New("capability negotiation failed")
	}
	out := make([]string, 0, len(caps))
	for cap := range caps {
		out = append(out, string(cap))
	}
	sort.Strings(out)
	return out, nil
}
func List(ctx context.Context, a config.Account, folder string, before uint32, limit int) (Page, error) {
	return Search(ctx, a, folder, before, limit, SearchFilter{})
}

type SearchFilter struct {
	From    string `json:"from,omitempty"`
	Subject string `json:"subject,omitempty"`
	Text    string `json:"text,omitempty"`
	Unread  bool   `json:"unread,omitempty"`
}

func Search(ctx context.Context, a config.Account, folder string, before uint32, limit int, filter SearchFilter) (Page, error) {
	if limit < 1 || limit > maxPage {
		return Page{}, fmt.Errorf("limit must be 1..%d", maxPage)
	}
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		return Page{}, err
	}
	defer closeFn()
	sel, err := selectFolder(c, folder, true)
	if err != nil {
		return Page{}, err
	}
	result := Page{Folder: folder, UIDValidity: sel.UIDValidity, Messages: []Message{}}
	criteria := &imap.SearchCriteria{}
	for _, field := range []struct{ key, value string }{{"From", filter.From}, {"Subject", filter.Subject}} {
		if field.value != "" {
			if len(field.value) > 256 || strings.ContainsAny(field.value, "\x00\r\n") {
				return Page{}, errors.New("invalid search term")
			}
			criteria.Header = append(criteria.Header, imap.SearchCriteriaHeaderField{Key: field.key, Value: field.value})
		}
	}
	if filter.Text != "" {
		if len(filter.Text) > 256 || strings.ContainsAny(filter.Text, "\x00\r\n") {
			return Page{}, errors.New("invalid search term")
		}
		criteria.Text = []string{filter.Text}
	}
	if filter.Unread {
		criteria.NotFlag = []imap.Flag{imap.FlagSeen}
	}
	// Bound the search to the 5000 most recent UID numbers. Sparse mailboxes may need repeated calls.
	upper := uint32(sel.UIDNext)
	if before > 0 && before < upper {
		upper = before
	}
	if upper <= 1 || sel.NumMessages == 0 {
		return result, nil
	}
	lower := uint32(1)
	if upper > 5000 {
		lower = upper - 5000
	}
	set := imap.UIDSet{}
	set.AddRange(imap.UID(lower), imap.UID(upper-1))
	criteria.UID = []imap.UIDSet{set}
	data, err := c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return Page{}, errors.New("UID search failed")
	}
	ids := data.AllUIDs()
	if len(ids) == 0 {
		if lower > 1 {
			result.HasMore = true
			result.NextBeforeUID = lower
		}
		return result, nil
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	if len(ids) > limit || lower > 1 {
		result.HasMore = true
	}
	if len(ids) > limit {
		ids = ids[:limit]
	}
	uidset := imap.UIDSet{}
	for _, id := range ids {
		uidset.AddNum(id)
	}
	messages, err := c.Fetch(uidset, &imap.FetchOptions{UID: true, Envelope: true, Flags: true, RFC822Size: true}).Collect()
	if err != nil {
		return Page{}, errors.New("message fetch failed")
	}
	for _, m := range messages {
		result.Messages = append(result.Messages, toMessage(m, sel.UIDValidity))
	}
	sort.Slice(result.Messages, func(i, j int) bool { return result.Messages[i].UID > result.Messages[j].UID })
	if len(result.Messages) > 0 && result.HasMore {
		result.NextBeforeUID = result.Messages[len(result.Messages)-1].UID
	}
	return result, nil
}
func Get(ctx context.Context, a config.Account, folder string, uid, validity uint32, maxBytes int) (Body, error) {
	if err := validateUID(uid); err != nil {
		return Body{}, err
	}
	if maxBytes < 1 || maxBytes > 262144 {
		return Body{}, errors.New("max_bytes must be 1..262144")
	}
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		return Body{}, err
	}
	defer closeFn()
	sel, err := selectFolder(c, folder, true)
	if err != nil {
		return Body{}, err
	}
	if err := checkValidity(sel.UIDValidity, validity); err != nil {
		return Body{}, err
	}
	section := &imap.FetchItemBodySection{Peek: true, Partial: &imap.SectionPartial{Offset: 0, Size: int64(maxBytes)}}
	messages, err := c.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{UID: true, Envelope: true, Flags: true, RFC822Size: true, BodySection: []*imap.FetchItemBodySection{section}}).Collect()
	if err != nil {
		return Body{}, errors.New("message fetch failed")
	}
	if len(messages) != 1 {
		return Body{}, errors.New("message not found")
	}
	m := messages[0]
	b := Body{Message: toMessage(m, sel.UIDValidity), TotalSize: m.RFC822Size}
	if len(m.BodySection) > 0 {
		b.RawBase64 = base64.StdEncoding.EncodeToString(m.BodySection[0].Bytes)
		b.Truncated = int64(len(m.BodySection[0].Bytes)) < b.TotalSize
	}
	return b, nil
}
func ChangeFlag(ctx context.Context, a config.Account, folder string, uid, validity uint32, flag imap.Flag, enabled bool) error {
	if err := validateUID(uid); err != nil {
		return err
	}
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		return err
	}
	defer closeFn()
	sel, err := selectFolder(c, folder, false)
	if err != nil {
		return err
	}
	if err := checkValidity(sel.UIDValidity, validity); err != nil {
		return err
	}
	before, err := c.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{UID: true}).Collect()
	if err != nil || len(before) != 1 {
		return errors.New("message not found or UID check failed")
	}
	op := imap.StoreFlagsDel
	if enabled {
		op = imap.StoreFlagsAdd
	}
	if err := c.Store(imap.UIDSetNum(imap.UID(uid)), &imap.StoreFlags{Op: op, Flags: []imap.Flag{flag}, Silent: true}, nil).Close(); err != nil {
		return errors.New("flag update failed or outcome uncertain")
	}
	after, err := c.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{UID: true, Flags: true}).Collect()
	if err != nil || len(after) != 1 {
		return errors.New("flag update outcome uncertain; inspect message before retrying")
	}
	has := false
	for _, current := range after[0].Flags {
		if current == flag {
			has = true
		}
	}
	if has != enabled {
		return errors.New("flag update postcondition was not observed")
	}
	return nil
}
func CopyOrMove(ctx context.Context, a config.Account, source, target string, uid, validity uint32, move bool) (any, error) {
	if err := validateUID(uid); err != nil {
		return nil, err
	}
	if err := folderName(target); err != nil {
		return nil, err
	}
	if source == target {
		return nil, errors.New("source and target folders are identical")
	}
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	sel, err := selectFolder(c, source, false)
	if err != nil {
		return nil, err
	}
	if err := checkValidity(sel.UIDValidity, validity); err != nil {
		return nil, err
	}
	set := imap.UIDSetNum(imap.UID(uid))
	found, err := c.Fetch(set, &imap.FetchOptions{UID: true}).Collect()
	if err != nil || len(found) != 1 {
		return nil, errors.New("source message not found or UID check failed")
	}
	if move {
		if !c.Caps().Has(imap.CapMove) {
			return nil, ErrUnsupported
		}
		data, err := c.Move(set, target).Wait()
		if err != nil {
			return nil, errors.New("move failed or outcome uncertain; inspect both folders before retrying")
		}
		return data, nil
	}
	data, err := c.Copy(set, target).Wait()
	if err != nil {
		return nil, errors.New("copy failed or outcome uncertain; inspect destination before retrying")
	}
	return data, nil
}

// FolderStatus returns counts without selecting or changing a mailbox.
type FolderInfo struct {
	Name        string `json:"name"`
	Messages    uint32 `json:"messages"`
	Unseen      uint32 `json:"unseen"`
	UIDValidity uint32 `json:"uid_validity"`
	UIDNext     uint32 `json:"uid_next"`
}

func Status(ctx context.Context, a config.Account, folder string) (FolderInfo, error) {
	if err := folderName(folder); err != nil {
		return FolderInfo{}, err
	}
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		return FolderInfo{}, err
	}
	defer closeFn()
	data, err := c.Status(folder, &imap.StatusOptions{NumMessages: true, NumUnseen: true, UIDValidity: true, UIDNext: true}).Wait()
	if err != nil {
		return FolderInfo{}, errors.New("folder status failed")
	}
	out := FolderInfo{Name: folder, UIDValidity: data.UIDValidity, UIDNext: uint32(data.UIDNext)}
	if data.NumMessages != nil {
		out.Messages = *data.NumMessages
	}
	if data.NumUnseen != nil {
		out.Unseen = *data.NumUnseen
	}
	return out, nil
}
func CreateFolder(ctx context.Context, a config.Account, folder string) error {
	if err := folderName(folder); err != nil {
		return err
	}
	if strings.EqualFold(folder, "INBOX") {
		return errors.New("system folder cannot be created")
	}
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		return err
	}
	defer closeFn()
	if err := c.Create(folder, nil).Wait(); err != nil {
		return errors.New("folder creation failed; check for collision or permissions")
	}
	return nil
}
