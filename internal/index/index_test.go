package index

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ales/yahoo-mail-mcp/internal/config"
	"github.com/ales/yahoo-mail-mcp/internal/mail"
	"github.com/emersion/go-imap/v2"
)

func header(uid uint32, from, subject string, day int, flags ...imap.Flag) mail.Header {
	return mail.Header{UID: uid, From: from, Subject: subject, Date: time.Date(2026, 1, day, 0, 0, 0, 0, time.UTC), Size: int64(uid) * 100, Flags: flags}
}

func uids(entries []mail.Header) []uint32 {
	out := []uint32{}
	for _, e := range entries {
		out = append(out, e.UID)
	}
	return out
}

func TestMergeAddsNewHeadersAndKeepsIncompleteSnapshot(t *testing.T) {
	base := Snapshot{Folder: "INBOX", UIDValidity: 7, NextUID: 3, Messages: []mail.Header{header(1, "a@x", "one", 1), header(2, "b@x", "two", 2)}}
	delta := mail.FolderDelta{UIDValidity: 7, NextUID: 5, New: []mail.Header{header(3, "c@x", "three", 3), header(4, "d@x", "four", 4)}}
	got := merge(base, delta)
	if !reflect.DeepEqual(uids(got.Messages), []uint32{1, 2, 3, 4}) || got.NextUID != 5 || got.Complete {
		t.Fatalf("merge: uids=%v next=%d complete=%t", uids(got.Messages), got.NextUID, got.Complete)
	}
	if len(base.Messages) != 2 {
		t.Fatal("merge mutated its input snapshot")
	}
}

func TestMergeCompletePassPrunesExpungedAndUpdatesFlags(t *testing.T) {
	base := Snapshot{Folder: "INBOX", UIDValidity: 7, NextUID: 4, Messages: []mail.Header{header(1, "a@x", "one", 1), header(2, "b@x", "two", 2), header(3, "c@x", "three", 3)}}
	delta := mail.FolderDelta{UIDValidity: 7, NextUID: 4, Complete: true, Flags: map[uint32][]imap.Flag{1: {imap.FlagSeen}, 3: {}}}
	got := merge(base, delta)
	if !reflect.DeepEqual(uids(got.Messages), []uint32{1, 3}) || !got.Complete {
		t.Fatalf("prune: uids=%v complete=%t", uids(got.Messages), got.Complete)
	}
	if !reflect.DeepEqual(got.Messages[0].Flags, []imap.Flag{imap.FlagSeen}) {
		t.Fatalf("flags not updated: %v", got.Messages[0].Flags)
	}
}

func TestMergeResetDiscardsSnapshotAfterUIDValidityChange(t *testing.T) {
	base := Snapshot{Folder: "INBOX", UIDValidity: 7, NextUID: 3, Complete: true, Messages: []mail.Header{header(1, "a@x", "one", 1), header(2, "b@x", "two", 2)}}
	delta := mail.FolderDelta{UIDValidity: 9, NextUID: 2, Reset: true, New: []mail.Header{header(1, "z@x", "new", 5)}}
	got := merge(base, delta)
	if got.UIDValidity != 9 || !reflect.DeepEqual(uids(got.Messages), []uint32{1}) || got.Messages[0].Subject != "new" || got.Complete {
		t.Fatalf("reset: %+v", got)
	}
}

func TestStoreRoundTripIsPrivateAndPerFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "index")
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("index dir mode: %v, %v", info, err)
	}
	snap := Snapshot{Folder: "Work/Projects", UIDValidity: 3, NextUID: 2, Messages: []mail.Header{header(1, "a@x", "one", 1)}}
	if err := store.Save("personal", snap); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("personal", "Work/Projects")
	if err != nil || loaded.UIDValidity != 3 || len(loaded.Messages) != 1 {
		t.Fatalf("load: %+v, %v", loaded, err)
	}
	empty, err := store.Load("personal", "INBOX")
	if err != nil || empty.Folder != "INBOX" || len(empty.Messages) != 0 {
		t.Fatalf("missing folder snapshot: %+v, %v", empty, err)
	}
	other, err := store.All("other")
	if err != nil || len(other) != 0 {
		t.Fatalf("snapshots leaked across accounts: %+v, %v", other, err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("files: %v, %v", files, err)
	}
	if fi, _ := files[0].Info(); fi.Mode().Perm() != 0600 {
		t.Fatalf("snapshot file mode %v", fi.Mode().Perm())
	}
}

func TestQueryFiltersAcrossFoldersAndSortsGlobally(t *testing.T) {
	snaps := []Snapshot{
		{Folder: "INBOX", Messages: []mail.Header{header(1, "alice@x", "Invoice March", 3, imap.FlagSeen), header(2, "bob@x", "Lunch", 1)}},
		{Folder: "Archive", Messages: []mail.Header{header(5, "alice@x", "Invoice January", 2), header(6, "carol@x", "Invoice Feb", 4, imap.FlagFlagged)}},
	}
	got, err := query(snaps, Query{Subject: "invoice", Sort: "date", Order: "asc", Limit: 10})
	if err != nil || got.Total != 3 || !reflect.DeepEqual(resultUIDs(got), []uint32{5, 1, 6}) || got.Results[0].Folder != "Archive" {
		t.Fatalf("subject search sorted by date: %+v, %v", got, err)
	}
	got, err = query(snaps, Query{From: "ALICE", Unread: true, Limit: 10})
	if err != nil || !reflect.DeepEqual(resultUIDs(got), []uint32{5}) {
		t.Fatalf("from + unread: %+v, %v", got, err)
	}
	got, err = query(snaps, Query{Flagged: true, Limit: 10})
	if err != nil || !reflect.DeepEqual(resultUIDs(got), []uint32{6}) {
		t.Fatalf("flagged: %+v, %v", got, err)
	}
	got, err = query(snaps, Query{Sort: "date", Order: "desc", Offset: 1, Limit: 2})
	if err != nil || got.Total != 4 || !reflect.DeepEqual(resultUIDs(got), []uint32{1, 5}) || !got.HasMore {
		t.Fatalf("paging: %+v, %v", got, err)
	}
	if _, err := query(snaps, Query{Sort: "nonsense", Limit: 10}); err == nil {
		t.Fatal("unknown sort accepted")
	}
	if _, err := query(snaps, Query{Limit: maxQueryLimit + 1}); err == nil {
		t.Fatal("oversized page accepted")
	}
}

func resultUIDs(r Result) []uint32 {
	out := []uint32{}
	for _, m := range r.Results {
		out = append(out, m.UID)
	}
	return out
}

func TestSyncStepPersistsProgressUntilComplete(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "index"))
	if err != nil {
		t.Fatal(err)
	}
	a := config.Account{Alias: "personal"}
	var requests []uint32
	fetch := func(_ context.Context, _ config.Account, folder string, validity, from uint32, maxNew int) (mail.FolderDelta, error) {
		requests = append(requests, from)
		if from <= 1 {
			return mail.FolderDelta{UIDValidity: 4, NextUID: 3, New: []mail.Header{header(1, "a@x", "one", 1), header(2, "b@x", "two", 2)}}, nil
		}
		return mail.FolderDelta{UIDValidity: 4, NextUID: 4, Complete: true, New: []mail.Header{header(3, "c@x", "three", 3)}, Flags: map[uint32][]imap.Flag{1: {}, 3: {}}}, nil
	}
	first, err := store.SyncStep(context.Background(), a, "INBOX", 2, fetch)
	if err != nil || first.Complete || first.Messages != 2 {
		t.Fatalf("first step: %+v, %v", first, err)
	}
	second, err := store.SyncStep(context.Background(), a, "INBOX", 2, fetch)
	if err != nil || !second.Complete || second.Messages != 2 || second.SyncedAt.IsZero() {
		t.Fatalf("second step: %+v, %v", second, err)
	}
	if !reflect.DeepEqual(requests, []uint32{0, 3}) {
		t.Fatalf("resume UIDs: %v", requests)
	}
	status, err := store.Status("personal")
	if err != nil || len(status) != 1 || status[0].Folder != "INBOX" {
		t.Fatalf("status: %+v, %v", status, err)
	}
}

func TestSyncStepRefusesConcurrentSyncAndRecoversStaleLock(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "index"))
	if err != nil {
		t.Fatal(err)
	}
	a := config.Account{Alias: "personal"}
	calls := 0
	fetch := func(context.Context, config.Account, string, uint32, uint32, int) (mail.FolderDelta, error) {
		calls++
		return mail.FolderDelta{UIDValidity: 1, NextUID: 1, Complete: true, Flags: map[uint32][]imap.Flag{}}, nil
	}
	lock := store.path("personal", "INBOX") + ".lock"
	if err := os.WriteFile(lock, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SyncStep(context.Background(), a, "INBOX", 10, fetch); err == nil || !mail.IsTemporary(err) || calls != 0 {
		t.Fatalf("concurrent sync: err=%v temporary=%t calls=%d", err, mail.IsTemporary(err), calls)
	}
	old := time.Now().Add(-staleSyncLock - time.Minute)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SyncStep(context.Background(), a, "INBOX", 10, fetch); err != nil || calls != 1 {
		t.Fatalf("stale lock not recovered: %v calls=%d", err, calls)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("lock left behind: %v", err)
	}
}
