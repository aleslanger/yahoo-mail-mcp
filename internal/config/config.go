package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var aliasPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// SMTP transport security modes; plaintext submission is never supported.
const (
	SMTPSecurityTLS      = "tls"
	SMTPSecuritySTARTTLS = "starttls"
	smtpSubmissionPort   = 587
)

type Account struct {
	Alias         string `json:"alias"`
	Address       string `json:"address"`
	IMAPHost      string `json:"imap_host"`
	IMAPPort      int    `json:"imap_port"`
	IMAPCAFile    string `json:"imap_ca_file,omitempty"`
	AllowOrganize bool   `json:"allow_organize"`
	SMTPHost      string `json:"smtp_host,omitempty"`
	SMTPPort      int    `json:"smtp_port,omitempty"`
	SMTPCAFile    string `json:"smtp_ca_file,omitempty"`
	SMTPSecurity  string `json:"smtp_security,omitempty"`
	TrashFolder   string `json:"trash_folder,omitempty"`
	AllowSend     bool   `json:"allow_send"`
}
type Config struct {
	Accounts    []Account `json:"accounts"`
	JournalDir  string    `json:"journal_dir,omitempty"`
	DownloadDir string    `json:"download_dir,omitempty"`
}

func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Config{}, err
	}
	if info.Size() > 1<<20 {
		return Config{}, errors.New("configuration exceeds 1 MiB")
	}
	dec := json.NewDecoder(io.LimitReader(f, 1<<20))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return c, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return c, errors.New("configuration must contain one JSON object under 1 MiB")
	}
	return validate(c)
}

func validate(c Config) (Config, error) {
	if c.JournalDir != "" && !filepath.IsAbs(c.JournalDir) {
		return c, errors.New("journal_dir must be an absolute path")
	}
	if c.DownloadDir != "" && !filepath.IsAbs(c.DownloadDir) {
		return c, errors.New("download_dir must be an absolute path")
	}
	seen := map[string]bool{}
	for i := range c.Accounts {
		a := &c.Accounts[i]
		if !aliasPattern.MatchString(a.Alias) || seen[a.Alias] {
			return c, errors.New("invalid or duplicate account alias")
		}
		seen[a.Alias] = true
		parsed, err := mail.ParseAddress(a.Address)
		if err != nil || parsed.Address != a.Address || strings.ContainsAny(a.Address, "\x00\r\n") {
			return c, fmt.Errorf("account %s has invalid address", a.Alias)
		}
		if a.IMAPHost == "" {
			a.IMAPHost = "imap.mail.yahoo.com"
		}
		if a.IMAPPort == 0 {
			a.IMAPPort = 993
		}
		if a.IMAPPort < 1 || a.IMAPPort > 65535 {
			return c, fmt.Errorf("account %s has invalid IMAP port", a.Alias)
		}
		if a.SMTPHost == "" {
			a.SMTPHost = "smtp.mail.yahoo.com"
		}
		if a.SMTPPort == 0 {
			a.SMTPPort = 465
		}
		if a.SMTPPort < 1 || a.SMTPPort > 65535 {
			return c, fmt.Errorf("account %s has invalid SMTP port", a.Alias)
		}
		if a.SMTPSecurity == "" {
			a.SMTPSecurity = SMTPSecurityTLS
			if a.SMTPPort == smtpSubmissionPort {
				a.SMTPSecurity = SMTPSecuritySTARTTLS
			}
		}
		if a.SMTPSecurity != SMTPSecurityTLS && a.SMTPSecurity != SMTPSecuritySTARTTLS {
			return c, fmt.Errorf("account %s has invalid smtp_security; use tls or starttls", a.Alias)
		}
		if strings.ContainsAny(a.TrashFolder, "\x00\r\n") {
			return c, fmt.Errorf("account %s has invalid trash_folder", a.Alias)
		}
		if a.AllowSend && c.JournalDir == "" {
			return c, fmt.Errorf("account %s allows send but journal_dir is missing", a.Alias)
		}
	}
	return c, nil
}

// Save replaces a non-secret configuration file atomically with mode 0600.
func Save(path string, c Config) error {
	if !filepath.IsAbs(path) {
		return errors.New("configuration path must be absolute")
	}
	if _, err := validate(c); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return errors.New("configuration path must be a regular file")
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil || len(data) > 1<<20 {
		return errors.New("configuration exceeds 1 MiB")
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return errors.New("cannot create temporary configuration")
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return errors.New("cannot write configuration")
	}
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return errors.New("cannot protect configuration")
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return errors.New("cannot sync configuration")
	}
	if err := tmp.Close(); err != nil {
		return errors.New("cannot close configuration")
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return errors.New("cannot replace configuration")
	}
	parent, err := os.Open(dir)
	if err != nil {
		return errors.New("cannot open configuration directory")
	}
	defer parent.Close()
	if err := parent.Sync(); err != nil {
		return errors.New("cannot sync configuration directory")
	}
	return nil
}
func (c Config) Account(alias string) (Account, bool) {
	for _, a := range c.Accounts {
		if a.Alias == alias {
			return a, true
		}
	}
	return Account{}, false
}
func Password(alias string) (string, error) {
	if !aliasPattern.MatchString(alias) {
		return "", errors.New("invalid account alias")
	}
	p := os.Getenv("YAHOO_MCP_PASSWORD_" + strings.ToUpper(alias))
	if p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.New("app password is not configured for this account")
	}
	dir := filepath.Join(home, ".config", "yahoo-mail-mcp")
	dirInfo, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "", errors.New("app password is not configured for this account")
	}
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode().Perm() != 0700 {
		return "", errors.New("credential directory must be private (0700)")
	}
	path := filepath.Join(dir, alias+"-password")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", errors.New("app password is not configured for this account")
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() < 1 || info.Size() > 1024 {
		return "", errors.New("credential file must be a private regular file (0600) under 1 KiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("cannot read app password file")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1025))
	if err != nil || len(data) > 1024 {
		return "", errors.New("cannot read app password file")
	}
	p = strings.TrimSuffix(string(data), "\n")
	if p == "" || strings.ContainsAny(p, "\x00\r\n") {
		return "", errors.New("invalid app password file")
	}
	return p, nil
}
