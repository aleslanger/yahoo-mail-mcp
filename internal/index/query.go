package index

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/aleslanger/yahoo-mail-mcp/internal/mail"
	"github.com/emersion/go-imap/v2"
)

const (
	defaultQueryLimit = 20
	maxQueryLimit     = 100
	queryDateLayout   = "2006-01-02"
)

// Query filters indexed headers with AND semantics. Text matches are
// case-insensitive substrings; Since is inclusive and Before exclusive.
type Query struct {
	Folder  string `json:"folder,omitempty"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Subject string `json:"subject,omitempty"`
	Since   string `json:"since,omitempty"`
	Before  string `json:"before,omitempty"`
	Unread  bool   `json:"unread,omitempty"`
	Flagged bool   `json:"flagged,omitempty"`
	Sort    string `json:"sort,omitempty"`
	Order   string `json:"order,omitempty"`
	Offset  int    `json:"offset,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

// Hit is one indexed message with the reference needed by IMAP tools.
type Hit struct {
	Folder      string `json:"folder"`
	UIDValidity uint32 `json:"uid_validity"`
	mail.Header
}

type Result struct {
	Total   int   `json:"total"`
	HasMore bool  `json:"has_more"`
	Results []Hit `json:"results"`
}

type window struct {
	since, before time.Time
}

func parseWindow(q Query) (window, error) {
	var w window
	var err error
	if q.Since != "" {
		if w.since, err = time.Parse(queryDateLayout, q.Since); err != nil {
			return w, errors.New("since must be YYYY-MM-DD")
		}
	}
	if q.Before != "" {
		if w.before, err = time.Parse(queryDateLayout, q.Before); err != nil {
			return w, errors.New("before must be YYYY-MM-DD")
		}
	}
	return w, nil
}

func contains(haystack, needle string) bool {
	return needle == "" || strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

func matches(q Query, w window, folder string, h mail.Header) bool {
	switch {
	case q.Folder != "" && q.Folder != folder,
		!contains(h.From, q.From),
		!contains(strings.Join(h.To, ","), q.To),
		!contains(h.Subject, q.Subject),
		!w.since.IsZero() && h.Date.Before(w.since),
		!w.before.IsZero() && !h.Date.Before(w.before),
		q.Unread && slices.Contains(h.Flags, imap.FlagSeen),
		q.Flagged && !slices.Contains(h.Flags, imap.FlagFlagged):
		return false
	}
	return true
}

func less(sortKey string) (func(a, b Hit) bool, error) {
	switch sortKey {
	case "", "date":
		return func(a, b Hit) bool { return a.Date.Before(b.Date) }, nil
	case "from":
		return func(a, b Hit) bool { return strings.ToLower(a.From) < strings.ToLower(b.From) }, nil
	case "subject":
		return func(a, b Hit) bool { return strings.ToLower(a.Subject) < strings.ToLower(b.Subject) }, nil
	case "size":
		return func(a, b Hit) bool { return a.Size < b.Size }, nil
	}
	return nil, errors.New("sort must be date, from, subject or size")
}

func query(snaps []Snapshot, q Query) (Result, error) {
	if q.Limit == 0 {
		q.Limit = defaultQueryLimit
	}
	if q.Limit < 1 || q.Limit > maxQueryLimit || q.Offset < 0 {
		return Result{}, fmt.Errorf("limit must be 1..%d and offset non-negative", maxQueryLimit)
	}
	if q.Order != "" && q.Order != "asc" && q.Order != "desc" {
		return Result{}, errors.New("order must be asc or desc")
	}
	cmp, err := less(q.Sort)
	if err != nil {
		return Result{}, err
	}
	w, err := parseWindow(q)
	if err != nil {
		return Result{}, err
	}
	hits := []Hit{}
	for _, snap := range snaps {
		for _, h := range snap.Messages {
			if matches(q, w, snap.Folder, h) {
				hits = append(hits, Hit{Folder: snap.Folder, UIDValidity: snap.UIDValidity, Header: h})
			}
		}
	}
	descending := q.Order != "asc"
	sort.SliceStable(hits, func(i, j int) bool {
		if descending {
			return cmp(hits[j], hits[i])
		}
		return cmp(hits[i], hits[j])
	})
	result := Result{Total: len(hits), Results: []Hit{}}
	if q.Offset >= len(hits) {
		return result, nil
	}
	end := min(q.Offset+q.Limit, len(hits))
	result.Results = hits[q.Offset:end]
	result.HasMore = end < len(hits)
	return result, nil
}

// Search queries every indexed folder of an account.
func (s Store) Search(account string, q Query) (Result, error) {
	snaps, err := s.All(account)
	if err != nil {
		return Result{}, err
	}
	return query(snaps, q)
}
