package server

import (
	"context"
	"errors"
	"github.com/ales/yahoo-mail-mcp/internal/config"
	"github.com/ales/yahoo-mail-mcp/internal/send"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"testing"
	"time"
)

func TestToolCallLimiterRejectsBusyServer(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	handler := toolCallLimiter(1, time.Second)(func(_ context.Context, method string, _ mcp.Request) (mcp.Result, error) {
		if method != "tools/call" {
			return nil, nil
		}
		entered <- struct{}{}
		<-release
		return nil, nil
	})
	done := make(chan error, 1)
	go func() { _, err := handler(context.Background(), "tools/call", nil); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first tool call did not enter")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := handler(ctx, "tools/call", nil); err == nil || err.Error() != "MCP tool capacity unavailable" || len(entered) != 0 {
		t.Fatalf("busy tool call was not rejected: %v", err)
	}
	if _, err := handler(ctx, "ping", nil); err != nil {
		t.Fatalf("non-tool method was limited: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := handler(canceled, "tools/call", nil); err == nil || err.Error() != "MCP tool request canceled" {
		t.Fatalf("canceled tool call entered: %v", err)
	}
}

func TestDraftSendWorkflow(t *testing.T) {
	account := config.Account{Alias: "personal", Address: "account@example.com", AllowSend: true, AllowOrganize: true}
	raw, err := send.ComposeDraft(account.Address, send.Input{To: []string{account.Address}, Subject: "Fixture draft", Text: "Fixture body"})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := send.PrepareStoredDraft(account.Address, raw, "draft-workflow-123", nil)
	if err != nil {
		t.Fatal(err)
	}
	in := DraftSendInput{Account: account.Alias, UID: 17, UIDValidity: 5, IdempotencyKey: "draft-workflow-123", ExpectedDigest: preview.Digest}
	for _, tc := range []struct {
		name        string
		digest      string
		sendReceipt send.Receipt
		sendErr     error
		deleteErr   error
		wantStatus  string
		wantSend    int
		wantDelete  int
		wantError   bool
	}{
		{name: "confirmed", sendReceipt: send.Receipt{Status: "confirmed"}, wantStatus: "confirmed", wantSend: 1, wantDelete: 1},
		{name: "preview changed", digest: "wrong", wantError: true},
		{name: "SMTP uncertain", sendReceipt: send.Receipt{Status: "uncertain"}, wantStatus: "uncertain", wantSend: 1},
		{name: "SMTP rejected", sendErr: errors.New("SMTP rejected"), wantSend: 1, wantError: true},
		{name: "draft retained", sendReceipt: send.Receipt{Status: "confirmed"}, deleteErr: errors.New("IMAP unavailable"), wantStatus: "confirmed_draft_retained", wantSend: 1, wantDelete: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := in
			if tc.digest != "" {
				input.ExpectedDigest = tc.digest
			}
			sent, deleted := 0, 0
			ops := draftSendOperations{
				load: func(_ context.Context, _ config.Account, uid, validity uint32) ([]byte, error) {
					if uid != 17 || validity != 5 {
						t.Fatalf("wrong draft reference: %d, %d", uid, validity)
					}
					return raw, nil
				},
				send: func(_ context.Context, _ config.Account, _ string, gotRaw []byte, key string, _ []string) (send.Receipt, error) {
					sent++
					if string(gotRaw) != string(raw) || key != in.IdempotencyKey {
						t.Fatal("SMTP received different draft MIME or key")
					}
					return tc.sendReceipt, tc.sendErr
				},
				delete: func(_ context.Context, _ config.Account, uid, validity uint32) error {
					deleted++
					if uid != 17 || validity != 5 {
						t.Fatal("wrong draft deleted")
					}
					return tc.deleteErr
				},
			}
			receipt, err := sendDraft(context.Background(), account, t.TempDir(), input, ops)
			if (err != nil) != tc.wantError || receipt.Status != tc.wantStatus || sent != tc.wantSend || deleted != tc.wantDelete {
				t.Fatalf("receipt=%+v err=%v sent=%d deleted=%d", receipt, err, sent, deleted)
			}
		})
	}
}

func TestDiscoveryAndPermission(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(config.Config{Accounts: []config.Account{{Alias: "personal", Address: "account@example.com"}}})
	st, ct := mcp.NewInMemoryTransports()
	if _, err := s.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	names := map[string]bool{}
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		names[tool.Name] = true
	}
	for _, name := range []string{"account_list", "account_test_connection", "account_test_smtp", "folder_list", "folder_tree", "email_list", "email_search", "email_next_unread", "email_get", "email_reply_context", "email_reply_prepare", "email_reply_send", "email_forward_prepare", "email_forward_send", "attachment_save", "email_move", "email_copy", "email_trash", "email_batch_prepare", "email_batch_execute", "email_mark_read", "draft_create", "draft_list", "draft_get", "draft_update", "draft_delete", "draft_prepare_send", "draft_send", "email_prepare", "email_send"} {
		if !names[name] {
			t.Errorf("missing tool %s", name)
		}
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "email_move", Arguments: map[string]any{"account": "personal", "folder": "INBOX", "target_folder": "Archive", "uid": 1, "uid_validity": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("organize operation was not denied")
	}
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "email_trash", Arguments: map[string]any{"account": "personal", "folder": "INBOX", "uid": 1, "uid_validity": 1}})
	if err != nil || !res.IsError {
		t.Fatalf("Trash move was not denied: %+v, %v", res, err)
	}
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "draft_create", Arguments: map[string]any{"account": "personal", "to": []string{"other@example.com"}, "subject": "hello", "text": "body"}})
	if err != nil || !res.IsError {
		t.Fatalf("draft creation was not denied: %+v, %v", res, err)
	}
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "email_prepare", Arguments: map[string]any{"account": "personal", "idempotency_key": "send-12345678", "to": []string{"other@example.com"}, "subject": "hello", "text": "body"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatal("message preview failed")
	}
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "email_send", Arguments: map[string]any{"account": "personal", "idempotency_key": "send-12345678", "to": []string{"other@example.com"}, "subject": "hello", "text": "body"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("send operation was not denied")
	}
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "email_reply_send", Arguments: map[string]any{"account": "personal", "folder": "INBOX", "uid": 1, "uid_validity": 1, "idempotency_key": "reply-12345678", "expected_digest": "fake", "text": "answer"}})
	if err != nil || !res.IsError {
		t.Fatalf("reply send was not denied: %+v, %v", res, err)
	}
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "email_forward_send", Arguments: map[string]any{"account": "personal", "folder": "INBOX", "uid": 1, "uid_validity": 1, "idempotency_key": "forward-12345678", "expected_digest": "fake", "to": []string{"other@example.com"}}})
	if err != nil || !res.IsError {
		t.Fatalf("forward send was not denied: %+v, %v", res, err)
	}
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "draft_send", Arguments: map[string]any{"account": "personal", "uid": 1, "uid_validity": 1, "idempotency_key": "draft-send-123", "expected_digest": "fake"}})
	if err != nil || !res.IsError {
		t.Fatalf("draft send was not denied: %+v, %v", res, err)
	}
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "email_batch_prepare", Arguments: map[string]any{"account": "personal", "folder": "INBOX", "uid_validity": 1, "uids": []int{1}, "action": "flag"}})
	if err != nil || !res.IsError {
		t.Fatalf("batch prepare was not denied: %+v, %v", res, err)
	}
}

func TestTransferOutcomeCarriesDestinationReference(t *testing.T) {
	copy := transferOutcome(&imap.CopyData{UIDValidity: 17, DestUIDs: imap.UIDSetNum(42)})
	if copy.Status != "completed" || copy.DestinationUID != 42 || copy.DestinationUIDValidity != 17 {
		t.Fatalf("copy mapping lost: %+v", copy)
	}
	move := transferOutcome(&imapclient.MoveData{UIDValidity: 19, DestUIDs: imap.UIDSetNum(53)})
	if move.DestinationUID != 53 || move.DestinationUIDValidity != 19 {
		t.Fatalf("move mapping lost: %+v", move)
	}
	withoutMapping := transferOutcome(&imapclient.MoveData{})
	if withoutMapping.DestinationUID != 0 || withoutMapping.DestinationUIDValidity != 0 {
		t.Fatalf("invented destination reference: %+v", withoutMapping)
	}
}
