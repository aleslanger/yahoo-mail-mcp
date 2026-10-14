package server

import (
	"context"
	"errors"

	"github.com/ales/yahoo-mail-mcp/internal/config"
	"github.com/ales/yahoo-mail-mcp/internal/index"
	"github.com/ales/yahoo-mail-mcp/internal/mail"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const defaultIndexSyncHeaders = 500

type IndexSyncInput struct {
	Account string `json:"account" jsonschema:"configured account alias"`
	Folder  string `json:"folder"`
	MaxNew  int    `json:"max_new,omitempty" jsonschema:"new headers to fetch in this step, 1..2000, default 500"`
}

type IndexSearchInput struct {
	Account string `json:"account" jsonschema:"configured account alias"`
	index.Query
}

type IndexStatusInput struct {
	Account string `json:"account" jsonschema:"configured account alias"`
}

type IndexStatus struct {
	Folders []index.FolderStatus `json:"folders"`
}

func registerIndexTools(s *mcp.Server, cfg config.Config, account func(string) (config.Account, error)) {
	store := func() (index.Store, error) {
		if cfg.IndexDir == "" {
			return index.Store{}, errors.New("index_dir is not configured")
		}
		return index.Open(cfg.IndexDir)
	}
	mcp.AddTool(s, &mcp.Tool{Name: "index_sync", Description: "Read-only: add up to max_new new message headers of one folder to the private local index. Repeat until complete is true; a complete pass also drops expunged messages and refreshes flags."}, func(ctx context.Context, _ *mcp.CallToolRequest, in IndexSyncInput) (*mcp.CallToolResult, index.FolderStatus, error) {
		a, err := account(in.Account)
		if err != nil {
			return nil, index.FolderStatus{}, err
		}
		st, err := store()
		if err != nil {
			return nil, index.FolderStatus{}, err
		}
		if in.MaxNew == 0 {
			in.MaxNew = defaultIndexSyncHeaders
		}
		out, err := st.SyncStep(ctx, a, in.Folder, in.MaxNew, mail.FetchFolderDelta)
		return nil, out, err
	})
	mcp.AddTool(s, &mcp.Tool{Name: "index_search", Description: "Search and sort indexed headers across all indexed folders of an account without contacting the server. Filters combine with AND; sort by date, from, subject or size. Results may be stale; check index_status."}, func(_ context.Context, _ *mcp.CallToolRequest, in IndexSearchInput) (*mcp.CallToolResult, index.Result, error) {
		if _, err := account(in.Account); err != nil {
			return nil, index.Result{}, err
		}
		st, err := store()
		if err != nil {
			return nil, index.Result{}, err
		}
		out, err := st.Search(in.Account, in.Query)
		return nil, out, err
	})
	mcp.AddTool(s, &mcp.Tool{Name: "index_status", Description: "List indexed folders with message counts, completeness and last sync time."}, func(_ context.Context, _ *mcp.CallToolRequest, in IndexStatusInput) (*mcp.CallToolResult, IndexStatus, error) {
		if _, err := account(in.Account); err != nil {
			return nil, IndexStatus{}, err
		}
		st, err := store()
		if err != nil {
			return nil, IndexStatus{}, err
		}
		folders, err := st.Status(in.Account)
		return nil, IndexStatus{Folders: folders}, err
	})
}
