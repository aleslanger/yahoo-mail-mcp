package rules

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aleslanger/yahoo-mail-mcp/internal/batch"
	"github.com/aleslanger/yahoo-mail-mcp/internal/config"
	"github.com/aleslanger/yahoo-mail-mcp/internal/mail"
)

var now = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

type recorder struct {
	filters  []mail.SearchFilter
	prepared []batch.Input
	executed []string
	searchFn func(folder string) (uint32, []uint32, error)
}

func (r *recorder) ops() Operations {
	return Operations{
		Search: func(_ context.Context, _ config.Account, folder string, filter mail.SearchFilter, limit int) (uint32, []uint32, error) {
			r.filters = append(r.filters, filter)
			if limit != maxRuleActions {
				return 0, nil, errors.New("unexpected limit")
			}
			if r.searchFn != nil {
				return r.searchFn(folder)
			}
			return 7, []uint32{3, 4}, nil
		},
		Headers: func(_ context.Context, _ config.Account, _ string, _ uint32, uids []uint32) ([]mail.Header, error) {
			out := []mail.Header{}
			for _, uid := range uids {
				out = append(out, mail.Header{UID: uid, Subject: "candidate"})
			}
			return out, nil
		},
		Trash: func(context.Context, config.Account) (string, error) { return "Trash", nil },
		Prepare: func(_ context.Context, _ config.Account, _ string, in batch.Input) (batch.Preview, error) {
			r.prepared = append(r.prepared, in)
			msgs := []mail.Message{}
			for _, uid := range in.UIDs {
				msgs = append(msgs, mail.Message{UID: uid, Subject: "candidate"})
			}
			return batch.Preview{Token: "token", Messages: msgs}, nil
		},
		Execute: func(_ context.Context, _ config.Account, _ string, token string, confirmed bool) (batch.Outcome, error) {
			if !confirmed {
				return batch.Outcome{}, errors.New("not confirmed")
			}
			r.executed = append(r.executed, token)
			return batch.Outcome{Status: "completed"}, nil
		},
	}
}

func cfgWith(automation bool, rules ...config.Rule) config.Config {
	return config.Config{JournalDir: "/journal", Accounts: []config.Account{{Alias: "personal", AllowOrganize: true, AllowAutomation: automation}}, Rules: rules}
}

func TestDryRunListsCandidatesWithoutChanges(t *testing.T) {
	rec := &recorder{}
	rule := config.Rule{Name: "old-promos", Account: "personal", Folder: "Promotions", Match: config.RuleMatch{OlderThanDays: 30}, Action: "trash"}
	reports := Run(context.Background(), cfgWith(false, rule), now, false, rec.ops())
	if len(reports) != 1 || reports[0].Error != "" || reports[0].Applied || len(reports[0].Candidates) != 2 {
		t.Fatalf("dry-run report: %+v", reports)
	}
	if len(rec.prepared) != 0 || len(rec.executed) != 0 {
		t.Fatalf("dry-run changed mail: prepared=%v executed=%v", rec.prepared, rec.executed)
	}
	if rec.filters[0].Before != "2026-09-14" {
		t.Fatalf("older_than_days filter: %+v", rec.filters[0])
	}
}

func TestApplyRequiresAutomationPermission(t *testing.T) {
	rec := &recorder{}
	rule := config.Rule{Name: "news", Account: "personal", Folder: "INBOX", Match: config.RuleMatch{From: "news@"}, Action: "move", TargetFolder: "News"}
	reports := Run(context.Background(), cfgWith(false, rule), now, true, rec.ops())
	if reports[0].Error != "automation is disabled for account" || len(rec.filters) != 0 || len(rec.prepared) != 0 {
		t.Fatalf("apply without allow_automation: %+v filters=%v", reports, rec.filters)
	}
}

func TestApplyMapsActionsToConfirmedBatches(t *testing.T) {
	rec := &recorder{}
	rules := []config.Rule{
		{Name: "news", Account: "personal", Folder: "INBOX", Match: config.RuleMatch{From: "news@"}, Action: "move", TargetFolder: "News"},
		{Name: "promos", Account: "personal", Folder: "Promotions", Match: config.RuleMatch{OlderThanDays: 30}, Action: "trash"},
		{Name: "receipts", Account: "personal", Folder: "INBOX", Match: config.RuleMatch{Subject: "receipt"}, Action: "mark_read"},
		{Name: "boss", Account: "personal", Folder: "INBOX", Match: config.RuleMatch{From: "boss@"}, Action: "flag"},
	}
	reports := Run(context.Background(), cfgWith(true, rules...), now, true, rec.ops())
	for _, r := range reports {
		if r.Error != "" || !r.Applied || r.Outcome == nil || r.Outcome.Status != "completed" {
			t.Fatalf("report %+v", r)
		}
	}
	want := []batch.Input{
		{Account: "personal", Folder: "INBOX", UIDValidity: 7, UIDs: []uint32{3, 4}, Action: "move", Target: "News"},
		{Account: "personal", Folder: "Promotions", UIDValidity: 7, UIDs: []uint32{3, 4}, Action: "move", Target: "Trash"},
		{Account: "personal", Folder: "INBOX", UIDValidity: 7, UIDs: []uint32{3, 4}, Action: "mark_read"},
		{Account: "personal", Folder: "INBOX", UIDValidity: 7, UIDs: []uint32{3, 4}, Action: "flag"},
	}
	if !reflect.DeepEqual(rec.prepared, want) || len(rec.executed) != 4 {
		t.Fatalf("batches:\n got %+v\nwant %+v", rec.prepared, want)
	}
	if !rec.filters[2].Unread || rec.filters[3].Flagged == nil || *rec.filters[3].Flagged {
		t.Fatalf("idempotence criteria missing: mark_read=%+v flag=%+v", rec.filters[2], rec.filters[3])
	}
}

func TestRuleFailureDoesNotStopOtherRulesAndEmptyMatchSkipsBatch(t *testing.T) {
	rec := &recorder{searchFn: func(folder string) (uint32, []uint32, error) {
		if folder == "Broken" {
			return 0, nil, errors.New("folder selection failed")
		}
		return 7, []uint32{}, nil
	}}
	rules := []config.Rule{
		{Name: "broken", Account: "personal", Folder: "Broken", Match: config.RuleMatch{From: "a"}, Action: "flag"},
		{Name: "quiet", Account: "personal", Folder: "INBOX", Match: config.RuleMatch{From: "b"}, Action: "flag"},
	}
	reports := Run(context.Background(), cfgWith(true, rules...), now, true, rec.ops())
	if reports[0].Error != "folder selection failed" || reports[1].Error != "" || len(reports[1].Candidates) != 0 || len(rec.prepared) != 0 {
		t.Fatalf("reports: %+v prepared=%v", reports, rec.prepared)
	}
}
