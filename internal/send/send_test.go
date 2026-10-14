package send

import (
	"bufio"
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
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ales/yahoo-mail-mcp/internal/config"
	mail "github.com/emersion/go-message/mail"
)

func TestComposeAttachmentAndDigest(t *testing.T) {
	in := Input{IdempotencyKey: "attachment-1", To: []string{"recipient@example.com"}, Subject: "Files", Text: "Plain", HTML: "<p>HTML</p>", Attachments: []Attachment{{Filename: "report.txt", ContentType: "text/plain", ContentBase64: "aGVsbG8="}}}
	preview, err := Prepare("sender@example.com", in)
	if err != nil || preview.MIMEBytes <= 0 {
		t.Fatalf("prepare attachment: %+v, %v", preview, err)
	}
	raw, err := ComposeDraft("sender@example.com", in)
	if err != nil || !bytes.Contains(raw, []byte("multipart/mixed")) || !bytes.Contains(raw, []byte("multipart/alternative")) || !bytes.Contains(raw, []byte("report.txt")) {
		t.Fatalf("compose MIME attachment: %v", err)
	}
	reader, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	attachmentFound := false
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header, ok := part.Header.(*mail.AttachmentHeader); ok {
			name, _ := header.Filename()
			data, err := io.ReadAll(part.Body)
			if err != nil || name != "report.txt" || string(data) != "hello" {
				t.Fatalf("attachment content: %q, %q, %v", name, data, err)
			}
			attachmentFound = true
		}
	}
	if !attachmentFound {
		t.Fatal("attachment MIME part missing")
	}
	in.Attachments[0].ContentBase64 = "d29ybGQ="
	other, err := Prepare("sender@example.com", in)
	if err != nil || other.Digest == preview.Digest {
		t.Fatalf("attachment content absent from digest: %+v, %v", other, err)
	}
	in.Attachments[0].Filename = "../secret"
	if _, err := Prepare("sender@example.com", in); err == nil {
		t.Fatal("path-like filename accepted")
	}
}

func TestReplyThreadingHeadersAreValidatedAndBoundToDigest(t *testing.T) {
	in := Input{IdempotencyKey: "reply-12345678", To: []string{"sender@example.com"}, Subject: "Re: topic", Text: "Answer", InReplyTo: "<original@example.com>", References: []string{"<earlier@example.com>", "<original@example.com>"}}
	preview, err := Prepare("recipient@example.com", in)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ComposeDraft("recipient@example.com", in)
	if err != nil || !bytes.Contains(raw, []byte("In-Reply-To: <original@example.com>")) || !bytes.Contains(raw, []byte("References: <earlier@example.com> <original@example.com>")) {
		t.Fatalf("missing threading headers: %v, %s", err, raw)
	}
	in.InReplyTo = "<different@example.com>"
	changed, err := Prepare("recipient@example.com", in)
	if err != nil || changed.Digest == preview.Digest {
		t.Fatalf("thread target absent from digest: %+v, %v", changed, err)
	}
	in.InReplyTo = "<bad@example.com>\r\nBcc: evil@example.com"
	if _, err := Prepare("recipient@example.com", in); err == nil {
		t.Fatal("threading header injection accepted")
	}
}

func TestComposeRejectsInjectionAndOmitsBccHeader(t *testing.T) {
	in := Input{IdempotencyKey: "send-12345678", To: []string{"to@example.com"}, Bcc: []string{"hidden@example.com"}, Subject: "naïve example", Text: "A naïve example", HTML: "<p>A naïve example</p>"}
	p, err := prepare("sender@example.com", in)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := Prepare("sender@example.com", in)
	if err != nil || preview.Digest != p.digest || preview.MIMEBytes == 0 {
		t.Fatalf("invalid preview: %+v, %v", preview, err)
	}
	if len(p.envelope) != 2 || !strings.Contains(string(p.message), "multipart/alternative") || strings.Contains(string(p.message), "Bcc:") {
		t.Fatalf("invalid MIME/envelope: %s", p.message)
	}
	in.Subject = "bad\r\nBcc: injected@example.com"
	if _, err := prepare("sender@example.com", in); err == nil {
		t.Fatal("header injection accepted")
	}
	in.Subject = "safe"
	in.To = []string{"to@example.com\r\nRCPT TO:<evil@example.com>"}
	if _, err := prepare("sender@example.com", in); err == nil {
		t.Fatal("recipient injection accepted")
	}
}

func fixtureCertificate(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "SMTP fixture"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"smtp.test"}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
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
	caPath := filepath.Join(t.TempDir(), "smtp-ca.pem")
	if err := os.WriteFile(caPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	return cert, caPath
}

// serveSMTPSession answers one authenticated SMTP session on an already secure connection.
func serveSMTPSession(secure io.ReadWriter, greet, accepted bool, messageCh chan<- string) {
	r := bufio.NewReader(secure)
	if greet {
		fmt.Fprint(secure, "220 smtp.test ready\r\n")
	}
	var body strings.Builder
	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		upper := strings.ToUpper(strings.TrimSpace(line))
		if inData {
			if line == ".\r\n" {
				messageCh <- body.String()
				if !accepted {
					return
				}
				fmt.Fprint(secure, "250 queued\r\n")
				inData = false
			} else {
				body.WriteString(line)
			}
			continue
		}
		switch {
		case strings.HasPrefix(upper, "EHLO "):
			fmt.Fprint(secure, "250-smtp.test\r\n250 AUTH PLAIN\r\n")
		case strings.HasPrefix(upper, "AUTH PLAIN "):
			decoded, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(line[len("AUTH PLAIN "):]))
			if !strings.Contains(string(decoded), "fixture-secret") {
				fmt.Fprint(secure, "535 bad auth\r\n")
				return
			}
			fmt.Fprint(secure, "235 authenticated\r\n")
		case strings.HasPrefix(upper, "MAIL FROM:"), strings.HasPrefix(upper, "RCPT TO:"):
			fmt.Fprint(secure, "250 ok\r\n")
		case upper == "DATA":
			fmt.Fprint(secure, "354 enter data\r\n")
			inData = true
		case upper == "QUIT":
			fmt.Fprint(secure, "221 bye\r\n")
			return
		default:
			fmt.Fprint(secure, "500 unsupported\r\n")
			return
		}
	}
}

func smtpFixture(t *testing.T, accepted bool, dialCount *atomic.Int32) (context.Context, config.Account, <-chan string) {
	t.Helper()
	cert, caPath := fixtureCertificate(t)
	messageCh := make(chan string, 1)
	ctx := context.WithValue(context.Background(), dialKey{}, dialFunc(func(_ context.Context, _ string, cfg *tls.Config) (net.Conn, error) {
		dialCount.Add(1)
		clientConn, serverConn := net.Pipe()
		go func() {
			defer serverConn.Close()
			secure := tls.Server(serverConn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
			if err := secure.Handshake(); err != nil {
				messageCh <- "handshake: " + err.Error()
				return
			}
			serveSMTPSession(secure, true, accepted, messageCh)
		}()
		clientTLS := tls.Client(clientConn, cfg)
		if err := clientTLS.Handshake(); err != nil {
			_ = clientConn.Close()
			return nil, err
		}
		return clientTLS, nil
	}))
	t.Setenv("YAHOO_MCP_PASSWORD_SENDER", "fixture-secret")
	a := config.Account{Alias: "sender", Address: "sender@example.com", SMTPHost: "smtp.test", SMTPPort: 465, SMTPSecurity: config.SMTPSecurityTLS, SMTPCAFile: caPath, AllowSend: true}
	return ctx, a, messageCh
}

func TestConfirmedSendIsIdempotent(t *testing.T) {
	var count atomic.Int32
	ctx, a, messages := smtpFixture(t, true, &count)
	dir := filepath.Join(t.TempDir(), "journal")
	in := Input{IdempotencyKey: "confirmed-123", To: []string{"to@example.com"}, Bcc: []string{"hidden@example.com"}, Subject: "hello", Text: "Hello"}
	first, err := Send(ctx, a, dir, in)
	if err != nil || first.Status != "confirmed" {
		t.Fatalf("first send: %+v, %v", first, err)
	}
	select {
	case body := <-messages:
		if strings.Contains(body, "Bcc:") || !strings.Contains(body, "Message-ID:") {
			t.Fatalf("bad message: %s", body)
		}
	case <-time.After(time.Second):
		t.Fatal("SMTP did not receive message")
	}
	second, err := Send(ctx, a, dir, in)
	if err != nil || second.Status != "confirmed" || second.MessageID != first.MessageID || count.Load() != 1 {
		t.Fatalf("duplicate send: %+v, %v, dials=%d", second, err, count.Load())
	}
	t.Setenv("YAHOO_MCP_PASSWORD_SENDER", "")
	t.Setenv("HOME", t.TempDir())
	a.SMTPCAFile = filepath.Join(t.TempDir(), "missing-ca.pem")
	withoutCredentials, err := Send(ctx, a, dir, in)
	if err != nil || withoutCredentials.Status != "confirmed" || withoutCredentials.MessageID != first.MessageID || count.Load() != 1 {
		t.Fatalf("confirmed replay required credentials or TLS setup: %+v, %v, dials=%d", withoutCredentials, err, count.Load())
	}
	newInput := in
	newInput.IdempotencyKey = "no-credential-123"
	if _, err := Send(ctx, a, dir, newInput); err == nil {
		t.Fatal("new send accepted without credentials")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("journal entries: %v, %v", entries, err)
	}
	journalBytes, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil || strings.Contains(string(journalBytes), "fixture-secret") || strings.Contains(string(journalBytes), in.Text) {
		t.Fatalf("journal leaked secret or message: %v", err)
	}
	in.Text = "different"
	if _, err := Send(ctx, a, dir, in); err == nil {
		t.Fatal("same key accepted for changed payload")
	}
}

func TestRestoredJournalPreventsDuplicateSubmission(t *testing.T) {
	var count atomic.Int32
	ctx, account, messages := smtpFixture(t, true, &count)
	base := t.TempDir()
	original := filepath.Join(base, "original")
	input := Input{IdempotencyKey: "restore-123", To: []string{"to@example.com"}, Subject: "restore", Text: "One message"}
	first, err := Send(ctx, account, original, input)
	if err != nil || first.Status != "confirmed" {
		t.Fatalf("first send: %+v, %v", first, err)
	}
	select {
	case <-messages:
	case <-time.After(time.Second):
		t.Fatal("SMTP did not receive first message")
	}
	entries, err := os.ReadDir(original)
	if err != nil || len(entries) != 1 {
		t.Fatalf("original journal entries: %v, %v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(original, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(base, "restored")
	if err := os.Mkdir(restored, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(restored, entries[0].Name()), data, 0600); err != nil {
		t.Fatal(err)
	}
	replay, err := Send(ctx, account, restored, input)
	if err != nil || replay.Status != "confirmed" || replay.MessageID != first.MessageID || count.Load() != 1 {
		t.Fatalf("restored journal replay: %+v, %v, dials=%d", replay, err, count.Load())
	}
	if err := os.WriteFile(filepath.Join(restored, entries[0].Name()), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	uncertain, err := Send(ctx, account, restored, input)
	if err != nil || uncertain.Status != "uncertain" || count.Load() != 1 {
		t.Fatalf("corrupt restored journal: %+v, %v, dials=%d", uncertain, err, count.Load())
	}
}

func TestSMTPConnectionCheckDoesNotSend(t *testing.T) {
	var count atomic.Int32
	ctx, account, messages := smtpFixture(t, true, &count)
	account.AllowSend = false
	if err := TestConnection(ctx, account); err != nil {
		t.Fatalf("SMTP connection check: %v", err)
	}
	if count.Load() != 1 || len(messages) != 0 {
		t.Fatalf("connection check started a message transaction: dials=%d messages=%d", count.Load(), len(messages))
	}
	t.Setenv("YAHOO_MCP_PASSWORD_SENDER", "wrong-secret")
	if err := TestConnection(ctx, account); err == nil || err.Error() != "SMTP authentication failed" {
		t.Fatalf("wrong password accepted or leaked SMTP detail: %v", err)
	}
}

func TestSMTPConnectionCapacityRejectsBeforeDial(t *testing.T) {
	smtpConnectionSlots <- struct{}{}
	smtpConnectionSlots <- struct{}{}
	defer func() { <-smtpConnectionSlots; <-smtpConnectionSlots }()
	var dialed atomic.Bool
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	ctx = context.WithValue(ctx, dialKey{}, dialFunc(func(context.Context, string, *tls.Config) (net.Conn, error) {
		dialed.Store(true)
		return nil, errors.New("unexpected dial")
	}))
	_, _, err := connectSMTP(ctx, config.Account{SMTPHost: "smtp.test", SMTPPort: 465}, "fixture-secret", &tls.Config{})
	if err == nil || err.Error() != "SMTP connection capacity unavailable" || dialed.Load() {
		t.Fatalf("capacity failure: %v, dialed=%t", err, dialed.Load())
	}
}

func TestAttachmentSubmissionMatchesPreview(t *testing.T) {
	var count atomic.Int32
	ctx, a, messages := smtpFixture(t, true, &count)
	in := Input{IdempotencyKey: "attached-123", To: []string{"to@example.com"}, Subject: "attached", Text: "See attachment", Attachments: []Attachment{{Filename: "note.txt", ContentType: "text/plain", ContentBase64: "aGVsbG8="}}}
	preview, err := Prepare(a.Address, in)
	if err != nil || preview.Digest == "" {
		t.Fatalf("preview: %+v, %v", preview, err)
	}
	receipt, err := Send(ctx, a, filepath.Join(t.TempDir(), "journal"), in)
	if err != nil || receipt.Status != "confirmed" {
		t.Fatalf("send attachment: %+v, %v", receipt, err)
	}
	select {
	case body := <-messages:
		if !strings.Contains(body, "multipart/mixed") || !strings.Contains(body, "note.txt") || !strings.Contains(body, "aGVsbG8=") {
			t.Fatalf("attachment absent from SMTP DATA: %s", body)
		}
	case <-time.After(time.Second):
		t.Fatal("SMTP did not receive attachment")
	}
	if count.Load() != 1 {
		t.Fatalf("unexpected SMTP submissions: %d", count.Load())
	}
}

func TestStoredDraftSubmission(t *testing.T) {
	var count atomic.Int32
	ctx, a, messages := smtpFixture(t, true, &count)
	raw, err := ComposeDraft(a.Address, Input{To: []string{"to@example.com"}, Subject: "Draft subject", Text: "Draft body", Attachments: []Attachment{{Filename: "note.txt", ContentBase64: "aGVsbG8="}}})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := PrepareStoredDraft(a.Address, raw, "draft-send-123", []string{"hidden@example.com"})
	if err != nil || preview.Digest == "" || len(preview.Bcc) != 1 || preview.AttachmentCount != 1 || preview.AttachmentNames[0] != "note.txt" {
		t.Fatalf("stored draft preview: %+v, %v", preview, err)
	}
	changed, err := PrepareStoredDraft(a.Address, raw, "draft-send-123", nil)
	if err != nil || changed.Digest == preview.Digest {
		t.Fatalf("Bcc absent from digest: %+v, %v", changed, err)
	}
	receipt, err := SendStoredDraft(ctx, a, filepath.Join(t.TempDir(), "journal"), raw, "draft-send-123", []string{"hidden@example.com"})
	if err != nil || receipt.Status != "confirmed" {
		t.Fatalf("stored draft send: %+v, %v", receipt, err)
	}
	select {
	case body := <-messages:
		if !strings.Contains(body, "Draft body") || strings.Contains(body, "Bcc:") {
			t.Fatalf("incorrect stored draft DATA: %s", body)
		}
	case <-time.After(time.Second):
		t.Fatal("SMTP did not receive stored draft")
	}
	if count.Load() != 1 {
		t.Fatalf("unexpected SMTP submissions: %d", count.Load())
	}
}

func TestLostAcknowledgmentRemainsUncertain(t *testing.T) {
	var count atomic.Int32
	ctx, a, messages := smtpFixture(t, false, &count)
	dir := filepath.Join(t.TempDir(), "journal")
	in := Input{IdempotencyKey: "uncertain-123", To: []string{"to@example.com"}, Subject: "hello", Text: "Hello"}
	first, err := Send(ctx, a, dir, in)
	if err != nil || first.Status != "uncertain" {
		t.Fatalf("lost ack: %+v, %v", first, err)
	}
	select {
	case <-messages:
	case <-time.After(time.Second):
		t.Fatal("SMTP did not receive message")
	}
	second, err := Send(ctx, a, dir, in)
	if err != nil || second.Status != "uncertain" || count.Load() != 1 {
		t.Fatalf("uncertain send retried: %+v, %v, dials=%d", second, err, count.Load())
	}
}

func TestAuthenticationFailureCanRetrySameKey(t *testing.T) {
	var count atomic.Int32
	ctx, a, _ := smtpFixture(t, true, &count)
	dir := filepath.Join(t.TempDir(), "journal")
	in := Input{IdempotencyKey: "auth-retry-123", To: []string{"to@example.com"}, Subject: "hello", Text: "Hello"}
	t.Setenv("YAHOO_MCP_PASSWORD_SENDER", "wrong-secret")
	if _, err := Send(ctx, a, dir, in); err == nil {
		t.Fatal("authentication failure did not return an error")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("unsent attempt retained in journal: %v, %v", entries, err)
	}
	t.Setenv("YAHOO_MCP_PASSWORD_SENDER", "fixture-secret")
	receipt, err := Send(ctx, a, dir, in)
	if err != nil || receipt.Status != "confirmed" || count.Load() != 2 {
		t.Fatalf("same-key retry failed: %+v, %v, dials=%d", receipt, err, count.Load())
	}
}
