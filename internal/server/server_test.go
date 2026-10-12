package server

import (
	"context"
	"github.com/ales/yahoo-mail-mcp/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"testing"
)

func TestDiscoveryAndPermission(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(config.Config{Accounts: []config.Account{{Alias: "personal", Address: "user@yahoo.com"}}})
	st, ct := mcp.NewInMemoryTransports()
	if _, err := s.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	names := map[string]bool{}
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		names[tool.Name] = true
	}
	for _, name := range []string{"account_list", "folder_list", "email_list", "email_get", "email_move", "email_copy", "email_mark_read"} {
		if !names[name] {
			t.Errorf("missing tool %s", name)
		}
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "email_move", Arguments: map[string]any{"account": "personal", "folder": "INBOX", "target_folder": "Archive", "uid": 1, "uid_validity": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("organize operation was not denied")
	}
}
