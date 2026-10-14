package mail

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ales/yahoo-mail-mcp/internal/config"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

type pipeListener struct {
	connections chan net.Conn
	closed      chan struct{}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.connections:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error   { close(l.closed); return nil }
func (l *pipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 993} }

func localIMAP(t *testing.T, withMove bool) (context.Context, config.Account) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "local IMAP test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	cert, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("fixture@example.com", "fixture-secret")
	for _, folder := range []string{"INBOX", "Archive", "Drafts"} {
		if err := user.Create(folder, nil); err != nil {
			t.Fatal(err)
		}
	}
	mem.AddUser(user)
	caps := imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapUIDPlus: {}}
	if withMove {
		caps[imap.CapMove] = struct{}{}
	}
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		}, Caps: caps,
	})
	listener := &pipeListener{connections: make(chan net.Conn), closed: make(chan struct{})}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })
	t.Setenv("YAHOO_MCP_PASSWORD_FIXTURE", "fixture-secret")
	ctx := context.WithValue(context.Background(), dialTLSKey{}, dialTLSFunc(func(addr string, options *imapclient.Options) (*imapclient.Client, error) {
		clientConn, serverConn := net.Pipe()
		select {
		case listener.connections <- tls.Server(serverConn, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}):
		case <-listener.closed:
			_ = clientConn.Close()
			_ = serverConn.Close()
			return nil, net.ErrClosed
		}
		clientTLS := &tls.Config{MinVersion: tls.VersionTLS12}
		if options.TLSConfig != nil {
			clientTLS = options.TLSConfig.Clone()
		}
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		clientTLS.ServerName = host
		secureConn := tls.Client(clientConn, clientTLS)
		if err := secureConn.Handshake(); err != nil {
			_ = clientConn.Close()
			_ = serverConn.Close()
			return nil, err
		}
		return imapclient.New(secureConn, options), nil
	}))
	return ctx, config.Account{Alias: "fixture", Address: "fixture@example.com", IMAPHost: "127.0.0.1", IMAPPort: 993, IMAPCAFile: caPath, AllowOrganize: true}
}

func appendFixture(t *testing.T, ctx context.Context, a config.Account) uint32 {
	t.Helper()
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	raw := "From: Sender <sender@example.com>\r\nTo: fixture@example.com\r\nSubject: Fixture subject\r\nDate: Mon, 12 Oct 2026 10:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nFixture body.\r\n"
	cmd := c.Append("INBOX", int64(len(raw)), nil)
	if _, err := cmd.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := cmd.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if data.UID == 0 {
		t.Fatal("APPEND did not return UID")
	}
	return uint32(data.UID)
}

func TestIMAPConnectionLimit(t *testing.T) {
	ctx, a := localIMAP(t, true)
	closeFns := make([]func(), 0, maxConcurrentIMAPConnections)
	defer func() {
		for _, closeFn := range closeFns {
			closeFn()
		}
	}()
	for i := 0; i < maxConcurrentIMAPConnections; i++ {
		_, closeFn, err := connect(ctx, a)
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		closeFns = append(closeFns, closeFn)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, closeFn, err := connect(waitCtx, a); err == nil {
		closeFn()
		t.Fatal("fifth IMAP connection exceeded the limit")
	}
	closeFns[0]()
	_, closeFn, err := connect(ctx, a)
	if err != nil {
		t.Fatalf("slot was not released: %v", err)
	}
	closeFn()
}

func TestFolderTreeCountPages(t *testing.T) {
	ctx, a := localIMAP(t, true)
	appendFixture(t, ctx, a)
	plain, err := ListFolderTree(ctx, a, false, 0, 0)
	if err != nil || len(plain.Folders) != 3 || plain.CountedFolders != 0 {
		t.Fatalf("plain tree: %+v, %v", plain, err)
	}
	first, err := ListFolderTree(ctx, a, true, 0, 1)
	if err != nil || first.CountedFolders != 1 || first.TotalCountableFolders != 3 || !first.HasMoreCounts || first.NextCountOffset != 1 {
		t.Fatalf("first count page: %+v, %v", first, err)
	}
	if first.Folders[0].Messages == nil || *first.Folders[0].Messages != 0 || first.Folders[1].Messages != nil {
		t.Fatalf("unexpected first page counts: %+v", first.Folders)
	}
	originalDial := ctx.Value(dialTLSKey{}).(dialTLSFunc)
	dials := 0
	countedCtx := context.WithValue(ctx, dialTLSKey{}, dialTLSFunc(func(addr string, options *imapclient.Options) (*imapclient.Client, error) {
		dials++
		return originalDial(addr, options)
	}))
	if _, err := ListFolderTree(countedCtx, a, true, 0, 3); err != nil || dials != 1 {
		t.Fatalf("count page must use one connection: dials=%d, err=%v", dials, err)
	}
	second, err := ListFolderTree(ctx, a, true, first.NextCountOffset, 1)
	if err != nil || second.CountedFolders != 1 || second.Folders[1].Path != "Drafts" || second.Folders[1].Messages == nil || *second.Folders[1].Messages != 0 {
		t.Fatalf("second count page: %+v, %v", second, err)
	}
	last, err := ListFolderTree(ctx, a, true, 2, 1)
	if err != nil || last.HasMoreCounts || last.Folders[2].Path != "INBOX" || last.Folders[2].Messages == nil || *last.Folders[2].Messages != 1 || last.Folders[2].Unseen == nil || *last.Folders[2].Unseen != 1 {
		t.Fatalf("last count page: %+v, %v", last, err)
	}
	if _, err := ListFolderTree(ctx, a, true, -1, 1); err == nil {
		t.Fatal("negative offset accepted")
	}
	if _, err := ListFolderTree(ctx, a, true, 0, 21); err == nil {
		t.Fatal("oversize count page accepted")
	}
}

func TestIMAPLifecycle(t *testing.T) {
	ctx, a := localIMAP(t, true)
	caps, err := Capabilities(ctx, a)
	if err != nil || len(caps) == 0 {
		t.Fatalf("authenticated capability check: %v, %v", caps, err)
	}
	uid := appendFixture(t, ctx, a)
	folders, err := ListFolders(ctx, a)
	if err != nil || len(folders) != 3 {
		t.Fatalf("folders: %v, %v", folders, err)
	}
	if err := CreateFolder(ctx, a, "Projects"); err != nil {
		t.Fatalf("create folder: %v", err)
	}
	projects, err := Status(ctx, a, "Projects")
	if err != nil || projects.Messages != 0 {
		t.Fatalf("new folder: %+v, %v", projects, err)
	}
	status, err := Status(ctx, a, "INBOX")
	if err != nil || status.Messages != 1 || status.UIDValidity == 0 {
		t.Fatalf("status: %+v, %v", status, err)
	}
	page, err := List(ctx, a, "INBOX", 0, 10)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].UID != uid {
		t.Fatalf("list: %+v, %v", page, err)
	}
	validity := page.UIDValidity
	page, err = Search(ctx, a, "INBOX", 0, 10, SearchFilter{Subject: "Fixture"})
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("search: %+v, %v", page, err)
	}
	page, err = Search(ctx, a, "INBOX", 0, 10, SearchFilter{Subject: "absent"})
	if err != nil || len(page.Messages) != 0 {
		t.Fatalf("negative search: %+v, %v", page, err)
	}
	body, err := Get(ctx, a, "INBOX", uid, validity, 4096)
	if err != nil || body.Truncated || body.RawBase64 == "" {
		t.Fatalf("get: %+v, %v", body, err)
	}
	first, err := GetRange(ctx, a, "INBOX", uid, validity, 0, 20)
	if err != nil || !first.Truncated || first.NextOffset != 20 {
		t.Fatalf("first MIME range: %+v, %v", first, err)
	}
	second, err := GetRange(ctx, a, "INBOX", uid, validity, first.NextOffset, 262144)
	if err != nil || second.Truncated || second.Offset != 20 {
		t.Fatalf("remaining MIME range: %+v, %v", second, err)
	}
	firstBytes, _ := base64.StdEncoding.DecodeString(first.RawBase64)
	secondBytes, _ := base64.StdEncoding.DecodeString(second.RawBase64)
	wholeBytes, _ := base64.StdEncoding.DecodeString(body.RawBase64)
	if !bytes.Equal(append(firstBytes, secondBytes...), wholeBytes) {
		t.Fatal("MIME ranges did not reassemble to original message")
	}
	parsed, err := ReadParsed(ctx, a, "INBOX", uid, validity)
	if err != nil || !strings.Contains(parsed.Plain, "Fixture body") {
		t.Fatalf("parsed: %+v, %v", parsed, err)
	}
	if _, err := Get(ctx, a, "INBOX", uid, validity+1, 4096); !errors.Is(err, ErrStale) {
		t.Fatalf("stale UID accepted: %v", err)
	}
	if err := ChangeFlag(ctx, a, "INBOX", uid, validity, imap.FlagSeen, true); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyOrMove(ctx, a, "INBOX", "Archive", uid, validity, false); err != nil {
		t.Fatal(err)
	}
	archive, err := Status(ctx, a, "Archive")
	if err != nil || archive.Messages != 1 {
		t.Fatalf("copy destination: %+v, %v", archive, err)
	}
	inbox, err := Status(ctx, a, "INBOX")
	if err != nil || inbox.Messages != 1 {
		t.Fatalf("copy source: %+v, %v", inbox, err)
	}
	if _, err := CopyOrMove(ctx, a, "INBOX", "Archive", uid, validity+1, true); !errors.Is(err, ErrStale) {
		t.Fatalf("stale transfer accepted: %v", err)
	}
	if _, err := CopyOrMove(ctx, a, "INBOX", "Archive", uid, validity, true); err != nil {
		t.Fatal(err)
	}
	inbox, err = Status(ctx, a, "INBOX")
	if err != nil || inbox.Messages != 0 {
		t.Fatalf("move source: %+v, %v", inbox, err)
	}
	archive, err = Status(ctx, a, "Archive")
	if err != nil || archive.Messages != 2 {
		t.Fatalf("move destination: %+v, %v", archive, err)
	}
}

func TestAdvancedSearch(t *testing.T) {
	ctx, a := localIMAP(t, true)
	uid := appendFixture(t, ctx, a)
	flagged := false
	filter := SearchFilter{To: a.Address, Since: time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02"), Before: time.Now().UTC().Add(24 * time.Hour).Format("2006-01-02"), Unread: true, Flagged: &flagged}
	page, err := Search(ctx, a, "INBOX", 0, 10, filter)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].UID != uid {
		t.Fatalf("advanced search: %+v, %v", page, err)
	}
	if err := ChangeFlag(ctx, a, "INBOX", uid, page.UIDValidity, imap.FlagFlagged, true); err != nil {
		t.Fatal(err)
	}
	flagged = true
	page, err = Search(ctx, a, "INBOX", 0, 10, filter)
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("flagged search: %+v, %v", page, err)
	}
	filter.Before = filter.Since
	if _, err := Search(ctx, a, "INBOX", 0, 10, filter); err == nil {
		t.Fatal("invalid date range accepted")
	}
}

func TestMoveToTrash(t *testing.T) {
	ctx, a := localIMAP(t, true)
	if err := CreateFolder(ctx, a, "Trash"); err != nil {
		t.Fatal(err)
	}
	uid := appendFixture(t, ctx, a)
	status, err := Status(ctx, a, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MoveToTrash(ctx, a, "INBOX", uid, status.UIDValidity+1); !errors.Is(err, ErrStale) {
		t.Fatalf("stale Trash move accepted: %v", err)
	}
	if _, err := MoveToTrash(ctx, a, "INBOX", uid, status.UIDValidity); err != nil {
		t.Fatalf("move to Trash: %v", err)
	}
	inbox, err := Status(ctx, a, "INBOX")
	if err != nil || inbox.Messages != 0 {
		t.Fatalf("INBOX after trash: %+v, %v", inbox, err)
	}
	trash, err := Status(ctx, a, "Trash")
	if err != nil || trash.Messages != 1 {
		t.Fatalf("Trash after move: %+v, %v", trash, err)
	}
}

func TestMoveToTrashWithoutMoveCapability(t *testing.T) {
	ctx, a := localIMAP(t, false)
	if err := CreateFolder(ctx, a, "Trash"); err != nil {
		t.Fatal(err)
	}
	uid := appendFixture(t, ctx, a)
	status, err := Status(ctx, a, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MoveToTrash(ctx, a, "INBOX", uid, status.UIDValidity); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("move without MOVE capability: %v", err)
	}
	inbox, err := Status(ctx, a, "INBOX")
	if err != nil || inbox.Messages != 1 {
		t.Fatalf("source changed without MOVE: %+v, %v", inbox, err)
	}
}

func TestDraftLifecycle(t *testing.T) {
	ctx, a := localIMAP(t, true)
	raw := []byte("From: fixture@example.com\r\nTo: recipient@example.com\r\nSubject: First draft\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nFirst body\r\n")
	created, err := CreateDraft(ctx, a, raw)
	if err != nil || created.UID == 0 || created.UIDValidity == 0 || created.Folder != "Drafts" {
		t.Fatalf("create draft: %+v, %v", created, err)
	}
	// Yahoo can store a message in Drafts without retaining the requested
	// \Draft flag. Folder membership and the exact UID identify this draft.
	if err := ChangeFlag(ctx, a, "Drafts", created.UID, created.UIDValidity, imap.FlagDraft, false); err != nil {
		t.Fatalf("remove draft flag: %v", err)
	}
	stored, err := LoadDraftRaw(ctx, a, created.UID, created.UIDValidity)
	if err != nil || !bytes.Equal(stored, raw) {
		t.Fatalf("loaded draft differs from stored MIME: %d bytes, %v", len(stored), err)
	}
	page, err := ListDrafts(ctx, a, 0, 10)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].UID != created.UID {
		t.Fatalf("list drafts: %+v, %v", page, err)
	}
	parsed, err := GetDraft(ctx, a, created.UID, created.UIDValidity)
	if err != nil || !strings.Contains(parsed.Plain, "First body") {
		t.Fatalf("get draft: %+v, %v", parsed, err)
	}
	if _, err := UpdateDraft(ctx, a, created.UID, created.UIDValidity+1, raw); !errors.Is(err, ErrStale) {
		t.Fatalf("stale draft update accepted: %v", err)
	}
	updatedRaw := []byte(strings.ReplaceAll(string(raw), "First", "Second"))
	updated, err := UpdateDraft(ctx, a, created.UID, created.UIDValidity, updatedRaw)
	if err != nil || updated.UID == created.UID {
		t.Fatalf("update draft: %+v, %v", updated, err)
	}
	if err := ChangeFlag(ctx, a, "Drafts", updated.UID, updated.UIDValidity, imap.FlagDraft, false); err != nil {
		t.Fatalf("remove updated draft flag: %v", err)
	}
	page, err = ListDrafts(ctx, a, 0, 10)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].UID != updated.UID {
		t.Fatalf("updated draft listing: %+v, %v", page, err)
	}
	parsed, err = GetDraft(ctx, a, updated.UID, updated.UIDValidity)
	if err != nil || !strings.Contains(parsed.Plain, "Second body") {
		t.Fatalf("updated draft content: %+v, %v", parsed, err)
	}
	if err := DeleteDraft(ctx, a, updated.UID, updated.UIDValidity); err != nil {
		t.Fatalf("delete draft: %v", err)
	}
	page, err = ListDrafts(ctx, a, 0, 10)
	if err != nil || len(page.Messages) != 0 {
		t.Fatalf("deleted draft listing: %+v, %v", page, err)
	}
}

func TestSaveLargeAttachment(t *testing.T) {
	ctx, a := localIMAP(t, true)
	payload := bytes.Repeat([]byte("large attachment content\n"), 15000)
	encoded := base64.StdEncoding.EncodeToString(payload)
	raw := []byte("From: sender@example.com\r\nTo: fixture@example.com\r\nSubject: Large attachment\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=part-boundary\r\n\r\n--part-boundary\r\nContent-Type: text/plain\r\n\r\nBody\r\n--part-boundary\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=large.bin\r\nContent-Transfer-Encoding: base64\r\n\r\n" + encoded + "\r\n--part-boundary--\r\n")
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	cmd := c.Append("INBOX", int64(len(raw)), nil)
	if _, err := cmd.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Close(); err != nil {
		t.Fatal(err)
	}
	appended, err := cmd.Wait()
	closeFn()
	if err != nil {
		t.Fatal(err)
	}
	status, err := Status(ctx, a, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ReadParsedLarge(ctx, a, "INBOX", uint32(appended.UID), status.UIDValidity)
	if err != nil || parsed.Plain != "Body" || parsed.BodyTruncated || len(parsed.Attachments) != 1 || parsed.Attachments[0].Size != int64(len(payload)) {
		t.Fatalf("large parsed message: %+v, %v", parsed, err)
	}
	dir := filepath.Join(t.TempDir(), "downloads")
	saved, err := SaveAttachment(ctx, a, "INBOX", uint32(appended.UID), status.UIDValidity, 2, dir)
	if err != nil || saved.Metadata.Filename != "large.bin" || saved.Metadata.Size != int64(len(payload)) {
		t.Fatalf("save attachment: %+v, %v", saved, err)
	}
	actual, err := os.ReadFile(saved.Path)
	if err != nil || !bytes.Equal(actual, payload) {
		t.Fatalf("saved attachment mismatch: %d bytes, %v", len(actual), err)
	}
	info, err := os.Stat(saved.Path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("attachment permissions: %v, %v", info, err)
	}
	if _, err := SaveAttachment(ctx, a, "INBOX", uint32(appended.UID), status.UIDValidity+1, 2, dir); !errors.Is(err, ErrStale) {
		t.Fatalf("stale attachment reference accepted: %v", err)
	}
}

func TestReplyContext(t *testing.T) {
	ctx, a := localIMAP(t, true)
	raw := []byte("From: Sender <sender@example.com>\r\nReply-To: replies@example.com\r\nTo: fixture@example.com, colleague@example.com\r\nCc: colleague@example.com, third@example.com\r\nSubject: =?utf-8?q?na=C3=AFve_example?=\r\nMessage-ID: <original@example.com>\r\nReferences: <earlier@example.com>\r\nDate: Mon, 12 Oct 2026 10:00:00 +0000\r\n\r\nBody\r\n")
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	cmd := c.Append("INBOX", int64(len(raw)), nil)
	if _, err := cmd.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Close(); err != nil {
		t.Fatal(err)
	}
	appended, err := cmd.Wait()
	closeFn()
	if err != nil {
		t.Fatal(err)
	}
	status, err := Status(ctx, a, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	reply, err := ReplyToMessage(ctx, a, "INBOX", uint32(appended.UID), status.UIDValidity, true)
	if err != nil || len(reply.To) != 1 || reply.To[0] != "replies@example.com" || len(reply.Cc) != 2 || reply.Subject != "Re: naïve example" || reply.InReplyTo != "<original@example.com>" || len(reply.References) != 2 {
		t.Fatalf("reply context: %+v, %v", reply, err)
	}
	if _, err := ReplyToMessage(ctx, a, "INBOX", uint32(appended.UID), status.UIDValidity+1, false); !errors.Is(err, ErrStale) {
		t.Fatalf("stale reply source accepted: %v", err)
	}
	selfRaw := []byte("From: fixture@example.com\r\nTo: fixture@example.com\r\nSubject: Self test\r\nMessage-ID: <self@example.com>\r\n\r\nBody\r\n")
	c, closeFn, err = connect(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	cmd = c.Append("INBOX", int64(len(selfRaw)), nil)
	if _, err := cmd.Write(selfRaw); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Close(); err != nil {
		t.Fatal(err)
	}
	selfAppended, err := cmd.Wait()
	closeFn()
	if err != nil {
		t.Fatal(err)
	}
	selfReply, err := ReplyToMessage(ctx, a, "INBOX", uint32(selfAppended.UID), status.UIDValidity, true)
	if err != nil || len(selfReply.To) != 1 || selfReply.To[0] != a.Address || len(selfReply.Cc) != 0 {
		t.Fatalf("self reply context: %+v, %v", selfReply, err)
	}
}

func TestCapabilitiesRejectsWrongPassword(t *testing.T) {
	ctx, a := localIMAP(t, true)
	t.Setenv("YAHOO_MCP_PASSWORD_FIXTURE", "wrong-secret")
	_, err := Capabilities(ctx, a)
	if err == nil || err.Error() != "IMAP authentication failed" {
		t.Fatalf("unexpected authentication result: %v", err)
	}
}

func TestCapabilitiesWithPrivateCredentialFile(t *testing.T) {
	ctx, a := localIMAP(t, true)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YAHOO_MCP_PASSWORD_FIXTURE", "")
	dir := filepath.Join(home, ".config", "yahoo-mail-mcp")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixture-password"), []byte("fixture-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	caps, err := Capabilities(ctx, a)
	if err != nil || len(caps) == 0 {
		t.Fatalf("credential-file IMAP login failed: %v", err)
	}
}

func TestMoveWithoutCapabilityKeepsSource(t *testing.T) {
	ctx, a := localIMAP(t, false)
	uid := appendFixture(t, ctx, a)
	status, err := Status(ctx, a, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CopyOrMove(ctx, a, "INBOX", "Archive", uid, status.UIDValidity, true); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("MOVE without capability: %v", err)
	}
	source, err := Status(ctx, a, "INBOX")
	if err != nil || source.Messages != 1 {
		t.Fatalf("source changed: %+v, %v", source, err)
	}
	dest, err := Status(ctx, a, "Archive")
	if err != nil || dest.Messages != 0 {
		t.Fatalf("destination changed: %+v, %v", dest, err)
	}
}
