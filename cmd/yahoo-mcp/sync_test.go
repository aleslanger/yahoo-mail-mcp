package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/aleslanger/yahoo-mail-mcp/internal/config"
	"github.com/aleslanger/yahoo-mail-mcp/internal/index"
	"github.com/aleslanger/yahoo-mail-mcp/internal/mail"
	"github.com/emersion/go-imap/v2"
)

func TestSyncFolderRetriesTransientReadFailures(t *testing.T) {
	store, err := index.Open(filepath.Join(t.TempDir(), "index"))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	fetch := func(context.Context, config.Account, string, uint32, uint32, int) (mail.FolderDelta, error) {
		calls++
		if calls <= 2 {
			return mail.FolderDelta{}, mail.NewTemporaryError("IMAP authentication failed")
		}
		return mail.FolderDelta{UIDValidity: 1, NextUID: 2, Complete: true, New: []mail.Header{{UID: 1}}, Flags: map[uint32][]imap.Flag{1: {}}}, nil
	}
	var waits []time.Duration
	sleep := func(d time.Duration) { waits = append(waits, d) }
	if err := syncFolder(context.Background(), store, config.Account{Alias: "personal"}, "INBOX", fetch, sleep, io.Discard); err != nil {
		t.Fatalf("sync after transient failures: %v", err)
	}
	if calls != 3 || len(waits) != 2 || waits[1] <= waits[0] {
		t.Fatalf("calls=%d waits=%v", calls, waits)
	}
}

func TestSyncFolderGivesUpAfterRetries(t *testing.T) {
	store, err := index.Open(filepath.Join(t.TempDir(), "index"))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	fetch := func(context.Context, config.Account, string, uint32, uint32, int) (mail.FolderDelta, error) {
		calls++
		return mail.FolderDelta{}, mail.NewTemporaryError("IMAP authentication failed")
	}
	err = syncFolder(context.Background(), store, config.Account{Alias: "personal"}, "INBOX", fetch, func(time.Duration) {}, io.Discard)
	if err == nil || calls != len(syncRetryDelays)+1 {
		t.Fatalf("persistent failure: err=%v calls=%d", err, calls)
	}
}

func TestSyncFolderDoesNotRetryPermanentErrors(t *testing.T) {
	store, err := index.Open(filepath.Join(t.TempDir(), "index"))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	fetch := func(context.Context, config.Account, string, uint32, uint32, int) (mail.FolderDelta, error) {
		calls++
		return mail.FolderDelta{}, errors.New("folder selection failed")
	}
	slept := false
	err = syncFolder(context.Background(), store, config.Account{Alias: "personal"}, "Missing", fetch, func(time.Duration) { slept = true }, io.Discard)
	if err == nil || calls != 1 || slept {
		t.Fatalf("permanent error retried: err=%v calls=%d slept=%t", err, calls, slept)
	}
}
