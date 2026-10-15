package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aleslanger/yahoo-mail-mcp/internal/batch"
	"github.com/aleslanger/yahoo-mail-mcp/internal/config"
	"github.com/aleslanger/yahoo-mail-mcp/internal/imaptest"
	"github.com/aleslanger/yahoo-mail-mcp/internal/index"
	"github.com/aleslanger/yahoo-mail-mcp/internal/mail"
	"github.com/aleslanger/yahoo-mail-mcp/internal/send"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const fixtureAddress = "account@example.com"

const plainFixture = "From: Alice <alice@example.com>\r\nTo: account@example.com\r\nCc: carol@example.com\r\nSubject: Quarterly report\r\nMessage-ID: <report-1@example.com>\r\nDate: Mon, 12 Oct 2026 10:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nNumbers attached.\r\n"

const attachmentFixture = "From: Bob <bob@example.com>\r\nTo: account@example.com\r\nSubject: Invoice\r\nMessage-ID: <invoice-1@example.com>\r\nDate: Mon, 12 Oct 2026 11:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b1\r\n\r\n--b1\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nSee the attachment.\r\n--b1\r\nContent-Type: text/plain\r\nContent-Disposition: attachment; filename=\"invoice.txt\"\r\nContent-Transfer-Encoding: base64\r\n\r\naW52b2ljZSBib2R5\r\n--b1--\r\n"

type imapFixture struct {
	session *mcp.ClientSession
	server  *imaptest.Server
	cfg     config.Config
}

func privateDir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// newIMAPFixture serves every tool against an in-process IMAP server with all
// write permissions except sending enabled.
func newIMAPFixture(t *testing.T) imapFixture {
	t.Helper()
	srv := imaptest.New(t, imaptest.Options{Alias: "personal", Address: fixtureAddress, Password: "fixture-secret", Folders: []string{"INBOX", "Archive", "Drafts", "Trash"}, Move: true})
	a := srv.Account
	a.AllowOrganize, a.AllowDelete = true, true
	cfg := config.Config{
		JournalDir:  privateDir(t, "journal"),
		DownloadDir: privateDir(t, "downloads"),
		IndexDir:    privateDir(t, "index"),
		Accounts:    []config.Account{a},
	}
	ctx := mail.WithDialTLS(context.Background(), srv.Dial)
	return imapFixture{session: connectWithContext(t, ctx, cfg), server: srv, cfg: cfg}
}

func callTool[T any](t *testing.T, session *mcp.ClientSession, name string, args map[string]any) T {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s failed: %s", name, toolText(res))
	}
	var out T
	if err := json.Unmarshal(mustJSON(t, res.StructuredContent), &out); err != nil {
		t.Fatalf("%s output: %v", name, err)
	}
	return out
}

func callToolError(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if !res.IsError {
		t.Fatalf("%s succeeded unexpectedly", name)
	}
	return toolText(res)
}

func toolText(res *mcp.CallToolResult) string {
	parts := []string{}
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func ref(folder string, uid, validity uint32) map[string]any {
	return map[string]any{"account": "personal", "folder": folder, "uid": uid, "uid_validity": validity}
}

func with(base map[string]any, extra map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func TestReadToolsOverIMAP(t *testing.T) {
	f := newIMAPFixture(t)
	plainUID := f.server.Append(t, "INBOX", plainFixture)
	attachUID := f.server.Append(t, "INBOX", attachmentFixture)
	acct := map[string]any{"account": "personal"}

	accounts := callTool[Accounts](t, f.session, "account_list", nil)
	if len(accounts.Accounts) != 1 || !accounts.Accounts[0].AllowDelete || accounts.Accounts[0].AllowSend {
		t.Fatalf("account_list: %+v", accounts)
	}
	if caps := callTool[Caps](t, f.session, "account_capabilities", acct); len(caps.Capabilities) == 0 {
		t.Fatalf("account_capabilities: %+v", caps)
	}
	if check := callTool[ConnectionCheck](t, f.session, "account_test_connection", acct); check.Status != "ok" || check.CapabilityCount == 0 {
		t.Fatalf("account_test_connection: %+v", check)
	}
	if folders := callTool[Folders](t, f.session, "folder_list", acct); len(folders.Folders) != 4 {
		t.Fatalf("folder_list: %+v", folders)
	}
	if tree := callTool[mail.FolderTreePage](t, f.session, "folder_tree", with(acct, map[string]any{"include_counts": true})); tree.CountedFolders != 4 {
		t.Fatalf("folder_tree: %+v", tree)
	}
	status := callTool[mail.FolderInfo](t, f.session, "folder_status", with(acct, map[string]any{"folder": "INBOX"}))
	if status.Messages != 2 {
		t.Fatalf("folder_status: %+v", status)
	}
	validity := status.UIDValidity

	if page := callTool[mail.Page](t, f.session, "email_list", with(acct, map[string]any{"folder": "INBOX"})); len(page.Messages) != 2 {
		t.Fatalf("email_list: %+v", page)
	}
	if page := callTool[mail.Page](t, f.session, "email_search", with(acct, map[string]any{"folder": "INBOX", "subject": "invoice"})); len(page.Messages) != 1 || page.Messages[0].UID != attachUID {
		t.Fatalf("email_search: %+v", page)
	}
	if page := callTool[mail.Page](t, f.session, "email_next_unread", with(acct, map[string]any{"folder": "INBOX"})); len(page.Messages) != 1 || page.Messages[0].UID != attachUID {
		t.Fatalf("email_next_unread: %+v", page)
	}
	body := callTool[mail.Body](t, f.session, "email_get", ref("INBOX", plainUID, validity))
	raw, err := base64.StdEncoding.DecodeString(body.RawBase64)
	if err != nil || !strings.Contains(string(raw), "Numbers attached.") || body.Truncated {
		t.Fatalf("email_get: %+v, %v", body, err)
	}
	parsed := callTool[mail.ParsedMessage](t, f.session, "email_get_body", ref("INBOX", attachUID, validity))
	if !strings.Contains(parsed.Plain, "See the attachment.") || len(parsed.Attachments) != 1 {
		t.Fatalf("email_get_body: %+v", parsed)
	}
	attachmentIndex := parsed.Attachments[0].Index
	if list := callTool[AttachmentList](t, f.session, "attachment_list", ref("INBOX", attachUID, validity)); len(list.Attachments) != 1 {
		t.Fatalf("attachment_list: %+v", list)
	}
	data := callTool[mail.AttachmentData](t, f.session, "attachment_get", with(ref("INBOX", attachUID, validity), map[string]any{"index": attachmentIndex}))
	if content, err := base64.StdEncoding.DecodeString(data.ContentBase64); err != nil || string(content) != "invoice body" {
		t.Fatalf("attachment_get: %+v, %v", data, err)
	}
	saved := callTool[mail.SavedAttachment](t, f.session, "attachment_save", with(ref("INBOX", attachUID, validity), map[string]any{"index": attachmentIndex}))
	if content, err := os.ReadFile(saved.Path); err != nil || string(content) != "invoice body" || filepath.Dir(saved.Path) != f.cfg.DownloadDir {
		t.Fatalf("attachment_save: %+v, %v", saved, err)
	}
}

func TestReplyAndForwardPreviewToolsOverIMAP(t *testing.T) {
	f := newIMAPFixture(t)
	plainUID := f.server.Append(t, "INBOX", plainFixture)
	attachUID := f.server.Append(t, "INBOX", attachmentFixture)
	validity := callTool[mail.FolderInfo](t, f.session, "folder_status", map[string]any{"account": "personal", "folder": "INBOX"}).UIDValidity

	thread := callTool[mail.ReplyContext](t, f.session, "email_reply_context", with(ref("INBOX", plainUID, validity), map[string]any{"reply_all": true}))
	if thread.Subject != "Re: Quarterly report" || thread.InReplyTo != "<report-1@example.com>" || len(thread.Cc) != 1 {
		t.Fatalf("email_reply_context: %+v", thread)
	}
	reply := callTool[send.Preview](t, f.session, "email_reply_prepare", with(ref("INBOX", plainUID, validity), map[string]any{"idempotency_key": "reply-fixture-1", "text": "Thanks", "quote_original": true}))
	if reply.Digest == "" || reply.Subject != "Re: Quarterly report" || len(reply.To) != 1 || reply.To[0] != "alice@example.com" {
		t.Fatalf("email_reply_prepare: %+v", reply)
	}
	if msg := callToolError(t, f.session, "email_reply_prepare", with(ref("INBOX", plainUID, validity), map[string]any{"idempotency_key": "reply-fixture-2", "html": "<p>x</p>", "quote_original": true})); !strings.Contains(msg, "plain text only") {
		t.Fatalf("quoted HTML reply: %s", msg)
	}
	forward := callTool[send.Preview](t, f.session, "email_forward_prepare", with(ref("INBOX", attachUID, validity), map[string]any{"idempotency_key": "forward-fixture-1", "to": []string{"dave@example.com"}, "text": "FYI", "original_attachment_indexes": []int{2}}))
	if forward.Subject != "Fwd: Invoice" || forward.Digest == "" {
		t.Fatalf("email_forward_prepare: %+v", forward)
	}
	for _, tc := range []struct {
		indexes []int
		want    string
	}{
		{[]int{2, 2}, "duplicate original attachment index"},
		{[]int{9}, "original attachment index not found"},
		{[]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}, "at most 10 original attachments"},
	} {
		args := with(ref("INBOX", attachUID, validity), map[string]any{"idempotency_key": "forward-fixture-2", "to": []string{"dave@example.com"}, "original_attachment_indexes": tc.indexes})
		if msg := callToolError(t, f.session, "email_forward_prepare", args); !strings.Contains(msg, tc.want) {
			t.Fatalf("forward %v: %s", tc.indexes, msg)
		}
	}
}

func TestSendToolsRejectMismatchedDigestBeforeSMTP(t *testing.T) {
	srv := imaptest.New(t, imaptest.Options{Alias: "personal", Address: fixtureAddress, Password: "fixture-secret", Folders: []string{"INBOX"}})
	a := srv.Account
	a.AllowSend = true
	a.SMTPHost, a.SMTPPort = "127.0.0.1", 1
	session := connectWithContext(t, mail.WithDialTLS(context.Background(), srv.Dial), config.Config{JournalDir: privateDir(t, "journal"), Accounts: []config.Account{a}})
	uid := srv.Append(t, "INBOX", plainFixture)
	validity := callTool[mail.FolderInfo](t, session, "folder_status", map[string]any{"account": "personal", "folder": "INBOX"}).UIDValidity
	for name, args := range map[string]map[string]any{
		"email_send":         {"account": "personal", "idempotency_key": "send-fixture-1", "expected_digest": "stale", "to": []string{"dave@example.com"}, "subject": "Hi", "text": "Body"},
		"email_reply_send":   with(ref("INBOX", uid, validity), map[string]any{"idempotency_key": "reply-fixture-1", "expected_digest": "stale", "text": "Thanks"}),
		"email_forward_send": with(ref("INBOX", uid, validity), map[string]any{"idempotency_key": "forward-fixture-1", "expected_digest": "stale", "to": []string{"dave@example.com"}}),
	} {
		if msg := callToolError(t, session, name, args); !strings.Contains(msg, "expected_digest does not match") {
			t.Fatalf("%s: %s", name, msg)
		}
	}
	if msg := callToolError(t, session, "account_test_smtp", map[string]any{"account": "personal"}); msg == "" {
		t.Fatal("account_test_smtp reported no error for a closed port")
	}
}

func TestOrganizeToolsOverIMAP(t *testing.T) {
	f := newIMAPFixture(t)
	uid := f.server.Append(t, "INBOX", plainFixture)
	acct := map[string]any{"account": "personal"}
	validity := callTool[mail.FolderInfo](t, f.session, "folder_status", with(acct, map[string]any{"folder": "INBOX"})).UIDValidity
	inbox := ref("INBOX", uid, validity)

	for _, name := range []string{"email_mark_read", "email_flag", "email_unflag", "email_mark_unread"} {
		if out := callTool[Outcome](t, f.session, name, inbox); out.Status != "completed" {
			t.Fatalf("%s: %+v", name, out)
		}
	}
	if out := callTool[Outcome](t, f.session, "folder_create", with(acct, map[string]any{"folder": "Receipts"})); out.Status != "completed" {
		t.Fatalf("folder_create: %+v", out)
	}
	copied := callTool[Outcome](t, f.session, "email_copy", with(inbox, map[string]any{"target_folder": "Archive"}))
	if copied.DestinationUID == 0 {
		t.Fatalf("email_copy: %+v", copied)
	}
	moved := callTool[Outcome](t, f.session, "email_move", with(inbox, map[string]any{"target_folder": "Receipts"}))
	if moved.DestinationUID == 0 {
		t.Fatalf("email_move: %+v", moved)
	}
	archiveValidity := callTool[mail.FolderInfo](t, f.session, "folder_status", with(acct, map[string]any{"folder": "Archive"})).UIDValidity
	trashed := callTool[Outcome](t, f.session, "email_trash", ref("Archive", copied.DestinationUID, archiveValidity))
	if trashed.DestinationUID == 0 {
		t.Fatalf("email_trash: %+v", trashed)
	}
	plan := callTool[batch.Preview](t, f.session, "email_batch_prepare", with(acct, map[string]any{"folder": "Trash", "uid_validity": trashed.DestinationUIDValidity, "uids": []uint32{trashed.DestinationUID}, "action": "delete"}))
	if plan.Token == "" || len(plan.Messages) != 1 {
		t.Fatalf("email_batch_prepare: %+v", plan)
	}
	if out := callTool[batch.Outcome](t, f.session, "email_batch_execute", with(acct, map[string]any{"token": plan.Token, "confirm": true})); out.Status != "completed" {
		t.Fatalf("email_batch_execute: %+v", out)
	}
	if trash := callTool[mail.FolderInfo](t, f.session, "folder_status", with(acct, map[string]any{"folder": "Trash"})); trash.Messages != 0 {
		t.Fatalf("Trash after delete: %+v", trash)
	}
}

func TestDraftToolsOverIMAP(t *testing.T) {
	f := newIMAPFixture(t)
	acct := map[string]any{"account": "personal"}
	compose := map[string]any{"to": []string{"dave@example.com"}, "subject": "Draft subject", "text": "Draft body"}
	created := callTool[DraftOutcome](t, f.session, "draft_create", with(acct, compose))
	if created.Status != "created" || created.Reference.UID == 0 {
		t.Fatalf("draft_create: %+v", created)
	}
	if page := callTool[mail.Page](t, f.session, "draft_list", acct); len(page.Messages) != 1 {
		t.Fatalf("draft_list: %+v", page)
	}
	draftRef := with(acct, map[string]any{"uid": created.Reference.UID, "uid_validity": created.Reference.UIDValidity})
	if draft := callTool[mail.ParsedMessage](t, f.session, "draft_get", draftRef); !strings.Contains(draft.Plain, "Draft body") {
		t.Fatalf("draft_get: %+v", draft)
	}
	updated := callTool[DraftOutcome](t, f.session, "draft_update", with(draftRef, with(compose, map[string]any{"text": "Revised body"})))
	if updated.Status != "updated" || updated.Reference.UID == created.Reference.UID {
		t.Fatalf("draft_update: %+v", updated)
	}
	updatedRef := with(acct, map[string]any{"uid": updated.Reference.UID, "uid_validity": updated.Reference.UIDValidity})
	if preview := callTool[send.Preview](t, f.session, "draft_prepare_send", with(updatedRef, map[string]any{"idempotency_key": "draft-fixture-1"})); preview.Digest == "" || preview.Subject != "Draft subject" {
		t.Fatalf("draft_prepare_send: %+v", preview)
	}
	if out := callTool[Outcome](t, f.session, "draft_delete", updatedRef); out.Status != "completed" {
		t.Fatalf("draft_delete: %+v", out)
	}
	if page := callTool[mail.Page](t, f.session, "draft_list", acct); len(page.Messages) != 0 {
		t.Fatalf("draft_list after delete: %+v", page)
	}
}

func TestIndexSyncToolOverIMAP(t *testing.T) {
	f := newIMAPFixture(t)
	f.server.Append(t, "INBOX", plainFixture)
	f.server.Append(t, "INBOX", attachmentFixture)
	status := callTool[index.FolderStatus](t, f.session, "index_sync", map[string]any{"account": "personal", "folder": "INBOX"})
	if !status.Complete || status.Messages != 2 {
		t.Fatalf("index_sync: %+v", status)
	}
	found := callTool[index.Result](t, f.session, "index_search", map[string]any{"account": "personal", "subject": "invoice"})
	if found.Total != 1 {
		t.Fatalf("index_search after sync: %+v", found)
	}
}

// requiredArgs fills every required input field of a tool with a placeholder
// of the right JSON type, then applies overrides.
func requiredArgs(t *testing.T, tool *mcp.Tool, overrides map[string]any) map[string]any {
	t.Helper()
	var schema struct {
		Properties map[string]struct {
			Type json.RawMessage `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(mustJSON(t, tool.InputSchema), &schema); err != nil {
		t.Fatal(err)
	}
	placeholders := map[string]any{"string": "x", "integer": 1, "boolean": false, "array": []any{}}
	args := map[string]any{}
	for _, name := range schema.Required {
		args[name] = placeholders[schemaType(t, schema.Properties[name].Type)]
	}
	for k, v := range overrides {
		if _, known := schema.Properties[k]; known {
			args[k] = v
		}
	}
	return args
}

// schemaType returns the JSON type, ignoring "null" in a nullable union.
func schemaType(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single
	}
	var union []string
	if err := json.Unmarshal(raw, &union); err != nil {
		t.Fatalf("schema type %s: %v", raw, err)
	}
	for _, typ := range union {
		if typ != "null" {
			return typ
		}
	}
	t.Fatalf("schema type %s has no concrete type", raw)
	return ""
}

func listTools(t *testing.T, session *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	tools := map[string]*mcp.Tool{}
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		tools[tool.Name] = tool
	}
	return tools
}

func TestToolsRejectUnknownAccount(t *testing.T) {
	session := connectInMemory(t, config.Config{IndexDir: privateDir(t, "index"), Accounts: []config.Account{{Alias: "personal", Address: fixtureAddress}}})
	tools := listTools(t, session)
	// 39 general tools plus 3 index tools; a shrinking list would hide gaps.
	if len(tools) != 42 {
		t.Fatalf("tool count changed: %d", len(tools))
	}
	for name, tool := range tools {
		if name == "account_list" {
			continue
		}
		args := requiredArgs(t, tool, map[string]any{"account": "missing"})
		if msg := callToolError(t, session, name, args); !strings.Contains(msg, "unknown account") {
			t.Errorf("%s: %s", name, msg)
		}
	}
}

func TestOrganizeToolsDeniedWithoutPermission(t *testing.T) {
	session := connectInMemory(t, config.Config{Accounts: []config.Account{{Alias: "personal", Address: fixtureAddress}}})
	tools := listTools(t, session)
	for _, name := range []string{"folder_create", "email_mark_read", "email_mark_unread", "email_flag", "email_unflag", "email_copy", "draft_update", "draft_delete"} {
		args := requiredArgs(t, tools[name], map[string]any{"account": "personal", "to": []string{"a@example.com"}})
		if msg := callToolError(t, session, name, args); !strings.Contains(msg, "organize permission is disabled") {
			t.Errorf("%s: %s", name, msg)
		}
	}
}
