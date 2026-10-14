package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAndNoSecretField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"accounts":[{"alias":"main","address":"account@example.com"}]}`), 0600); err != nil {
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
	os.WriteFile(path, []byte(`{"accounts":[{"alias":"main","address":"account@example.com"},{"alias":"main","address":"account@example.com"}]}`), 0600)
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
func TestPasswordFromPrivateFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YAHOO_MCP_PASSWORD_MAIN", "")
	dir := filepath.Join(home, ".config", "yahoo-mail-mcp")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "main-password")
	if err := os.WriteFile(path, []byte("file-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := Password("main"); err != nil || got != "file-secret" {
		t.Fatalf("private file lookup failed: %v", err)
	}
	t.Setenv("YAHOO_MCP_PASSWORD_MAIN", "environment-secret")
	if got, err := Password("main"); err != nil || got != "environment-secret" {
		t.Fatalf("environment did not take precedence: %v", err)
	}
	t.Setenv("YAHOO_MCP_PASSWORD_MAIN", "")
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Password("main"); err == nil {
		t.Fatal("read an accessible credential file")
	}
}
func TestRejectSecretInConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"accounts":[{"alias":"main","address":"account@example.com","password":"secret"}]}`), 0600)
	if _, err := Load(path); err == nil {
		t.Fatal("plaintext credential field accepted")
	}
}

func TestSendRequiresAbsoluteJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, raw := range []string{
		`{"accounts":[{"alias":"main","address":"account@example.com","allow_send":true}]}`,
		`{"journal_dir":"relative","accounts":[{"alias":"main","address":"account@example.com","allow_send":true}]}`,
	} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("accepted unsafe send config: %s", raw)
		}
	}
	valid := `{"journal_dir":"` + t.TempDir() + `","accounts":[{"alias":"main","address":"account@example.com","allow_send":true}]}`
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil || c.Accounts[0].SMTPHost != "smtp.mail.yahoo.com" || c.Accounts[0].SMTPPort != 465 {
		t.Fatalf("send defaults: %+v, %v", c, err)
	}
}

func TestDownloadDirMustBeAbsolute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"download_dir":"relative","accounts":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("relative download_dir accepted")
	}
}

func TestSaveAccountConfiguration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	c := Config{Accounts: []Account{{Alias: "work", Address: "account@example.com"}}}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("configuration permissions: %v, %v", info, err)
	}
	loaded, err := Load(path)
	if err != nil || len(loaded.Accounts) != 1 || loaded.Accounts[0].Address != "account@example.com" {
		t.Fatalf("saved account: %+v, %v", loaded, err)
	}
	bad := Config{Accounts: []Account{{Alias: "work", Address: "Name <account@example.com>"}}}
	if err := Save(path, bad); err == nil {
		t.Fatal("display-name address accepted")
	}
	if err := os.Symlink(path, filepath.Join(dir, "link.json")); err != nil {
		t.Fatal(err)
	}
	if err := Save(filepath.Join(dir, "link.json"), c); err == nil {
		t.Fatal("configuration symlink accepted")
	}
}

func TestTrashOverrideRejectsControlCharacters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"accounts":[{"alias":"personal","address":"account@example.com","trash_folder":"Trash\nINBOX"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("unsafe Trash folder override accepted")
	}
}

func TestSMTPSecurityDefaultsFromPortAndRejectsUnknownMode(t *testing.T) {
	cases := []struct {
		name    string
		account string
		want    string
		wantErr bool
	}{
		{name: "implicit TLS by default", account: `{"alias":"main","address":"account@example.com"}`, want: SMTPSecurityTLS},
		{name: "port 587 implies STARTTLS", account: `{"alias":"main","address":"account@example.com","smtp_port":587}`, want: SMTPSecuritySTARTTLS},
		{name: "explicit STARTTLS", account: `{"alias":"main","address":"account@example.com","smtp_port":2525,"smtp_security":"starttls"}`, want: SMTPSecuritySTARTTLS},
		{name: "explicit TLS on 587", account: `{"alias":"main","address":"account@example.com","smtp_port":587,"smtp_security":"tls"}`, want: SMTPSecurityTLS},
		{name: "plaintext rejected", account: `{"alias":"main","address":"account@example.com","smtp_security":"none"}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(`{"accounts":[`+tc.account+`]}`), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(path)
			if tc.wantErr {
				if err == nil {
					t.Fatal("unsupported smtp_security accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := c.Accounts[0].SMTPSecurity; got != tc.want {
				t.Fatalf("smtp_security = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDeleteRequiresAbsoluteJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"accounts":[{"alias":"main","address":"account@example.com","allow_delete":true}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("allow_delete accepted without journal_dir")
	}
	journal := filepath.Join(t.TempDir(), "journal")
	if err := os.WriteFile(path, []byte(`{"journal_dir":"`+journal+`","accounts":[{"alias":"main","address":"account@example.com","allow_delete":true}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil || !c.Accounts[0].AllowDelete {
		t.Fatalf("allow_delete with journal_dir: %+v, %v", c, err)
	}
}
