package index

import (
	"context"
	"time"

	"github.com/ales/yahoo-mail-mcp/internal/config"
	"github.com/ales/yahoo-mail-mcp/internal/mail"
)

// Fetcher performs one bounded read-only synchronization step against IMAP.
type Fetcher func(ctx context.Context, a config.Account, folder string, knownValidity, fromUID uint32, maxNew int) (mail.FolderDelta, error)

// FolderStatus summarizes one folder snapshot.
type FolderStatus struct {
	Folder      string    `json:"folder"`
	UIDValidity uint32    `json:"uid_validity"`
	Messages    int       `json:"messages"`
	Complete    bool      `json:"complete"`
	SyncedAt    time.Time `json:"synced_at"`
}

func statusOf(s Snapshot) FolderStatus {
	return FolderStatus{Folder: s.Folder, UIDValidity: s.UIDValidity, Messages: len(s.Messages), Complete: s.Complete, SyncedAt: s.SyncedAt}
}

// SyncStep advances one folder snapshot by at most maxNew new headers.
// Repeat it until the returned status is complete.
func (s Store) SyncStep(ctx context.Context, a config.Account, folder string, maxNew int, fetch Fetcher) (FolderStatus, error) {
	snap, err := s.Load(a.Alias, folder)
	if err != nil {
		return FolderStatus{}, err
	}
	delta, err := fetch(ctx, a, folder, snap.UIDValidity, snap.NextUID, maxNew)
	if err != nil {
		return FolderStatus{}, err
	}
	next := merge(snap, delta)
	next.SyncedAt = time.Now().UTC()
	if err := s.Save(a.Alias, next); err != nil {
		return FolderStatus{}, err
	}
	return statusOf(next), nil
}

// Status lists the snapshots stored for an account.
func (s Store) Status(account string) ([]FolderStatus, error) {
	snaps, err := s.All(account)
	if err != nil {
		return nil, err
	}
	out := make([]FolderStatus, 0, len(snaps))
	for _, snap := range snaps {
		out = append(out, statusOf(snap))
	}
	return out, nil
}
