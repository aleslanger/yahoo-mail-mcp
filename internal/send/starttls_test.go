package send

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aleslanger/yahoo-mail-mcp/internal/config"
)

// startTLSFixture serves a plaintext SMTP greeting that upgrades with STARTTLS
// when offerSTARTTLS is true. authOnPlaintext records credentials sent before TLS.
func startTLSFixture(t *testing.T, offerSTARTTLS bool, authOnPlaintext *atomic.Bool) (context.Context, config.Account, <-chan string) {
	t.Helper()
	cert, caPath := fixtureCertificate(t)
	messageCh := make(chan string, 1)
	ctx := context.WithValue(context.Background(), dialKey{}, dialFunc(func(_ context.Context, _ string, cfg *tls.Config) (net.Conn, error) {
		if cfg != nil {
			return nil, fmt.Errorf("STARTTLS dial must start in plaintext")
		}
		clientConn, serverConn := net.Pipe()
		go func() {
			defer serverConn.Close()
			r := bufio.NewReader(serverConn)
			fmt.Fprint(serverConn, "220 smtp.test ready\r\n")
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return
				}
				upper := strings.ToUpper(strings.TrimSpace(line))
				switch {
				case strings.HasPrefix(upper, "EHLO ") && offerSTARTTLS:
					fmt.Fprint(serverConn, "250-smtp.test\r\n250 STARTTLS\r\n")
				case strings.HasPrefix(upper, "EHLO "):
					fmt.Fprint(serverConn, "250-smtp.test\r\n250 AUTH PLAIN\r\n")
				case upper == "STARTTLS" && offerSTARTTLS:
					fmt.Fprint(serverConn, "220 ready for TLS\r\n")
					secure := tls.Server(serverConn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
					if err := secure.Handshake(); err != nil {
						return
					}
					serveSMTPSession(secure, false, true, messageCh)
					return
				case strings.HasPrefix(upper, "AUTH "):
					authOnPlaintext.Store(true)
					fmt.Fprint(serverConn, "535 refused\r\n")
					return
				case upper == "QUIT":
					fmt.Fprint(serverConn, "221 bye\r\n")
					return
				default:
					fmt.Fprint(serverConn, "500 unsupported\r\n")
					return
				}
			}
		}()
		return clientConn, nil
	}))
	t.Setenv("YAHOO_MCP_PASSWORD_SENDER", "fixture-secret")
	a := config.Account{Alias: "sender", Address: "sender@example.com", SMTPHost: "smtp.test", SMTPPort: 587, SMTPSecurity: config.SMTPSecuritySTARTTLS, SMTPCAFile: caPath, AllowSend: true}
	return ctx, a, messageCh
}

func TestSTARTTLSSubmissionAuthenticatesOnlyAfterUpgrade(t *testing.T) {
	var authOnPlaintext atomic.Bool
	ctx, account, messages := startTLSFixture(t, true, &authOnPlaintext)
	input := Input{IdempotencyKey: "starttls-1", To: []string{"to@example.com"}, Subject: "STARTTLS", Text: "Submitted after upgrade."}
	receipt, err := Send(ctx, account, filepath.Join(t.TempDir(), "journal"), input)
	if err != nil || receipt.Status != "confirmed" {
		t.Fatalf("STARTTLS send: status=%s error=%v", receipt.Status, err)
	}
	if body := <-messages; !strings.Contains(body, "Submitted after upgrade.") {
		t.Fatalf("submitted body missing text: %q", body)
	}
	if authOnPlaintext.Load() {
		t.Fatal("credentials were sent before TLS")
	}
}

func TestSTARTTLSMissingCapabilityStopsBeforeCredentials(t *testing.T) {
	var authOnPlaintext atomic.Bool
	ctx, account, _ := startTLSFixture(t, false, &authOnPlaintext)
	err := TestConnection(ctx, account)
	if err == nil || err.Error() != "SMTP server does not offer STARTTLS" {
		t.Fatalf("missing STARTTLS: %v", err)
	}
	if authOnPlaintext.Load() {
		t.Fatal("credentials were sent over plaintext")
	}
}
