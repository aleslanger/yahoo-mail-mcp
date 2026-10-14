package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
)

const (
	defaultHTTPListen = "127.0.0.1:8765"
	httpTokenFile     = "http-token"
	httpTokenBytes    = 32
)

// validateLoopbackListen accepts only explicit loopback addresses: the HTTP
// transport is meant for clients on the same machine.
func validateLoopbackListen(listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return errors.New("http_listen must be host:port")
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return errors.New("http_listen has invalid port")
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("http_listen must be a loopback address such as 127.0.0.1")
	}
	return nil
}

// HTTPToken returns the bearer token required by the HTTP transport.
func HTTPToken() (string, error) {
	return readSecretFile(httpTokenFile, errors.New("HTTP token is not configured; run yahoo-mcp http-token"))
}

// CreateHTTPToken generates a 256-bit token in a new private file. It never
// overwrites an existing token; delete the file first to rotate it.
func CreateHTTPToken() (string, error) {
	dir, err := credentialDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", errors.New("cannot create credential directory")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return "", errors.New("credential directory must be private (0700)")
	}
	var random [httpTokenBytes]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", errors.New("cannot generate HTTP token")
	}
	token := hex.EncodeToString(random[:])
	path := filepath.Join(dir, httpTokenFile)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", errors.New("HTTP token already exists or cannot be created; delete it first to rotate")
	}
	_, writeErr := f.WriteString(token + "\n")
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if closeErr := f.Close(); writeErr != nil || closeErr != nil {
		if removeErr := os.Remove(path); removeErr != nil {
			return "", errors.New("cannot write HTTP token; remove the partial token file manually")
		}
		return "", errors.New("cannot write HTTP token")
	}
	return token, nil
}
