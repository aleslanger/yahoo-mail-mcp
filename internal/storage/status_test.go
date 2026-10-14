package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInspect(t *testing.T) {
	missing, err := Inspect("")
	if err != nil || missing.Configured || missing.Exists {
		t.Fatalf("unconfigured usage: %+v, %v", missing, err)
	}
	dir := filepath.Join(t.TempDir(), "journal")
	missing, err = Inspect(dir)
	if err != nil || !missing.Configured || missing.Exists {
		t.Fatalf("missing usage: %+v, %v", missing, err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"one.json": "123", "two.json": "45678"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	usage, err := Inspect(dir)
	if err != nil || !usage.Exists || usage.Files != 2 || usage.Bytes != 8 {
		t.Fatalf("usage: %+v, %v", usage, err)
	}
}

func TestInspectRejectsUnsafeStorage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing-target", filepath.Join(dir, "record.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(dir); err == nil {
		t.Fatal("expected symlink rejection")
	}
	if err := os.Remove(filepath.Join(dir, "record.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "record.json"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(dir); err == nil {
		t.Fatal("expected public-file rejection")
	}
}

func TestAlertsAtThresholds(t *testing.T) {
	if got := Alerts(Usage{Files: JournalFileAlert - 1, Bytes: JournalByteAlert - 1}, Usage{Bytes: DownloadByteAlert - 1}); len(got) != 0 {
		t.Fatalf("early alert: %v", got)
	}
	got := Alerts(Usage{Files: JournalFileAlert, Bytes: JournalByteAlert}, Usage{Bytes: DownloadByteAlert})
	if len(got) != 3 || got[0] != "journal_file_count" || got[1] != "journal_logical_bytes" || got[2] != "download_logical_bytes" {
		t.Fatalf("missing threshold alerts: %v", got)
	}
}
