// Package imaptest runs an in-process TLS IMAP server for tests in other
// packages. Connections go through net.Pipe, so no port is opened.
// Only test files import it; it is never linked into the binary.
package imaptest

import (
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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aleslanger/yahoo-mail-mcp/internal/config"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

const (
	serverPort = 993
	certSerial = 128
)

// Options configure the fixture account and server.
type Options struct {
	Alias    string
	Address  string
	Password string
	Folders  []string
	Move     bool
}

// Server is a running fixture. Pass Dial to mail.WithDialTLS.
type Server struct {
	Account config.Account
	Dial    func(string, *imapclient.Options) (*imapclient.Client, error)
	roots   *x509.CertPool
	pass    string
}

type pipeListener struct {
	connections chan net.Conn
	closed      chan struct{}
	closeOnce   sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.connections:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}
func (l *pipeListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: serverPort}
}

// New starts the server, stores the account password in the environment and
// writes the fixture CA to a temporary file referenced by Account.IMAPCAFile.
func New(t testing.TB, o Options) *Server {
	t.Helper()
	certPEM, cert := certificate(t)
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	mem := imapmemserver.New()
	user := imapmemserver.NewUser(o.Address, o.Password)
	for _, folder := range o.Folders {
		if err := user.Create(folder, nil); err != nil {
			t.Fatal(err)
		}
	}
	mem.AddUser(user)
	caps := imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapUIDPlus: {}}
	if o.Move {
		caps[imap.CapMove] = struct{}{}
	}
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps: caps,
	})
	listener := &pipeListener{connections: make(chan net.Conn), closed: make(chan struct{})}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })
	t.Setenv("YAHOO_MCP_PASSWORD_"+strings.ToUpper(o.Alias), o.Password)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	return &Server{
		Account: config.Account{Alias: o.Alias, Address: o.Address, IMAPHost: "127.0.0.1", IMAPPort: serverPort, IMAPCAFile: caPath},
		Dial:    pipeDialer(listener, cert),
		roots:   roots,
		pass:    o.Password,
	}
}

func pipeDialer(listener *pipeListener, cert tls.Certificate) func(string, *imapclient.Options) (*imapclient.Client, error) {
	return func(addr string, options *imapclient.Options) (*imapclient.Client, error) {
		clientConn, serverConn := net.Pipe()
		select {
		case listener.connections <- tls.Server(serverConn, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}):
		case <-listener.closed:
			_ = clientConn.Close()
			_ = serverConn.Close()
			return nil, net.ErrClosed
		}
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		clientTLS := options.TLSConfig.Clone()
		clientTLS.ServerName = host
		secureConn := tls.Client(clientConn, clientTLS)
		if err := secureConn.Handshake(); err != nil {
			_ = clientConn.Close()
			_ = serverConn.Close()
			return nil, err
		}
		return imapclient.New(secureConn, options), nil
	}
}

// Append stores raw MIME in folder and returns its UID.
func (s *Server) Append(t testing.TB, folder, raw string) uint32 {
	t.Helper()
	client, err := s.Dial(net.JoinHostPort(s.Account.IMAPHost, "993"), &imapclient.Options{TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: s.roots}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Login(s.Account.Address, s.pass).Wait(); err != nil {
		t.Fatal(err)
	}
	cmd := client.Append(folder, int64(len(raw)), nil)
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

func certificate(t testing.TB) ([]byte, tls.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), certSerial))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "IMAP test fixture"},
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
	return certPEM, cert
}
