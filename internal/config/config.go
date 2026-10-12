package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

var aliasPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

type Account struct {
	Alias         string `json:"alias"`
	Address       string `json:"address"`
	IMAPHost      string `json:"imap_host"`
	IMAPPort      int    `json:"imap_port"`
	AllowOrganize bool   `json:"allow_organize"`
}
type Config struct {
	Accounts []Account `json:"accounts"`
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
	seen := map[string]bool{}
	for i := range c.Accounts {
		a := &c.Accounts[i]
		if !aliasPattern.MatchString(a.Alias) || seen[a.Alias] {
			return c, errors.New("invalid or duplicate account alias")
		}
		seen[a.Alias] = true
		if !strings.Contains(a.Address, "@") {
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
	}
	return c, nil
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
	if p == "" {
		return "", errors.New("app password is not configured for this account")
	}
	return p, nil
}
