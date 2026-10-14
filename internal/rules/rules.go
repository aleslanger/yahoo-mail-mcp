// Package rules applies configured organizing rules through confirmed,
// journaled batches. Rules never send mail and never delete permanently.
package rules

import (
	"context"
	"errors"
	"time"

	"github.com/aleslanger/yahoo-mail-mcp/internal/batch"
	"github.com/aleslanger/yahoo-mail-mcp/internal/config"
	"github.com/aleslanger/yahoo-mail-mcp/internal/mail"
)

// maxRuleActions caps each rule per run at one batch.
const maxRuleActions = 20

const searchDateLayout = "2006-01-02"

// Operations are the mailbox calls a run needs; tests replace them.
type Operations struct {
	Search  func(ctx context.Context, a config.Account, folder string, filter mail.SearchFilter, limit int) (uint32, []uint32, error)
	Headers func(ctx context.Context, a config.Account, folder string, validity uint32, uids []uint32) ([]mail.Header, error)
	Trash   func(ctx context.Context, a config.Account) (string, error)
	Prepare func(ctx context.Context, a config.Account, dir string, in batch.Input) (batch.Preview, error)
	Execute func(ctx context.Context, a config.Account, dir, token string, confirmed bool) (batch.Outcome, error)
}

// RealOperations uses the IMAP and batch packages.
func RealOperations() Operations {
	return Operations{Search: mail.SearchOldest, Headers: mail.FetchHeaders, Trash: mail.TrashFolder, Prepare: batch.Prepare, Execute: batch.Execute}
}

type Candidate struct {
	UID     uint32    `json:"uid"`
	From    string    `json:"from,omitempty"`
	Subject string    `json:"subject"`
	Date    time.Time `json:"date,omitempty"`
}

// Report describes what one rule matched and, when applied, what happened.
type Report struct {
	Rule       string         `json:"rule"`
	Account    string         `json:"account"`
	Folder     string         `json:"folder"`
	Action     string         `json:"action"`
	Target     string         `json:"target_folder,omitempty"`
	Applied    bool           `json:"applied"`
	Candidates []Candidate    `json:"candidates"`
	Outcome    *batch.Outcome `json:"outcome,omitempty"`
	Error      string         `json:"error,omitempty"`
}

// Run evaluates every rule. Without apply it only lists candidates.
// A failing rule is reported and does not stop the others.
func Run(ctx context.Context, cfg config.Config, now time.Time, apply bool, ops Operations) []Report {
	reports := make([]Report, 0, len(cfg.Rules))
	for _, rule := range cfg.Rules {
		report := Report{Rule: rule.Name, Account: rule.Account, Folder: rule.Folder, Action: rule.Action, Target: rule.TargetFolder, Candidates: []Candidate{}}
		if err := runRule(ctx, cfg, rule, now, apply, ops, &report); err != nil {
			report.Error = err.Error()
		}
		reports = append(reports, report)
	}
	return reports
}

func runRule(ctx context.Context, cfg config.Config, rule config.Rule, now time.Time, apply bool, ops Operations, report *Report) error {
	a, ok := cfg.Account(rule.Account)
	if !ok {
		return errors.New("unknown account")
	}
	if apply && !a.AllowAutomation {
		return errors.New("automation is disabled for account")
	}
	validity, uids, err := ops.Search(ctx, a, rule.Folder, filterFor(rule, now), maxRuleActions)
	if err != nil || len(uids) == 0 {
		return err
	}
	if !apply {
		headers, err := ops.Headers(ctx, a, rule.Folder, validity, uids)
		if err != nil {
			return err
		}
		for _, h := range headers {
			report.Candidates = append(report.Candidates, Candidate{UID: h.UID, From: h.From, Subject: h.Subject, Date: h.Date})
		}
		return nil
	}
	in, err := batchInput(ctx, a, rule, validity, uids, ops)
	if err != nil {
		return err
	}
	report.Target = in.Target
	preview, err := ops.Prepare(ctx, a, cfg.JournalDir, in)
	if err != nil {
		return err
	}
	for _, m := range preview.Messages {
		report.Candidates = append(report.Candidates, Candidate{UID: m.UID, From: m.From, Subject: m.Subject})
	}
	outcome, err := ops.Execute(ctx, a, cfg.JournalDir, preview.Token, true)
	report.Applied = true
	report.Outcome = &outcome
	return err
}

// filterFor adds criteria that stop a rule from matching messages it already
// handled, so repeated runs are idempotent.
func filterFor(rule config.Rule, now time.Time) mail.SearchFilter {
	m := rule.Match
	filter := mail.SearchFilter{From: m.From, To: m.To, Subject: m.Subject, Unread: m.Unread}
	if m.OlderThanDays > 0 {
		filter.Before = now.AddDate(0, 0, -m.OlderThanDays).Format(searchDateLayout)
	}
	switch rule.Action {
	case "mark_read":
		filter.Unread = true
	case "flag":
		notFlagged := false
		filter.Flagged = &notFlagged
	}
	return filter
}

func batchInput(ctx context.Context, a config.Account, rule config.Rule, validity uint32, uids []uint32, ops Operations) (batch.Input, error) {
	in := batch.Input{Account: a.Alias, Folder: rule.Folder, UIDValidity: validity, UIDs: uids, Action: rule.Action}
	switch rule.Action {
	case "move":
		in.Target = rule.TargetFolder
	case "trash":
		trash, err := ops.Trash(ctx, a)
		if err != nil {
			return batch.Input{}, err
		}
		if trash == rule.Folder {
			return batch.Input{}, errors.New("rule folder is already Trash")
		}
		in.Action, in.Target = "move", trash
	}
	return in, nil
}
