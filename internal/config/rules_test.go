package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadRules(t *testing.T, rules string) (Config, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{"journal_dir":"` + filepath.Join(dir, "journal") + `","accounts":[{"alias":"personal","address":"a@example.com","allow_organize":true,"allow_automation":true}],"rules":[` + rules + `]}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestRulesValidation(t *testing.T) {
	cases := []struct {
		name    string
		rule    string
		wantErr string
	}{
		{name: "valid move", rule: `{"name":"news","account":"personal","folder":"INBOX","match":{"from":"news@example.com"},"action":"move","target_folder":"News"}`},
		{name: "valid age-based trash", rule: `{"name":"old","account":"personal","folder":"Promotions","match":{"older_than_days":30},"action":"trash"}`},
		{name: "unknown account", rule: `{"name":"x","account":"other","folder":"INBOX","match":{"from":"a"},"action":"flag"}`, wantErr: "unknown account"},
		{name: "empty match", rule: `{"name":"x","account":"personal","folder":"INBOX","match":{},"action":"mark_read"}`, wantErr: "at least one match"},
		{name: "unread alone is not specific", rule: `{"name":"x","account":"personal","folder":"INBOX","match":{"unread":true},"action":"mark_read"}`, wantErr: "at least one match"},
		{name: "sending is not an action", rule: `{"name":"x","account":"personal","folder":"INBOX","match":{"from":"a"},"action":"send"}`, wantErr: "action"},
		{name: "permanent delete is not an action", rule: `{"name":"x","account":"personal","folder":"Trash","match":{"from":"a"},"action":"delete"}`, wantErr: "action"},
		{name: "move needs target", rule: `{"name":"x","account":"personal","folder":"INBOX","match":{"from":"a"},"action":"move"}`, wantErr: "target_folder"},
		{name: "flag rejects target", rule: `{"name":"x","account":"personal","folder":"INBOX","match":{"from":"a"},"action":"flag","target_folder":"News"}`, wantErr: "target_folder"},
		{name: "age out of range", rule: `{"name":"x","account":"personal","folder":"INBOX","match":{"older_than_days":4000},"action":"trash"}`, wantErr: "older_than_days"},
		{name: "control characters", rule: `{"name":"x","account":"personal","folder":"INBOX","match":{"subject":"a\r\nb"},"action":"flag"}`, wantErr: "invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadRules(t, tc.rule)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("valid rule rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestRuleNamesMustBeUnique(t *testing.T) {
	rule := `{"name":"same","account":"personal","folder":"INBOX","match":{"from":"a"},"action":"flag"}`
	if _, err := loadRules(t, rule+","+rule); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate rule names: %v", err)
	}
}

func TestAutomationRequiresOrganizeAndJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"accounts":[{"alias":"personal","address":"a@example.com","allow_automation":true}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("allow_automation accepted without allow_organize and journal_dir")
	}
}
