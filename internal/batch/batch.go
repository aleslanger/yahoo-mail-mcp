package batch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ales/yahoo-mail-mcp/internal/config"
	"github.com/ales/yahoo-mail-mcp/internal/mail"
	"github.com/emersion/go-imap/v2"
)

const maxItems = 20

var tokenPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Input struct {
	Account     string   `json:"account"`
	Folder      string   `json:"folder"`
	UIDValidity uint32   `json:"uid_validity"`
	UIDs        []uint32 `json:"uids"`
	Action      string   `json:"action"`
	Target      string   `json:"target_folder,omitempty"`
}

type Item struct {
	UID     uint32 `json:"uid"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type Preview struct {
	Token       string         `json:"token"`
	ExpiresAt   time.Time      `json:"expires_at"`
	Action      string         `json:"action"`
	Folder      string         `json:"folder"`
	Target      string         `json:"target_folder,omitempty"`
	UIDValidity uint32         `json:"uid_validity"`
	Messages    []mail.Message `json:"messages"`
}

type Outcome struct {
	Status string `json:"status"`
	Items  []Item `json:"items"`
}

type record struct {
	Input     Input     `json:"input"`
	ExpiresAt time.Time `json:"expires_at"`
	Executed  bool      `json:"executed"`
	Items     []Item    `json:"items"`
}

type mailOperations struct {
	status       func(context.Context, config.Account, string) (mail.FolderInfo, error)
	capabilities func(context.Context, config.Account) ([]string, error)
	getRange     func(context.Context, config.Account, string, uint32, uint32, int64, int) (mail.Body, error)
	copyOrMove   func(context.Context, config.Account, string, string, uint32, uint32, bool) (any, error)
	changeFlag   func(context.Context, config.Account, string, uint32, uint32, imap.Flag, bool) error
}

func realMailOperations() mailOperations {
	return mailOperations{
		status: mail.Status, capabilities: mail.Capabilities, getRange: mail.GetRange,
		copyOrMove: func(ctx context.Context, a config.Account, folder, target string, uid, validity uint32, move bool) (any, error) {
			return mail.CopyOrMove(ctx, a, folder, target, uid, validity, move)
		},
		changeFlag: mail.ChangeFlag,
	}
}

func privateDir(dir string) error {
	if dir == "" || !filepath.IsAbs(dir) {
		return errors.New("absolute journal_dir is required for batch plans")
	}
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return errors.New("cannot create batch journal; parent directory must exist")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("batch journal must be a private directory")
	}
	return nil
}

func validate(in Input) error {
	if in.Account == "" || in.Folder == "" || in.UIDValidity == 0 || len(in.UIDs) == 0 || len(in.UIDs) > maxItems {
		return errors.New("batch requires account, folder, UIDVALIDITY and 1..20 UIDs")
	}
	switch in.Action {
	case "move", "copy":
		if in.Target == "" || in.Target == in.Folder {
			return errors.New("batch transfer needs a different target folder")
		}
	case "mark_read", "mark_unread", "flag", "unflag":
		if in.Target != "" {
			return errors.New("flag batch must not include target_folder")
		}
	default:
		return errors.New("unsupported batch action")
	}
	seen := map[uint32]bool{}
	for _, uid := range in.UIDs {
		if uid == 0 || seen[uid] {
			return errors.New("batch UIDs must be positive and unique")
		}
		seen[uid] = true
	}
	return nil
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func save(path string, state record, create bool) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return errors.New("cannot encode batch plan")
	}
	if create {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return errors.New("cannot create batch plan")
		}
		_, writeErr := f.Write(encoded)
		if writeErr == nil {
			writeErr = f.Sync()
		}
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil || syncDir(filepath.Dir(path)) != nil {
			return errors.New("cannot persist batch plan")
		}
		return nil
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".batch-*")
	if err != nil {
		return errors.New("cannot update batch journal")
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(encoded); err != nil {
		_ = f.Close()
		return errors.New("cannot update batch journal")
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return errors.New("cannot sync batch journal")
	}
	if err := f.Close(); err != nil {
		return errors.New("cannot close batch journal")
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return errors.New("cannot replace batch journal")
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return errors.New("cannot sync batch journal directory")
	}
	return nil
}

func Prepare(ctx context.Context, a config.Account, dir string, in Input) (Preview, error) {
	return prepare(ctx, a, dir, in, realMailOperations())
}

func prepare(ctx context.Context, a config.Account, dir string, in Input, ops mailOperations) (Preview, error) {
	if !a.AllowOrganize {
		return Preview{}, errors.New("organize permission is disabled for account")
	}
	if in.Account != a.Alias {
		return Preview{}, errors.New("batch account mismatch")
	}
	if err := validate(in); err != nil {
		return Preview{}, err
	}
	if err := privateDir(dir); err != nil {
		return Preview{}, err
	}
	if in.Action == "move" || in.Action == "copy" {
		if _, err := ops.status(ctx, a, in.Target); err != nil {
			return Preview{}, errors.New("target folder is unavailable")
		}
		if in.Action == "move" {
			caps, err := ops.capabilities(ctx, a)
			if err != nil {
				return Preview{}, err
			}
			supported := false
			for _, cap := range caps {
				if strings.EqualFold(cap, "MOVE") {
					supported = true
				}
			}
			if !supported {
				return Preview{}, errors.New("server does not advertise MOVE")
			}
		}
	}
	messages := make([]mail.Message, 0, len(in.UIDs))
	for _, uid := range in.UIDs {
		body, err := ops.getRange(ctx, a, in.Folder, uid, in.UIDValidity, 0, 1)
		if err != nil {
			return Preview{}, err
		}
		messages = append(messages, body.Message)
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Preview{}, errors.New("cannot generate batch token")
	}
	token := hex.EncodeToString(random[:])
	expires := time.Now().Add(10 * time.Minute)
	state := record{Input: in, ExpiresAt: expires, Items: make([]Item, 0, len(in.UIDs))}
	for _, uid := range in.UIDs {
		state.Items = append(state.Items, Item{UID: uid, Status: "pending"})
	}
	if err := save(filepath.Join(dir, "batch-"+token+".json"), state, true); err != nil {
		return Preview{}, err
	}
	return Preview{Token: token, ExpiresAt: expires, Action: in.Action, Folder: in.Folder, Target: in.Target, UIDValidity: in.UIDValidity, Messages: messages}, nil
}

func Execute(ctx context.Context, a config.Account, dir, token string, confirmed bool) (Outcome, error) {
	return execute(ctx, a, dir, token, confirmed, realMailOperations())
}

func execute(ctx context.Context, a config.Account, dir, token string, confirmed bool, ops mailOperations) (Outcome, error) {
	if !a.AllowOrganize {
		return Outcome{}, errors.New("organize permission is disabled for account")
	}
	if !confirmed {
		return Outcome{}, errors.New("batch execution requires confirm=true after inspecting the preview")
	}
	if !tokenPattern.MatchString(token) {
		return Outcome{}, errors.New("invalid batch token")
	}
	if err := privateDir(dir); err != nil {
		return Outcome{}, err
	}
	path := filepath.Join(dir, "batch-"+token+".json")
	lock, err := os.OpenFile(path+".lock", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return Outcome{}, errors.New("batch plan is locked or unavailable; inspect journal before retrying")
	}
	defer os.Remove(path + ".lock")
	defer lock.Close()
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 1<<20 {
		return Outcome{}, errors.New("batch plan missing or oversized")
	}
	var state record
	if err := json.Unmarshal(data, &state); err != nil || state.Input.Account != a.Alias || validate(state.Input) != nil || len(state.Items) != len(state.Input.UIDs) {
		return Outcome{}, errors.New("batch plan is invalid or belongs to another account")
	}
	for i, item := range state.Items {
		if item.UID != state.Input.UIDs[i] {
			return Outcome{}, errors.New("batch journal items do not match previewed UIDs")
		}
	}
	if state.Executed {
		return summarize(state.Items), nil
	}
	if time.Now().After(state.ExpiresAt) {
		return Outcome{}, errors.New("batch preview expired; prepare a new plan")
	}
	state.Executed = true
	if err := save(path, state, false); err != nil {
		return Outcome{}, err
	}
	for i := range state.Items {
		if err := ctx.Err(); err != nil {
			break
		}
		state.Items[i].Status = "uncertain"
		state.Items[i].Message = "operation started; inspect mailbox before retrying if interrupted"
		if err := save(path, state, false); err != nil {
			return Outcome{}, err
		}
		uid := state.Items[i].UID
		var operationErr error
		switch state.Input.Action {
		case "move", "copy":
			_, operationErr = ops.copyOrMove(ctx, a, state.Input.Folder, state.Input.Target, uid, state.Input.UIDValidity, state.Input.Action == "move")
		case "mark_read", "mark_unread":
			operationErr = ops.changeFlag(ctx, a, state.Input.Folder, uid, state.Input.UIDValidity, imap.FlagSeen, state.Input.Action == "mark_read")
		case "flag", "unflag":
			operationErr = ops.changeFlag(ctx, a, state.Input.Folder, uid, state.Input.UIDValidity, imap.FlagFlagged, state.Input.Action == "flag")
		}
		if operationErr == nil {
			state.Items[i].Status = "completed"
			state.Items[i].Message = "postcondition verified"
		} else {
			state.Items[i].Message = operationErr.Error()
		}
		if err := save(path, state, false); err != nil {
			return Outcome{}, err
		}
	}
	return summarize(state.Items), nil
}

func summarize(items []Item) Outcome {
	status := "completed"
	for _, item := range items {
		if item.Status != "completed" {
			status = "partial_or_uncertain"
			break
		}
	}
	return Outcome{Status: status, Items: items}
}
