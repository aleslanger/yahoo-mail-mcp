package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/aleslanger/yahoo-mail-mcp/internal/config"
	"github.com/aleslanger/yahoo-mail-mcp/internal/index"
	"github.com/aleslanger/yahoo-mail-mcp/internal/mail"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connectInMemory(t *testing.T, cfg config.Config) *mcp.ClientSession {
	t.Helper()
	return connectWithContext(t, context.Background(), cfg)
}

// connectWithContext serves cfg in memory; tool handlers inherit ctx, which
// lets tests inject an IMAP dialer.
func connectWithContext(t *testing.T, ctx context.Context, cfg config.Config) *mcp.ClientSession {
	t.Helper()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := New(cfg).Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestIndexToolsRequireIndexDir(t *testing.T) {
	session := connectInMemory(t, config.Config{Accounts: []config.Account{{Alias: "personal", Address: "a@example.com"}}})
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "index_search", Arguments: map[string]any{"account": "personal"}})
	if err != nil || !res.IsError {
		t.Fatalf("index_search without index_dir: %+v, %v", res, err)
	}
}

func TestIndexSearchAndStatusReadStoredSnapshots(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "index")
	store, err := index.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	snap := index.Snapshot{Folder: "Archive", UIDValidity: 9, NextUID: 3, Complete: true, Messages: []mail.Header{
		{UID: 1, From: "alice@example.com", Subject: "Invoice", Date: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)},
		{UID: 2, From: "bob@example.com", Subject: "Lunch", Date: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)},
	}}
	if err := store.Save("personal", snap); err != nil {
		t.Fatal(err)
	}
	session := connectInMemory(t, config.Config{IndexDir: dir, Accounts: []config.Account{{Alias: "personal", Address: "a@example.com"}}})
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "index_search", Arguments: map[string]any{"account": "personal", "from": "alice"}})
	if err != nil || res.IsError {
		t.Fatalf("index_search: %+v, %v", res, err)
	}
	var found index.Result
	if err := json.Unmarshal(mustJSON(t, res.StructuredContent), &found); err != nil || found.Total != 1 || found.Results[0].Folder != "Archive" || found.Results[0].UIDValidity != 9 {
		t.Fatalf("search result: %+v, %v", found, err)
	}
	res, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "index_status", Arguments: map[string]any{"account": "personal"}})
	if err != nil || res.IsError {
		t.Fatalf("index_status: %+v, %v", res, err)
	}
	var status IndexStatus
	if err := json.Unmarshal(mustJSON(t, res.StructuredContent), &status); err != nil || len(status.Folders) != 1 || status.Folders[0].Messages != 2 {
		t.Fatalf("status: %+v, %v", status, err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
