// Package index keeps a private local snapshot of message headers per folder
// so that search and sorting can span a whole mailbox without IMAP round trips.
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ales/yahoo-mail-mcp/internal/mail"
	"github.com/emersion/go-imap/v2"
)

const maxSnapshotBytes = 256 << 20

// Snapshot holds the indexed headers of one folder.
type Snapshot struct {
	Account     string        `json:"account"`
	Folder      string        `json:"folder"`
	UIDValidity uint32        `json:"uid_validity"`
	NextUID     uint32        `json:"next_uid"`
	Complete    bool          `json:"complete"`
	SyncedAt    time.Time     `json:"synced_at"`
	Messages    []mail.Header `json:"messages"`
}

// Store reads and writes snapshots in a private directory.
type Store struct {
	dir string
}

// Open validates or creates the private index directory.
func Open(dir string) (Store, error) {
	if dir == "" || !filepath.IsAbs(dir) {
		return Store{}, errors.New("absolute index_dir is required")
	}
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return Store{}, errors.New("cannot create index directory; parent directory must exist")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return Store{}, errors.New("index directory must be private (0700)")
	}
	return Store{dir: dir}, nil
}

func (s Store) path(account, folder string) string {
	sum := sha256.Sum256([]byte(folder))
	return filepath.Join(s.dir, account+"--"+hex.EncodeToString(sum[:16])+".json")
}

// Load returns the folder snapshot, or an empty one when none exists yet.
func (s Store) Load(account, folder string) (Snapshot, error) {
	snap, err := readSnapshot(s.path(account, folder))
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{Account: account, Folder: folder, Messages: []mail.Header{}}, nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	if snap.Account != account || snap.Folder != folder {
		return Snapshot{}, errors.New("index snapshot does not match its account and folder")
	}
	return snap, nil
}

// All returns every snapshot stored for an account.
func (s Store) All(account string) ([]Snapshot, error) {
	paths, err := filepath.Glob(filepath.Join(s.dir, account+"--*.json"))
	if err != nil {
		return nil, errors.New("cannot list index snapshots")
	}
	sort.Strings(paths)
	snaps := make([]Snapshot, 0, len(paths))
	for _, path := range paths {
		snap, err := readSnapshot(path)
		if err != nil {
			return nil, err
		}
		if snap.Account == account {
			snaps = append(snaps, snap)
		}
	}
	return snaps, nil
}

func readSnapshot(path string) (Snapshot, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Snapshot{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxSnapshotBytes {
		return Snapshot{}, errors.New("index snapshot is not a bounded regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, errors.New("cannot read index snapshot")
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return Snapshot{}, errors.New("index snapshot is corrupt; delete it and sync again")
	}
	return snap, nil
}

// Save replaces the folder snapshot atomically with a 0600 file.
func (s Store) Save(account string, snap Snapshot) error {
	if strings.ContainsAny(account, "/\\\x00") || account == "" {
		return errors.New("invalid account alias")
	}
	snap.Account = account
	encoded, err := json.Marshal(snap)
	if err != nil {
		return errors.New("cannot encode index snapshot")
	}
	tmp, err := os.CreateTemp(s.dir, ".index-*")
	if err != nil {
		return errors.New("cannot write index snapshot")
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		return errors.New("cannot write index snapshot")
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return errors.New("cannot sync index snapshot")
	}
	if err := tmp.Close(); err != nil {
		return errors.New("cannot close index snapshot")
	}
	if err := os.Rename(tmp.Name(), s.path(account, snap.Folder)); err != nil {
		return errors.New("cannot replace index snapshot")
	}
	return nil
}

// merge applies one synchronization step and returns a new snapshot.
func merge(base Snapshot, delta mail.FolderDelta) Snapshot {
	out := base
	out.UIDValidity = delta.UIDValidity
	out.NextUID = delta.NextUID
	out.Complete = delta.Complete
	existing := base.Messages
	if delta.Reset {
		existing = nil
	}
	known := make(map[uint32]bool, len(existing)+len(delta.New))
	messages := make([]mail.Header, 0, len(existing)+len(delta.New))
	for _, h := range existing {
		known[h.UID] = true
		messages = append(messages, h)
	}
	for _, h := range delta.New {
		if !known[h.UID] {
			known[h.UID] = true
			messages = append(messages, h)
		}
	}
	if delta.Complete {
		messages = applyPresence(messages, delta.Flags)
	}
	sort.Slice(messages, func(i, j int) bool { return messages[i].UID < messages[j].UID })
	out.Messages = messages
	return out
}

// applyPresence drops expunged UIDs and refreshes flags from a complete pass.
func applyPresence(messages []mail.Header, present map[uint32][]imap.Flag) []mail.Header {
	kept := make([]mail.Header, 0, len(messages))
	for _, h := range messages {
		flags, ok := present[h.UID]
		if !ok {
			continue
		}
		h.Flags = append([]imap.Flag(nil), flags...)
		kept = append(kept, h)
	}
	return kept
}
