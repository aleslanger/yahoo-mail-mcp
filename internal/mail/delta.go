package mail

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/ales/yahoo-mail-mcp/internal/config"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Header is the locally indexed summary of one message.
type Header struct {
	UID     uint32      `json:"uid"`
	Subject string      `json:"subject"`
	From    string      `json:"from"`
	To      []string    `json:"to,omitempty"`
	Date    time.Time   `json:"date"`
	Size    int64       `json:"size"`
	Flags   []imap.Flag `json:"flags"`
}

// FolderDelta is one bounded, read-only synchronization step for a folder.
type FolderDelta struct {
	UIDValidity uint32
	// NextUID is the first UID to request on the next step.
	NextUID uint32
	// Reset reports that UIDVALIDITY changed and earlier headers are invalid.
	Reset bool
	New   []Header
	// Complete reports that all current messages were seen; Flags then lists
	// every UID still present in the folder with its current flags.
	Complete bool
	Flags    map[uint32][]imap.Flag
}

const (
	maxDeltaHeaders = 2000
	deltaFetchChunk = 250
)

// FetchFolderDelta reads, without changing the mailbox, at most maxNew headers
// with UID >= fromUID. When no headers remain it also lists every UID with its
// flags so the caller can drop expunged messages and refresh flags.
func FetchFolderDelta(ctx context.Context, a config.Account, folder string, knownValidity, fromUID uint32, maxNew int) (FolderDelta, error) {
	if maxNew < 1 || maxNew > maxDeltaHeaders {
		return FolderDelta{}, fmt.Errorf("max_new must be 1..%d", maxDeltaHeaders)
	}
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		return FolderDelta{}, err
	}
	defer closeFn()
	sel, err := selectFolder(c, folder, true)
	if err != nil {
		return FolderDelta{}, err
	}
	delta := FolderDelta{UIDValidity: sel.UIDValidity, NextUID: uint32(sel.UIDNext), New: []Header{}}
	if knownValidity != sel.UIDValidity {
		delta.Reset = knownValidity != 0
		fromUID = 1
	}
	fromUID = max(fromUID, 1)
	if delta.NextUID == 0 {
		delta.NextUID = fromUID
	}
	if sel.NumMessages == 0 {
		delta.Complete, delta.Flags = true, map[uint32][]imap.Flag{}
		return delta, nil
	}
	pending, err := uidsFrom(c, fromUID)
	if err != nil {
		return FolderDelta{}, err
	}
	if len(pending) > maxNew {
		pending = pending[:maxNew]
		delta.NextUID = uint32(pending[len(pending)-1]) + 1
	} else {
		delta.Complete = true
		if delta.NextUID == 0 && len(pending) > 0 {
			delta.NextUID = uint32(pending[len(pending)-1]) + 1
		}
	}
	if delta.New, err = fetchHeaders(c, pending); err != nil {
		return FolderDelta{}, err
	}
	if delta.Complete {
		if delta.Flags, err = allFlags(c); err != nil {
			return FolderDelta{}, err
		}
	}
	return delta, nil
}

// uidsFrom returns ascending UIDs >= from. "n:*" also matches the highest UID
// when n exceeds it, so results below from are discarded.
func uidsFrom(c *imapclient.Client, from uint32) ([]imap.UID, error) {
	set := imap.UIDSet{}
	set.AddRange(imap.UID(from), 0)
	data, err := c.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{set}}, nil).Wait()
	if err != nil {
		return nil, NewTemporaryError("UID search failed")
	}
	uids := []imap.UID{}
	for _, uid := range data.AllUIDs() {
		if uint32(uid) >= from {
			uids = append(uids, uid)
		}
	}
	slices.Sort(uids)
	return uids, nil
}

func fetchHeaders(c *imapclient.Client, uids []imap.UID) ([]Header, error) {
	headers := make([]Header, 0, len(uids))
	for start := 0; start < len(uids); start += deltaFetchChunk {
		set := imap.UIDSetNum(uids[start:min(start+deltaFetchChunk, len(uids))]...)
		messages, err := c.Fetch(set, &imap.FetchOptions{UID: true, Envelope: true, Flags: true, RFC822Size: true, InternalDate: true}).Collect()
		if err != nil {
			return nil, NewTemporaryError("header fetch failed")
		}
		for _, m := range messages {
			headers = append(headers, toHeader(m))
		}
	}
	slices.SortFunc(headers, func(x, y Header) int { return cmp.Compare(x.UID, y.UID) })
	return headers, nil
}

func toHeader(m *imapclient.FetchMessageBuffer) Header {
	h := Header{UID: uint32(m.UID), Size: m.RFC822Size, Flags: m.Flags, Date: m.InternalDate}
	if h.Flags == nil {
		h.Flags = []imap.Flag{}
	}
	if m.Envelope == nil {
		return h
	}
	h.Subject = m.Envelope.Subject
	if !m.Envelope.Date.IsZero() {
		h.Date = m.Envelope.Date
	}
	if len(m.Envelope.From) > 0 {
		h.From = address(&m.Envelope.From[0])
	}
	for i := range m.Envelope.To {
		h.To = append(h.To, address(&m.Envelope.To[i]))
	}
	return h
}

func allFlags(c *imapclient.Client) (map[uint32][]imap.Flag, error) {
	set := imap.UIDSet{}
	set.AddRange(1, 0)
	messages, err := c.Fetch(set, &imap.FetchOptions{UID: true, Flags: true}).Collect()
	if err != nil {
		return nil, NewTemporaryError("flag fetch failed")
	}
	flags := make(map[uint32][]imap.Flag, len(messages))
	for _, m := range messages {
		f := m.Flags
		if f == nil {
			f = []imap.Flag{}
		}
		flags[uint32(m.UID)] = f
	}
	return flags, nil
}

// SearchOldest runs one server-side search over the whole folder and returns
// the oldest matching UIDs, at most limit of them.
func SearchOldest(ctx context.Context, a config.Account, folder string, filter SearchFilter, limit int) (uint32, []uint32, error) {
	if limit < 1 || limit > maxDeltaHeaders {
		return 0, nil, fmt.Errorf("limit must be 1..%d", maxDeltaHeaders)
	}
	criteria, err := searchCriteria(filter)
	if err != nil {
		return 0, nil, err
	}
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		return 0, nil, err
	}
	defer closeFn()
	sel, err := selectFolder(c, folder, true)
	if err != nil {
		return 0, nil, err
	}
	if sel.NumMessages == 0 {
		return sel.UIDValidity, []uint32{}, nil
	}
	data, err := c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return 0, nil, NewTemporaryError("UID search failed")
	}
	found := data.AllUIDs()
	slices.Sort(found)
	uids := make([]uint32, 0, min(limit, len(found)))
	for _, uid := range found[:min(limit, len(found))] {
		uids = append(uids, uint32(uid))
	}
	return sel.UIDValidity, uids, nil
}

// FetchHeaders reads headers of the given UIDs without marking them read.
func FetchHeaders(ctx context.Context, a config.Account, folder string, validity uint32, uids []uint32) ([]Header, error) {
	if len(uids) == 0 || len(uids) > maxDeltaHeaders {
		return nil, fmt.Errorf("uids must contain 1..%d entries", maxDeltaHeaders)
	}
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	sel, err := selectFolder(c, folder, true)
	if err != nil {
		return nil, err
	}
	if err := checkValidity(sel.UIDValidity, validity); err != nil {
		return nil, err
	}
	list := make([]imap.UID, 0, len(uids))
	for _, uid := range uids {
		if err := validateUID(uid); err != nil {
			return nil, err
		}
		list = append(list, imap.UID(uid))
	}
	return fetchHeaders(c, list)
}
