package batch

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aleslanger/yahoo-mail-mcp/internal/config"
	"github.com/aleslanger/yahoo-mail-mcp/internal/mail"
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
	case conn := <-l.connections:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error   { close(l.closed); return nil }
func (l *pipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 993} }

func batchIMAP(t *testing.T, withMove bool) (context.Context, config.Account, func(string) uint32) {
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
		SerialNumber: serial, Subject: pkix.Name{CommonName: "batch IMAP test"},
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
	user := imapmemserver.NewUser("batch@example.com", "batch-secret")
	for _, folder := range []string{"INBOX", "Archive", "Trash"} {
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
	t.Setenv("YAHOO_MCP_PASSWORD_BATCH", "batch-secret")
	dial := func(addr string, options *imapclient.Options) (*imapclient.Client, error) {
		clientConn, serverConn := net.Pipe()
		select {
		case listener.connections <- tls.Server(serverConn, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}):
		case <-listener.closed:
			_ = clientConn.Close()
			_ = serverConn.Close()
			return nil, net.ErrClosed
		}
		clientTLS := options.TLSConfig.Clone()
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
	}
	ctx := mail.WithDialTLS(context.Background(), dial)
	a := config.Account{Alias: "batch", Address: "batch@example.com", IMAPHost: "127.0.0.1", IMAPPort: 993, IMAPCAFile: caPath, AllowOrganize: true}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	appendMessage := func(subject string) uint32 {
		client, err := dial(net.JoinHostPort(a.IMAPHost, "993"), &imapclient.Options{TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}})
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		if err := client.Login(a.Address, "batch-secret").Wait(); err != nil {
			t.Fatal(err)
		}
		raw := "From: sender@example.com\r\nTo: batch@example.com\r\nSubject: " + subject + "\r\n\r\nBody\r\n"
		cmd := client.Append("INBOX", int64(len(raw)), nil)
		if _, err := cmd.Write([]byte(raw)); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Close(); err != nil {
			t.Fatal(err)
		}
		data, err := cmd.Wait()
		if err != nil || data.UID == 0 {
			t.Fatalf("append: %+v, %v", data, err)
		}
		return uint32(data.UID)
	}
	return ctx, a, appendMessage
}

func TestBatchRealIMAPMoveConcurrentChange(t *testing.T) {
	ctx, a, appendMessage := batchIMAP(t, true)
	first, second := appendMessage("first"), appendMessage("second")
	inbox, err := mail.Status(ctx, a, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "journal")
	preview, err := Prepare(ctx, a, dir, Input{Account: a.Alias, Folder: "INBOX", UIDValidity: inbox.UIDValidity, UIDs: []uint32{first, second}, Action: "move", Target: "Archive"})
	if err != nil || len(preview.Messages) != 2 || preview.Messages[0].UID != first {
		t.Fatalf("preview: %+v, %v", preview, err)
	}
	if _, err := Execute(ctx, a, dir, preview.Token, false); err == nil {
		t.Fatal("execution without confirmation")
	}
	if _, err := mail.CopyOrMove(ctx, a, "INBOX", "Archive", first, inbox.UIDValidity, true); err != nil {
		t.Fatalf("external move: %v", err)
	}
	out, err := Execute(ctx, a, dir, preview.Token, true)
	if err != nil || out.Status != "partial_or_uncertain" || out.Items[0].Status != "uncertain" || out.Items[1].Status != "completed" {
		t.Fatalf("execution after mailbox change: %+v, %v", out, err)
	}
	beforeReplay, err := mail.Status(ctx, a, "Archive")
	if err != nil || beforeReplay.Messages != 2 {
		t.Fatalf("archive after execution: %+v, %v", beforeReplay, err)
	}
	replayed, err := Execute(ctx, a, dir, preview.Token, true)
	afterReplay, statusErr := mail.Status(ctx, a, "Archive")
	if err != nil || statusErr != nil || replayed.Items[0].Status != "uncertain" || afterReplay.Messages != beforeReplay.Messages {
		t.Fatalf("replay changed mailbox: %+v, %+v, %v, %v", replayed, afterReplay, err, statusErr)
	}
}

func TestBatchRealIMAPFlagAndMoveGate(t *testing.T) {
	ctx, a, appendMessage := batchIMAP(t, false)
	uid := appendMessage("flag")
	inbox, err := mail.Status(ctx, a, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "journal")
	if _, err := Prepare(ctx, a, dir, Input{Account: a.Alias, Folder: "INBOX", UIDValidity: inbox.UIDValidity, UIDs: []uint32{uid}, Action: "move", Target: "Archive"}); err == nil {
		t.Fatal("MOVE preview accepted without capability")
	}
	preview, err := Prepare(ctx, a, dir, Input{Account: a.Alias, Folder: "INBOX", UIDValidity: inbox.UIDValidity, UIDs: []uint32{uid}, Action: "mark_read"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Execute(ctx, a, dir, preview.Token, true)
	status, statusErr := mail.Status(ctx, a, "INBOX")
	if err != nil || statusErr != nil || out.Status != "completed" || status.Unseen != 0 {
		t.Fatalf("mark-read postcondition: %+v, %+v, %v, %v", out, status, err, statusErr)
	}
}

func TestBatchRealIMAPPermanentDeleteOnlyExpungesPlannedUID(t *testing.T) {
	ctx, a, appendMessage := batchIMAP(t, true)
	a.AllowDelete = true
	inbox, err := mail.Status(ctx, a, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	for _, subject := range []string{"keep", "delete", "marked"} {
		uid := appendMessage(subject)
		if _, err := mail.CopyOrMove(ctx, a, "INBOX", "Trash", uid, inbox.UIDValidity, true); err != nil {
			t.Fatalf("move %s to Trash: %v", subject, err)
		}
	}
	inboxUID := appendMessage("inbox")
	trashUIDs := func() map[string]uint32 {
		t.Helper()
		page, err := mail.List(ctx, a, "Trash", 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		bySubject := map[string]uint32{}
		for _, message := range page.Messages {
			bySubject[message.Subject] = message.UID
		}
		return bySubject
	}
	before := trashUIDs()
	trash, err := mail.Status(ctx, a, "Trash")
	if err != nil {
		t.Fatal(err)
	}
	if err := mail.ChangeFlag(ctx, a, "Trash", before["marked"], trash.UIDValidity, imap.FlagDeleted, true); err != nil {
		t.Fatalf("mark unrelated Trash message deleted: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "journal")
	if _, err := Prepare(ctx, a, dir, Input{Account: a.Alias, Folder: "INBOX", UIDValidity: inbox.UIDValidity, UIDs: []uint32{inboxUID}, Action: "delete"}); err == nil {
		t.Fatal("permanent delete preview accepted INBOX")
	}
	preview, err := Prepare(ctx, a, dir, Input{Account: a.Alias, Folder: "Trash", UIDValidity: trash.UIDValidity, UIDs: []uint32{before["delete"]}, Action: "delete"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Execute(ctx, a, dir, preview.Token, true)
	if err != nil || out.Status != "completed" {
		t.Fatalf("permanent delete: %+v, %v", out, err)
	}
	after := trashUIDs()
	if _, ok := after["delete"]; ok {
		t.Fatal("planned message still in Trash")
	}
	if after["keep"] != before["keep"] || after["marked"] != before["marked"] {
		t.Fatalf("unplanned Trash messages changed: before=%v after=%v", before, after)
	}
}
