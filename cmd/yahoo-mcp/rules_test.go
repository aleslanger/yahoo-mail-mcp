package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aleslanger/yahoo-mail-mcp/internal/batch"
	"github.com/aleslanger/yahoo-mail-mcp/internal/config"
	"github.com/aleslanger/yahoo-mail-mcp/internal/mail"
	"github.com/aleslanger/yahoo-mail-mcp/internal/rules"
)

func TestRulesRunDefaultsToDryRunAndReportsFailures(t *testing.T) {
	cfg := config.Config{JournalDir: "/journal", Accounts: []config.Account{{Alias: "personal", AllowOrganize: true, AllowAutomation: true}}, Rules: []config.Rule{{Name: "news", Account: "personal", Folder: "INBOX", Match: config.RuleMatch{From: "news@"}, Action: "flag"}}}
	prepared := 0
	searchErr := error(nil)
	ops := rules.Operations{
		Search: func(context.Context, config.Account, string, mail.SearchFilter, int) (uint32, []uint32, error) {
			return 1, []uint32{5}, searchErr
		},
		Headers: func(context.Context, config.Account, string, uint32, []uint32) ([]mail.Header, error) {
			return []mail.Header{{UID: 5, Subject: "Weekly news"}}, nil
		},
		Prepare: func(context.Context, config.Account, string, batch.Input) (batch.Preview, error) {
			prepared++
			return batch.Preview{}, nil
		},
	}
	var out bytes.Buffer
	if err := runRules(context.Background(), cfg, nil, &out, ops); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	var report struct {
		Applied bool           `json:"applied"`
		Rules   []rules.Report `json:"rules"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Applied || len(report.Rules) != 1 || report.Rules[0].Candidates[0].Subject != "Weekly news" || prepared != 0 {
		t.Fatalf("dry-run report: %s, %v, prepared=%d", out.String(), err, prepared)
	}
	if err := runRules(context.Background(), cfg, []string{"--force"}, &out, ops); err == nil {
		t.Fatal("unknown flag accepted")
	}
	searchErr = errors.New("folder selection failed")
	if err := runRules(context.Background(), cfg, nil, &bytes.Buffer{}, ops); err == nil {
		t.Fatal("failed rule did not fail the run")
	}
}
