package index

import (
	"context"
	"errors"
	"os"
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
func (s Store) SyncStep(ctx context.Context, a config.Account, folder string, maxNew int, fetch Fetcher) (status FolderStatus, err error) {
	unlock, err := s.lockFolder(a.Alias, folder)
	if err != nil {
		return FolderStatus{}, err
	}
	defer func() {
		if unlockErr := unlock(); unlockErr != nil && err == nil {
			err = unlockErr
		}
	}()
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

// staleSyncLock is how old a lock must be before it is treated as left over
// by a crashed process; one sync step takes far less.
const staleSyncLock = 10 * time.Minute

// lockFolder serializes sync steps of one folder across processes, so that
// two writers cannot overwrite each other's snapshot.
func (s Store) lockFolder(account, folder string) (func() error, error) {
	path := s.path(account, folder) + ".lock"
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			if closeErr := f.Close(); closeErr != nil {
				_ = os.Remove(path)
				return nil, errors.New("cannot create index lock")
			}
			return func() error {
				if err := os.Remove(path); err != nil {
					return errors.New("index updated but its lock could not be removed; it expires after 10 minutes")
				}
				return nil
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, errors.New("cannot create index lock")
		}
		info, statErr := os.Stat(path)
		if statErr != nil || time.Since(info.ModTime()) < staleSyncLock {
			return nil, mail.NewTemporaryError("folder sync already in progress; try again later")
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("cannot remove stale index lock")
		}
	}
	return nil, mail.NewTemporaryError("folder sync already in progress; try again later")
}
