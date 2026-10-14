package send

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

type Receipt struct {
	Status    string `json:"status"`
	MessageID string `json:"message_id"`
	Note      string `json:"note,omitempty"`
}

type journalRecord struct {
	Version   int    `json:"version"`
	Digest    string `json:"digest"`
	Status    string `json:"status"`
	MessageID string `json:"message_id"`
}

type journal struct {
	dir  string
	path string
}

func newJournal(dir, alias, key string) (journal, error) {
	if !keyPattern.MatchString(key) {
		return journal{}, errors.New("idempotency_key must contain 8..128 ASCII letters, digits, underscores or hyphens")
	}
	if dir == "" || !filepath.IsAbs(dir) {
		return journal{}, errors.New("absolute journal_dir is required")
	}
	if err := os.Mkdir(dir, 0700); err == nil {
		if err := syncDir(filepath.Dir(dir)); err != nil {
			return journal{}, errors.New("cannot persist send journal directory")
		}
	} else if !errors.Is(err, os.ErrExist) {
		return journal{}, errors.New("cannot create send journal; parent directory must exist")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return journal{}, errors.New("send journal must be a private directory")
	}
	sum := sha256.Sum256([]byte(alias + "\x00" + key))
	return journal{dir: dir, path: filepath.Join(dir, hex.EncodeToString(sum[:])+".json")}, nil
}

func (j journal) begin(digest, messageID string) (Receipt, bool, error) {
	record := journalRecord{Version: 1, Digest: digest, Status: "uncertain", MessageID: messageID}
	f, err := os.OpenFile(j.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		previous, readErr := j.read()
		if readErr != nil {
			return Receipt{Status: "uncertain", Note: "existing journal entry is unreadable; do not retry automatically"}, true, nil
		}
		if previous.Digest != digest {
			return Receipt{}, true, errors.New("idempotency_key already belongs to different message content")
		}
		return Receipt{Status: previous.Status, MessageID: previous.MessageID, Note: "existing send attempt; no new SMTP submission"}, true, nil
	}
	if err != nil {
		return Receipt{}, false, errors.New("cannot create send journal entry")
	}
	encoded, _ := json.Marshal(record)
	_, writeErr := f.Write(encoded)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return Receipt{}, false, errors.New("cannot persist send attempt; inspect journal before retrying")
	}
	if err := syncDir(j.dir); err != nil {
		return Receipt{}, false, errors.New("cannot persist send journal directory; inspect before retrying")
	}
	return Receipt{Status: "uncertain", MessageID: messageID}, false, nil
}

func (j journal) read() (journalRecord, error) {
	f, err := os.Open(j.path)
	if err != nil {
		return journalRecord{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 {
		return journalRecord{}, errors.New("invalid send journal record")
	}
	var r journalRecord
	if err := json.Unmarshal(data, &r); err != nil || r.Version != 1 || (r.Status != "uncertain" && r.Status != "confirmed") {
		return journalRecord{}, errors.New("invalid send journal record")
	}
	return r, nil
}

func (j journal) discardUnsent(digest, messageID string) error {
	record, err := j.read()
	if err != nil || record.Digest != digest || record.MessageID != messageID || record.Status != "uncertain" {
		return errors.New("cannot verify unsent journal entry")
	}
	if err := os.Remove(j.path); err != nil {
		return err
	}
	return syncDir(j.dir)
}

func (j journal) confirm(digest, messageID string) error {
	record := journalRecord{Version: 1, Digest: digest, Status: "confirmed", MessageID: messageID}
	encoded, _ := json.Marshal(record)
	tmp, err := os.CreateTemp(j.dir, ".send-*")
	if err != nil {
		return errors.New("cannot update send journal")
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), j.path); err != nil {
		return fmt.Errorf("cannot confirm send: %w", err)
	}
	return syncDir(j.dir)
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
