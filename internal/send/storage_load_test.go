package send

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aleslanger/yahoo-mail-mcp/internal/storage"
)

// Opt-in local growth measurement; it never connects to a mailbox.
func TestStorageGrowthMeasurement(t *testing.T) {
	if os.Getenv("YAHOO_MCP_STORAGE_LOAD") != "1" {
		t.Skip("set YAHOO_MCP_STORAGE_LOAD=1 to measure local journal and download growth")
	}
	base := t.TempDir()
	journalDir := filepath.Join(base, "journal")
	started := time.Now()
	const records = 500
	for i := 0; i < records; i++ {
		j, err := newJournal(journalDir, "fixture", fmt.Sprintf("growth-%08d", i))
		if err != nil {
			t.Fatal(err)
		}
		messageID := fmt.Sprintf("<growth-%08d@example.com>", i)
		if _, existed, err := j.begin("synthetic-digest", messageID); err != nil || existed {
			t.Fatalf("journal begin %d: existed=%t err=%v", i, existed, err)
		}
		if err := j.confirm("synthetic-digest", messageID); err != nil {
			t.Fatalf("journal confirm %d: %v", i, err)
		}
	}
	journalElapsed := time.Since(started)
	journalUsage, err := storage.Inspect(journalDir)
	if err != nil || journalUsage.Files != records {
		t.Fatalf("journal usage: %+v, %v", journalUsage, err)
	}
	downloadDir := filepath.Join(base, "downloads")
	if err := os.Mkdir(downloadDir, 0700); err != nil {
		t.Fatal(err)
	}
	started = time.Now()
	const downloads = 16
	block := bytes.Repeat([]byte("x"), 1<<20)
	for i := 0; i < downloads; i++ {
		if err := os.WriteFile(filepath.Join(downloadDir, fmt.Sprintf("attachment-%02d", i)), block, 0600); err != nil {
			t.Fatal(err)
		}
	}
	downloadElapsed := time.Since(started)
	downloadUsage, err := storage.Inspect(downloadDir)
	if err != nil || downloadUsage.Files != downloads || downloadUsage.Bytes != downloads<<20 {
		t.Fatalf("download usage: %+v, %v", downloadUsage, err)
	}
	t.Logf("local growth: journal_records=%d journal_bytes=%d bytes_per_record=%.1f journal_elapsed=%s downloads=%d download_bytes=%d download_elapsed=%s", journalUsage.Files, journalUsage.Bytes, float64(journalUsage.Bytes)/records, journalElapsed.Round(time.Millisecond), downloadUsage.Files, downloadUsage.Bytes, downloadElapsed.Round(time.Millisecond))
}
