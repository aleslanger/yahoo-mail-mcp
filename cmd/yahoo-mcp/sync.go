package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/aleslanger/yahoo-mail-mcp/internal/config"
	"github.com/aleslanger/yahoo-mail-mcp/internal/index"
	"github.com/aleslanger/yahoo-mail-mcp/internal/mail"
	"github.com/emersion/go-imap/v2"
)

const (
	syncHeadersPerStep = 1000
	syncStepTimeout    = 2 * time.Minute
	maxSyncSteps       = 10000
)

// runSync indexes the given folders, or every selectable folder, until each is complete.
func runSync(ctx context.Context, cfg config.Config, args []string, progress io.Writer) error {
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
		if folders, err = selectableFolders(ctx, account); err != nil {
			return err
		}
	}
	for _, folder := range folders {
		if err := syncFolder(ctx, store, account, folder, mail.FetchFolderDelta, time.Sleep, progress); err != nil {
			return fmt.Errorf("%s: %w", folder, err)
		}
	}
	return nil
}

func selectableFolders(ctx context.Context, a config.Account) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, syncStepTimeout)
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

func syncFolder(ctx context.Context, store index.Store, a config.Account, folder string, fetch index.Fetcher, sleep func(time.Duration), progress io.Writer) error {
	for range maxSyncSteps {
		status, err := syncStepWithRetry(ctx, store, a, folder, fetch, sleep, progress)
		if err != nil {
			return err
		}
		fmt.Fprintf(progress, "%s: %d messages indexed\n", folder, status.Messages)
		if status.Complete {
			return nil
		}
	}
	return errors.New("synchronization did not complete within the step limit")
}

func syncStepWithRetry(ctx context.Context, store index.Store, a config.Account, folder string, fetch index.Fetcher, sleep func(time.Duration), progress io.Writer) (index.FolderStatus, error) {
	var lastErr error
	for attempt := 0; attempt <= len(syncRetryDelays); attempt++ {
		if attempt > 0 {
			delay := syncRetryDelays[attempt-1]
			fmt.Fprintf(progress, "%s: %v; retrying in %s\n", folder, lastErr, delay)
			sleep(delay)
		}
		stepCtx, cancel := context.WithTimeout(ctx, syncStepTimeout)
		status, err := store.SyncStep(stepCtx, a, folder, syncHeadersPerStep, fetch)
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
