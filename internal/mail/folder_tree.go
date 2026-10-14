package mail

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/ales/yahoo-mail-mcp/internal/config"
	"github.com/emersion/go-imap/v2"
)

type FolderNode struct {
	Name       string             `json:"name"`
	Path       string             `json:"path"`
	Attributes []imap.MailboxAttr `json:"attributes,omitempty"`
	Children   []*FolderNode      `json:"children,omitempty"`
}

type FolderTreeEntry struct {
	Name       string             `json:"name"`
	Path       string             `json:"path"`
	Parent     string             `json:"parent,omitempty"`
	Depth      int                `json:"depth"`
	Attributes []imap.MailboxAttr `json:"attributes,omitempty"`
	Messages   *uint32            `json:"messages,omitempty"`
	Unseen     *uint32            `json:"unseen,omitempty"`
}

type FolderTreePage struct {
	Folders               []FolderTreeEntry `json:"folders"`
	CountedFolders        int               `json:"counted_folders"`
	TotalCountableFolders int               `json:"total_countable_folders"`
	NextCountOffset       int               `json:"next_count_offset,omitempty"`
	HasMoreCounts         bool              `json:"has_more_counts"`
}

// ListFolderTree loads a folder hierarchy and optionally one bounded page of counts.
// The offset counts selectable folders in hierarchy order; each call gets a fresh LIST.
func ListFolderTree(ctx context.Context, a config.Account, includeCounts bool, offset, limit int) (FolderTreePage, error) {
	if offset < 0 || offset > 1000 {
		return FolderTreePage{}, errors.New("count_offset must be 0..1000")
	}
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 20 {
		return FolderTreePage{}, errors.New("count_limit must be 1..20")
	}
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		return FolderTreePage{}, err
	}
	defer closeFn()
	folders, err := listFolders(c)
	if err != nil {
		return FolderTreePage{}, err
	}
	out := FolderTreePage{Folders: FlattenFolderTree(BuildFolderTree(folders))}
	if !includeCounts {
		return out, nil
	}
	selectable := make(map[string]bool, len(folders))
	for _, folder := range folders {
		if folder.Name == "" {
			continue
		}
		selectable[folder.Name] = true
		for _, attr := range folder.Attributes {
			if attr == imap.MailboxAttrNoSelect || attr == imap.MailboxAttrNonExistent {
				selectable[folder.Name] = false
			}
		}
	}
	for _, entry := range out.Folders {
		if selectable[entry.Path] {
			out.TotalCountableFolders++
		}
	}
	seen := 0
	for i := range out.Folders {
		entry := &out.Folders[i]
		if !selectable[entry.Path] {
			continue
		}
		if seen < offset {
			seen++
			continue
		}
		if out.CountedFolders == limit {
			break
		}
		data, err := c.Status(entry.Path, &imap.StatusOptions{NumMessages: true, NumUnseen: true}).Wait()
		if err != nil || data.NumMessages == nil || data.NumUnseen == nil {
			return FolderTreePage{}, errors.New("folder count lookup failed for " + entry.Path)
		}
		entry.Messages, entry.Unseen = data.NumMessages, data.NumUnseen
		out.CountedFolders++
		seen++
	}
	if offset+out.CountedFolders < out.TotalCountableFolders {
		out.HasMoreCounts = true
		out.NextCountOffset = offset + out.CountedFolders
	}
	return out, nil
}

func FlattenFolderTree(roots []*FolderNode) []FolderTreeEntry {
	out := []FolderTreeEntry{}
	var walk func([]*FolderNode, string, int)
	walk = func(nodes []*FolderNode, parent string, depth int) {
		for _, node := range nodes {
			out = append(out, FolderTreeEntry{Name: node.Name, Path: node.Path, Parent: parent, Depth: depth, Attributes: node.Attributes})
			walk(node.Children, node.Path, depth+1)
		}
	}
	walk(roots, "", 0)
	return out
}

// BuildFolderTree groups one LIST response using each folder's reported delimiter.
// It does not issue STATUS calls or assume special folder names.
func BuildFolderTree(folders []Folder) []*FolderNode {
	roots := []*FolderNode{}
	nodes := map[string]*FolderNode{}
	for _, folder := range folders {
		if folder.Name == "" {
			continue
		}
		parts := []string{folder.Name}
		if folder.Delimiter != "" {
			parts = strings.Split(folder.Name, folder.Delimiter)
		}
		for i := range parts {
			path := strings.Join(parts[:i+1], folder.Delimiter)
			if _, exists := nodes[path]; exists {
				continue
			}
			node := &FolderNode{Name: parts[i], Path: path}
			nodes[path] = node
			if i == 0 {
				roots = append(roots, node)
			} else {
				parent := strings.Join(parts[:i], folder.Delimiter)
				nodes[parent].Children = append(nodes[parent].Children, node)
			}
		}
		nodes[folder.Name].Attributes = folder.Attributes
	}
	var sortNodes func([]*FolderNode)
	sortNodes = func(items []*FolderNode) {
		sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].Path) < strings.ToLower(items[j].Path) })
		for _, item := range items {
			sortNodes(item.Children)
		}
	}
	sortNodes(roots)
	return roots
}
