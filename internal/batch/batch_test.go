package batch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ales/yahoo-mail-mcp/internal/config"
	"github.com/ales/yahoo-mail-mcp/internal/mail"
	"github.com/emersion/go-imap/v2"
)

func TestBatchPlanValidationAndReplay(t *testing.T) {
	base := Input{Account: "personal", Folder: "INBOX", UIDValidity: 7, UIDs: []uint32{5, 6}, Action: "move", Target: "Archive"}
	if err := validate(base); err != nil {
		t.Fatal(err)
	}
	bad := base
	bad.UIDs = []uint32{5, 5}
	if err := validate(bad); err == nil {
		t.Fatal("duplicate UID accepted")
	}
	bad = base
	bad.Action = "flag"
	if err := validate(bad); err == nil {
		t.Fatal("flag operation accepted a destination")
	}
	dir := filepath.Join(t.TempDir(), "journal")
	if err := privateDir(dir); err != nil {
		t.Fatal(err)
	}
	token := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	state := record{Input: base, ExpiresAt: time.Now().Add(time.Minute), Executed: true, Items: []Item{{UID: 5, Status: "completed"}, {UID: 6, Status: "uncertain"}}}
	if err := save(filepath.Join(dir, "batch-"+token+".json"), state, true); err != nil {
		t.Fatal(err)
	}
	a := config.Account{Alias: "personal", AllowOrganize: true}
	if _, err := Execute(context.Background(), a, dir, token, false); err == nil {
		t.Fatal("batch executed without confirmation")
	}
	out, err := Execute(context.Background(), a, dir, token, true)
	if err != nil || out.Status != "partial_or_uncertain" || out.Items[1].Status != "uncertain" {
		t.Fatalf("journal replay changed outcome: %+v, %v", out, err)
	}
	if _, err := Execute(context.Background(), config.Account{Alias: "other", AllowOrganize: true}, dir, token, true); err == nil {
		t.Fatal("cross-account token accepted")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("lock file was left behind: %v, %v", entries, err)
	}
}

func TestBatchPrepareExecuteAndReplay(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	a := config.Account{Alias: "personal", AllowOrganize: true}
	input := Input{Account: a.Alias, Folder: "INBOX", UIDValidity: 7, UIDs: []uint32{5, 6}, Action: "move", Target: "Archive"}
	var moved []uint32
	ops := mailOperations{
		status: func(_ context.Context, _ config.Account, folder string) (mail.FolderInfo, error) {
			if folder != "Archive" {
				t.Fatalf("wrong destination: %s", folder)
			}
			return mail.FolderInfo{}, nil
		},
		capabilities: func(context.Context, config.Account) ([]string, error) { return []string{"MOVE"}, nil },
		getRange: func(_ context.Context, _ config.Account, folder string, uid, validity uint32, offset int64, limit int) (mail.Body, error) {
			if folder != "INBOX" || validity != 7 || offset != 0 || limit != 1 {
				t.Fatalf("wrong preview reference: %s %d %d %d %d", folder, uid, validity, offset, limit)
			}
			return mail.Body{Message: mail.Message{UID: uid, UIDValidity: validity, Subject: "preview"}}, nil
		},
		copyOrMove: func(_ context.Context, _ config.Account, folder, target string, uid, validity uint32, move bool) (any, error) {
			if folder != "INBOX" || target != "Archive" || validity != 7 || !move {
				t.Fatalf("wrong move arguments: %s %s %d %d %v", folder, target, uid, validity, move)
			}
			moved = append(moved, uid)
			return nil, nil
		},
	}
	preview, err := prepare(context.Background(), a, dir, input, ops)
	if err != nil || len(preview.Messages) != 2 || preview.Messages[0].UID != 5 || preview.Messages[1].UID != 6 {
		t.Fatalf("preview: %+v, %v", preview, err)
	}
	if _, err := execute(context.Background(), a, dir, preview.Token, false, ops); err == nil {
		t.Fatal("batch executed without confirmation")
	}
	out, err := execute(context.Background(), a, dir, preview.Token, true, ops)
	if err != nil || out.Status != "completed" || !reflect.DeepEqual(moved, []uint32{5, 6}) {
		t.Fatalf("execution: %+v, moved=%v, err=%v", out, moved, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "batch-"+preview.Token+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var state record
	if err := json.Unmarshal(data, &state); err != nil || !state.Executed || state.Items[0].Status != "completed" || state.Items[1].Status != "completed" {
		t.Fatalf("persisted state: %+v, %v", state, err)
	}
	out, err = execute(context.Background(), a, dir, preview.Token, true, ops)
	if err != nil || out.Status != "completed" || len(moved) != 2 {
		t.Fatalf("replay repeated move: %+v, moved=%v, err=%v", out, moved, err)
	}
}

func TestBatchPartialFlagFailureIsNotRetried(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	a := config.Account{Alias: "personal", AllowOrganize: true}
	input := Input{Account: a.Alias, Folder: "INBOX", UIDValidity: 9, UIDs: []uint32{11, 12}, Action: "mark_read"}
	calls := []uint32{}
	ops := mailOperations{
		getRange: func(_ context.Context, _ config.Account, _ string, uid, validity uint32, _ int64, _ int) (mail.Body, error) {
			return mail.Body{Message: mail.Message{UID: uid, UIDValidity: validity}}, nil
		},
		changeFlag: func(_ context.Context, _ config.Account, _ string, uid, validity uint32, flag imap.Flag, enabled bool) error {
			if validity != 9 || flag != imap.FlagSeen || !enabled {
				t.Fatal("incorrect flag operation")
			}
			calls = append(calls, uid)
			if uid == 12 {
				return errors.New("connection lost after STORE")
			}
			return nil
		},
	}
	preview, err := prepare(context.Background(), a, dir, input, ops)
	if err != nil {
		t.Fatal(err)
	}
	out, err := execute(context.Background(), a, dir, preview.Token, true, ops)
	if err != nil || out.Status != "partial_or_uncertain" || out.Items[0].Status != "completed" || out.Items[1].Status != "uncertain" {
		t.Fatalf("partial result: %+v, %v", out, err)
	}
	_, err = execute(context.Background(), a, dir, preview.Token, true, ops)
	if err != nil || !reflect.DeepEqual(calls, []uint32{11, 12}) {
		t.Fatalf("uncertain flag operation repeated: %v, %v", calls, err)
	}
}

func TestBatchPersistsUncertainBeforeFailedOperation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YAHOO_MCP_PASSWORD_BATCH", "")
	dir := filepath.Join(t.TempDir(), "journal")
	if err := privateDir(dir); err != nil {
		t.Fatal(err)
	}
	token := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	state := record{Input: Input{Account: "batch", Folder: "INBOX", UIDValidity: 4, UIDs: []uint32{8}, Action: "flag"}, ExpiresAt: time.Now().Add(time.Minute), Items: []Item{{UID: 8, Status: "pending"}}}
	path := filepath.Join(dir, "batch-"+token+".json")
	if err := save(path, state, true); err != nil {
		t.Fatal(err)
	}
	a := config.Account{Alias: "batch", AllowOrganize: true}
	out, err := Execute(context.Background(), a, dir, token, true)
	if err != nil || out.Status != "partial_or_uncertain" || out.Items[0].Status != "uncertain" {
		t.Fatalf("failed operation was not journaled as uncertain: %+v, %v", out, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted record
	if err := json.Unmarshal(data, &persisted); err != nil || !persisted.Executed || persisted.Items[0].Status != "uncertain" {
		t.Fatalf("unsafe persisted batch state: %+v, %v", persisted, err)
	}
	other, err := Execute(context.Background(), a, dir, token, true)
	if err != nil || other.Items[0].Status != "uncertain" {
		t.Fatalf("replayed uncertain operation: %+v, %v", other, err)
	}
}

func TestBatchConcurrentExecuteUsesOneTokenOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	a := config.Account{Alias: "personal", AllowOrganize: true}
	in := Input{Account: a.Alias, Folder: "INBOX", UIDValidity: 5, UIDs: []uint32{10}, Action: "flag"}
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	ops := mailOperations{
		getRange: func(context.Context, config.Account, string, uint32, uint32, int64, int) (mail.Body, error) {
			return mail.Body{Message: mail.Message{UID: 10, UIDValidity: 5}}, nil
		},
		changeFlag: func(context.Context, config.Account, string, uint32, uint32, imap.Flag, bool) error {
			calls.Add(1)
			close(started)
			<-release
			return nil
		},
	}
	preview, err := prepare(context.Background(), a, dir, in, ops)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		out Outcome
		err error
	}
	firstDone := make(chan result, 1)
	go func() {
		out, err := execute(context.Background(), a, dir, preview.Token, true, ops)
		firstDone <- result{out, err}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("first execution did not reach operation")
	}
	_, concurrentErr := execute(context.Background(), a, dir, preview.Token, true, ops)
	if concurrentErr == nil || !strings.Contains(concurrentErr.Error(), "locked") || calls.Load() != 1 {
		close(release)
		t.Fatalf("concurrent execution was not rejected: calls=%d, err=%v", calls.Load(), concurrentErr)
	}
	close(release)
	first := <-firstDone
	if first.err != nil || first.out.Status != "completed" {
		t.Fatalf("first execution: %+v, %v", first.out, first.err)
	}
	replayed, err := execute(context.Background(), a, dir, preview.Token, true, ops)
	if err != nil || replayed.Status != "completed" || calls.Load() != 1 {
		t.Fatalf("replay performed another operation: %+v, calls=%d, err=%v", replayed, calls.Load(), err)
	}
}

func TestBatchCancellationLeavesRemainingItemsPending(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	a := config.Account{Alias: "personal", AllowOrganize: true}
	in := Input{Account: a.Alias, Folder: "INBOX", UIDValidity: 5, UIDs: []uint32{10, 11}, Action: "flag"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls []uint32
	ops := mailOperations{
		getRange: func(_ context.Context, _ config.Account, _ string, uid, validity uint32, _ int64, _ int) (mail.Body, error) {
			return mail.Body{Message: mail.Message{UID: uid, UIDValidity: validity}}, nil
		},
		changeFlag: func(_ context.Context, _ config.Account, _ string, uid, _ uint32, _ imap.Flag, _ bool) error {
			calls = append(calls, uid)
			cancel()
			return nil
		},
	}
	preview, err := prepare(ctx, a, dir, in, ops)
	if err != nil {
		t.Fatal(err)
	}
	out, err := execute(ctx, a, dir, preview.Token, true, ops)
	if err != nil || out.Status != "partial_or_uncertain" || out.Items[0].Status != "completed" || out.Items[1].Status != "pending" || !reflect.DeepEqual(calls, []uint32{10}) {
		t.Fatalf("cancelled execution: %+v, calls=%v, err=%v", out, calls, err)
	}
	replayed, err := execute(context.Background(), a, dir, preview.Token, true, ops)
	if err != nil || replayed.Items[1].Status != "pending" || !reflect.DeepEqual(calls, []uint32{10}) {
		t.Fatalf("replay ran pending item: %+v, calls=%v, err=%v", replayed, calls, err)
	}
}
