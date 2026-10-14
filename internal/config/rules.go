package config

import (
	"errors"
	"fmt"
	"strings"
)

const maxRuleAgeDays = 3650

// RuleMatch selects messages; all set fields must match. Text fields are
// IMAP header substring searches.
type RuleMatch struct {
	From          string `json:"from,omitempty"`
	To            string `json:"to,omitempty"`
	Subject       string `json:"subject,omitempty"`
	OlderThanDays int    `json:"older_than_days,omitempty"`
	Unread        bool   `json:"unread,omitempty"`
}

// Rule organizes matching messages of one folder. Rules never send mail and
// never delete permanently.
type Rule struct {
	Name         string    `json:"name"`
	Account      string    `json:"account"`
	Folder       string    `json:"folder"`
	Match        RuleMatch `json:"match"`
	Action       string    `json:"action"`
	TargetFolder string    `json:"target_folder,omitempty"`
}

func validateRules(c Config) error {
	names := map[string]bool{}
	for _, r := range c.Rules {
		if r.Name == "" || names[r.Name] {
			return fmt.Errorf("rule name %q is empty or duplicate", r.Name)
		}
		names[r.Name] = true
		if err := validateRule(c, r); err != nil {
			return fmt.Errorf("rule %s: %w", r.Name, err)
		}
	}
	return nil
}

func validateRule(c Config, r Rule) error {
	if _, ok := c.Account(r.Account); !ok {
		return errors.New("unknown account")
	}
	for _, text := range []string{r.Name, r.Folder, r.TargetFolder, r.Match.From, r.Match.To, r.Match.Subject} {
		if len(text) > 256 || strings.ContainsAny(text, "\x00\r\n") {
			return errors.New("invalid text field")
		}
	}
	if r.Folder == "" {
		return errors.New("folder is required")
	}
	m := r.Match
	if m.OlderThanDays < 0 || m.OlderThanDays > maxRuleAgeDays {
		return fmt.Errorf("older_than_days must be 0..%d", maxRuleAgeDays)
	}
	if m.From == "" && m.To == "" && m.Subject == "" && m.OlderThanDays == 0 {
		return errors.New("at least one match of from, to, subject or older_than_days is required")
	}
	switch r.Action {
	case "move":
		if r.TargetFolder == "" || r.TargetFolder == r.Folder {
			return errors.New("move needs a different target_folder")
		}
	case "trash", "mark_read", "flag":
		if r.TargetFolder != "" {
			return errors.New("only move accepts target_folder")
		}
	default:
		return errors.New("action must be move, trash, mark_read or flag")
	}
	return nil
}
