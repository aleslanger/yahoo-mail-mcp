package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAndNoSecretField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"accounts":[{"alias":"main","address":"user@yahoo.com"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Accounts) != 1 || c.Accounts[0].IMAPHost != "imap.mail.yahoo.com" {
		t.Fatalf("unexpected config: %+v", c)
	}
	if strings.Contains(strings.ToLower(strings.Join([]string{c.Accounts[0].Alias, c.Accounts[0].Address}, " ")), "password") {
		t.Fatal("secret exposed")
	}
}
func TestRejectDuplicateAlias(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"accounts":[{"alias":"main","address":"user@yahoo.com"},{"alias":"main","address":"other@yahoo.com"}]}`), 0600)
	if _, err := Load(path); err == nil {
		t.Fatal("duplicate alias accepted")
	}
}
func TestPasswordFromEnvironmentOnly(t *testing.T) {
	t.Setenv("YAHOO_MCP_PASSWORD_MAIN", "example-secret")
	p, err := Password("main")
	if err != nil || p != "example-secret" {
		t.Fatal("password lookup failed")
	}
	if _, err := Password("main\nBAD"); err == nil {
		t.Fatal("invalid alias accepted")
	}
}
func TestRejectSecretInConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"accounts":[{"alias":"main","address":"user@yahoo.com","password":"secret"}]}`), 0600)
	if _, err := Load(path); err == nil {
		t.Fatal("plaintext credential field accepted")
	}
}
