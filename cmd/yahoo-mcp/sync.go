package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/ales/yahoo-mail-mcp/internal/config"
	"github.com/ales/yahoo-mail-mcp/internal/index"
	"github.com/ales/yahoo-mail-mcp/internal/mail"
	"github.com/emersion/go-imap/v2"
)

const (
	syncHeadersPerStep = 1000
	syncStepTimeout    = 2 * time.Minute
	maxSyncSteps       = 10000
)

// runSync indexes the given folders, or every selectable folder, until each is complete.
func runSync(cfg config.Config, args []string) error {
	if len(args) < 1 {
		return errors.New("usage: yahoo-mcp sync <alias> [folder...]")
	}
	account, ok := cfg.Account(args[0])
	if !ok {
		return errors.New("unknown account alias")
	}
	if cfg.IndexDir == "" {
		return errors.New("index_dir is not configured")
	}
	store, err := index.Open(cfg.IndexDir)
	if err != nil {
		return err
	}
	folders := args[1:]
	if len(folders) == 0 {
		if folders, err = selectableFolders(account); err != nil {
			return err
		}
	}
	for _, folder := range folders {
		if err := syncFolder(store, account, folder, mail.FetchFolderDelta, time.Sleep); err != nil {
			return fmt.Errorf("%s: %w", folder, err)
		}
	}
	return nil
}

func selectableFolders(a config.Account) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), syncStepTimeout)
	defer cancel()
	folders, err := mail.ListFolders(ctx, a)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, f := range folders {
		if !slices.Contains(f.Attributes, imap.MailboxAttrNoSelect) && !slices.Contains(f.Attributes, imap.MailboxAttrNonExistent) {
			names = append(names, f.Name)
		}
	}
	return names, nil
}

// syncRetryDelays spaces out retries of a failed read-only step; Yahoo may
// briefly reject repeated logins.
var syncRetryDelays = []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second}

func syncFolder(store index.Store, a config.Account, folder string, fetch index.Fetcher, sleep func(time.Duration)) error {
	for range maxSyncSteps {
		status, err := syncStepWithRetry(store, a, folder, fetch, sleep)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s: %d messages indexed\n", folder, status.Messages)
		if status.Complete {
			return nil
		}
	}
	return errors.New("synchronization did not complete within the step limit")
}

func syncStepWithRetry(store index.Store, a config.Account, folder string, fetch index.Fetcher, sleep func(time.Duration)) (index.FolderStatus, error) {
	var lastErr error
	for attempt := 0; attempt <= len(syncRetryDelays); attempt++ {
		if attempt > 0 {
			delay := syncRetryDelays[attempt-1]
			fmt.Fprintf(os.Stderr, "%s: %v; retrying in %s\n", folder, lastErr, delay)
			sleep(delay)
		}
		ctx, cancel := context.WithTimeout(context.Background(), syncStepTimeout)
		status, err := store.SyncStep(ctx, a, folder, syncHeadersPerStep, fetch)
		cancel()
		if err == nil {
			return status, nil
		}
		if !mail.IsTemporary(err) {
			return index.FolderStatus{}, err
		}
		lastErr = err
	}
	return index.FolderStatus{}, lastErr
}
