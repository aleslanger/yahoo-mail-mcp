package storage

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
)

// Usage reports logical file bytes in one private, flat storage directory.
type Usage struct {
	Configured bool  `json:"configured"`
	Exists     bool  `json:"exists"`
	Files      int64 `json:"files"`
	Bytes      int64 `json:"bytes"`
}

const JournalFileAlert = 100_000
const JournalByteAlert = 128 << 20
const DownloadByteAlert = 512 << 20

// Alerts applies the initial review thresholds for the supervised local target.
func Alerts(journal, downloads Usage) []string {
	alerts := []string{}
	if journal.Files >= JournalFileAlert {
		alerts = append(alerts, "journal_file_count")
	}
	if journal.Bytes >= JournalByteAlert {
		alerts = append(alerts, "journal_logical_bytes")
	}
	if downloads.Bytes >= DownloadByteAlert {
		alerts = append(alerts, "download_logical_bytes")
	}
	return alerts
}

func Inspect(path string) (Usage, error) {
	usage := Usage{Configured: path != ""}
	if path == "" {
		return usage, nil
	}
	if !filepath.IsAbs(path) {
		return Usage{}, errors.New("storage path must be absolute")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return usage, nil
	}
	if err != nil {
		return Usage{}, errors.New("cannot inspect storage directory")
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return Usage{}, errors.New("storage directory must be private and not a symlink")
	}
	dir, err := os.Open(path)
	if err != nil {
		return Usage{}, errors.New("cannot open storage directory")
	}
	defer dir.Close()
	usage.Exists = true
	for {
		entries, err := dir.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return Usage{}, errors.New("cannot read storage directory")
		}
		for _, entry := range entries {
			fileInfo, statErr := os.Lstat(filepath.Join(path, entry.Name()))
			if errors.Is(statErr, os.ErrNotExist) {
				continue // An active server may remove a temporary file during the scan.
			}
			if statErr != nil {
				return Usage{}, errors.New("cannot inspect storage file")
			}
			if !fileInfo.Mode().IsRegular() || fileInfo.Mode().Perm()&0077 != 0 {
				return Usage{}, fmt.Errorf("storage contains a non-private or non-regular entry: %s", entry.Name())
			}
			if fileInfo.Size() < 0 || usage.Bytes > math.MaxInt64-fileInfo.Size() || usage.Files == math.MaxInt64 {
				return Usage{}, errors.New("storage usage exceeds supported range")
			}
			usage.Files++
			usage.Bytes += fileInfo.Size()
		}
		if errors.Is(err, io.EOF) {
			return usage, nil
		}
	}
}
