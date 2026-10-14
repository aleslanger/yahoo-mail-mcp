package send

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"os"
	"sync"
	"time"

	"github.com/aleslanger/yahoo-mail-mcp/internal/config"
	"golang.org/x/time/rate"
)

type dialKey struct{}
type dialFunc func(context.Context, string, *tls.Config) (net.Conn, error)

const maxConcurrentSMTPConnections = 2
const minSMTPLoginInterval = 2 * time.Second

var smtpConnectionSlots = make(chan struct{}, maxConcurrentSMTPConnections)
var smtpLoginPacer = rate.NewLimiter(rate.Every(minSMTPLoginInterval), 1)

func Send(ctx context.Context, a config.Account, journalDir string, in Input) (Receipt, error) {
	if !a.AllowSend {
		return Receipt{}, errors.New("send permission is disabled for account")
	}
	p, err := prepare(a.Address, in)
	if err != nil {
		return Receipt{}, err
	}
	return sendPrepared(ctx, a, journalDir, in.IdempotencyKey, p)
}

func sendPrepared(ctx context.Context, a config.Account, journalDir, key string, p prepared) (Receipt, error) {
	j, err := newJournal(journalDir, a.Alias, key)
	if err != nil {
		return Receipt{}, err
	}
	receipt, existed, err := j.begin(p.digest, p.messageID)
	if err != nil || existed {
		return receipt, err
	}
	password, err := config.Password(a.Alias)
	if err != nil {
		return abortBeforeSubmission(j, p, err)
	}
	tlsConfig, err := smtpTLSConfig(a)
	if err != nil {
		return abortBeforeSubmission(j, p, err)
	}
	bodyStarted, err := submit(ctx, a, password, tlsConfig, p)
	if err != nil {
		if !bodyStarted {
			return abortBeforeSubmission(j, p, errors.New("SMTP failed before message submission; retry with the same idempotency_key after resolving the error"))
		}
		return Receipt{Status: "uncertain", MessageID: p.messageID, Note: "SMTP submission outcome uncertain; inspect Sent folder before any new attempt"}, nil
	}
	if err := j.confirm(p.digest, p.messageID); err != nil {
		return Receipt{Status: "uncertain", MessageID: p.messageID, Note: "SMTP acknowledged message, but confirmation could not be persisted"}, nil
	}
	return Receipt{Status: "confirmed", MessageID: p.messageID}, nil
}

func abortBeforeSubmission(j journal, p prepared, cause error) (Receipt, error) {
	if err := j.discardUnsent(p.digest, p.messageID); err == nil {
		return Receipt{}, cause
	}
	return Receipt{Status: "uncertain", MessageID: p.messageID, Note: "cannot verify removal of unsent attempt; inspect journal before retrying"}, nil
}

func smtpTLSConfig(a config.Account) (*tls.Config, error) {
	cfg := &tls.Config{ServerName: a.SMTPHost, MinVersion: tls.VersionTLS12}
	if a.SMTPCAFile == "" {
		return cfg, nil
	}
	f, err := os.Open(a.SMTPCAFile)
	if err != nil {
		return nil, errors.New("cannot read SMTP CA certificate")
	}
	pem, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	_ = f.Close()
	if err != nil || len(pem) > 1<<20 {
		return nil, errors.New("cannot read SMTP CA certificate")
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, errors.New("cannot load system CA certificates")
	}
	if !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("invalid SMTP CA certificate")
	}
	cfg.RootCAs = roots
	return cfg, nil
}

func submit(ctx context.Context, a config.Account, password string, tlsConfig *tls.Config, p prepared) (bool, error) {
	client, closeClient, err := connectSMTP(ctx, a, password, tlsConfig)
	if err != nil {
		return false, err
	}
	defer closeClient()
	if err := client.Mail(a.Address); err != nil {
		return false, err
	}
	for _, recipient := range p.envelope {
		if err := client.Rcpt(recipient); err != nil {
			return false, err
		}
	}
	w, err := client.Data()
	if err != nil {
		return false, err
	}
	if _, err := w.Write(p.message); err != nil {
		_ = w.Close()
		return true, err
	}
	if err := w.Close(); err != nil {
		return true, err
	}
	_ = client.Quit()
	return true, nil
}

// TestConnection authenticates with SMTP but never starts a message transaction.
func TestConnection(ctx context.Context, a config.Account) error {
	password, err := config.Password(a.Alias)
	if err != nil {
		return err
	}
	tlsConfig, err := smtpTLSConfig(a)
	if err != nil {
		return err
	}
	client, closeClient, err := connectSMTP(ctx, a, password, tlsConfig)
	if err != nil {
		return err
	}
	defer closeClient()
	_ = client.Quit()
	return nil
}

func connectSMTP(ctx context.Context, a config.Account, password string, tlsConfig *tls.Config) (*smtp.Client, func(), error) {
	queueCtx, stopWaiting := context.WithTimeout(ctx, 10*time.Second)
	defer stopWaiting()
	if err := queueCtx.Err(); err != nil {
		return nil, nil, errors.New("SMTP connection request canceled")
	}
	select {
	case smtpConnectionSlots <- struct{}{}:
	case <-queueCtx.Done():
		return nil, nil, errors.New("SMTP connection capacity unavailable")
	}
	releaseSlot := func() { <-smtpConnectionSlots }
	addr := net.JoinHostPort(a.SMTPHost, fmt.Sprint(a.SMTPPort))
	dial := dialFunc(func(ctx context.Context, address string, cfg *tls.Config) (net.Conn, error) {
		netDialer := &net.Dialer{Timeout: 10 * time.Second}
		if cfg == nil {
			return netDialer.DialContext(ctx, "tcp", address)
		}
		return (&tls.Dialer{NetDialer: netDialer, Config: cfg}).DialContext(ctx, "tcp", address)
	})
	startTLS := a.SMTPSecurity == config.SMTPSecuritySTARTTLS
	dialConfig := tlsConfig
	if startTLS {
		dialConfig = nil
	}
	if injected, ok := ctx.Value(dialKey{}).(dialFunc); ok {
		dial = injected
	} else if err := smtpLoginPacer.Wait(queueCtx); err != nil {
		releaseSlot()
		return nil, nil, errors.New("SMTP login rate capacity unavailable")
	}
	if err := queueCtx.Err(); err != nil {
		releaseSlot()
		return nil, nil, errors.New("SMTP connection request canceled")
	}
	conn, err := dial(ctx, addr, dialConfig)
	if err != nil {
		releaseSlot()
		return nil, nil, errors.New("SMTP TLS connection failed")
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	done := make(chan struct{})
	var closeOnce sync.Once
	closeConn := func() { closeOnce.Do(func() { close(done); _ = conn.Close(); releaseSlot() }) }
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	client, err := smtp.NewClient(conn, a.SMTPHost)
	if err != nil {
		closeConn()
		return nil, nil, errors.New("SMTP greeting failed")
	}
	if startTLS {
		if err := upgradeSTARTTLS(client, tlsConfig); err != nil {
			_ = client.Close()
			closeConn()
			return nil, nil, err
		}
	}
	if err := client.Auth(smtp.PlainAuth("", a.Address, password, a.SMTPHost)); err != nil {
		_ = client.Close()
		closeConn()
		return nil, nil, errors.New("SMTP authentication failed")
	}
	return client, func() { _ = client.Close(); closeConn() }, nil
}

// upgradeSTARTTLS refuses to continue unless the server offers STARTTLS, so
// credentials are never sent over a plaintext connection.
func upgradeSTARTTLS(client *smtp.Client, tlsConfig *tls.Config) error {
	if ok, _ := client.Extension("STARTTLS"); !ok {
		return errors.New("SMTP server does not offer STARTTLS")
	}
	if err := client.StartTLS(tlsConfig); err != nil {
		return errors.New("SMTP STARTTLS negotiation failed")
	}
	return nil
}
