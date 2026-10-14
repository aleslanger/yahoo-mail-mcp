package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/ales/yahoo-mail-mcp/internal/config"
	"github.com/ales/yahoo-mail-mcp/internal/rules"
)

const rulesRunTimeout = 10 * time.Minute

// runRules evaluates configured rules; without --apply it only lists candidates.
// It writes a JSON report and fails when any rule reported an error.
func runRules(cfg config.Config, args []string, out io.Writer, ops rules.Operations) error {
	apply := false
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "--apply":
		apply = true
	default:
		return errors.New("usage: yahoo-mcp rules-run [--apply]")
	}
	if len(cfg.Rules) == 0 {
		return errors.New("no rules configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), rulesRunTimeout)
	defer cancel()
	reports := rules.Run(ctx, cfg, time.Now(), apply, ops)
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(map[string]any{"applied": apply, "rules": reports}); err != nil {
		return errors.New("cannot write rules report")
	}
	failed := 0
	for _, r := range reports {
		if r.Error != "" {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d rule(s) failed; see report", failed)
	}
	return nil
}
