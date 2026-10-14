package mail

import (
	"slices"
	"testing"

	"github.com/emersion/go-imap/v2"
)

func TestFetchFolderDeltaPagesTracksFlagsAndExpunges(t *testing.T) {
	ctx, a := localIMAP(t, false)
	u1, u2, u3 := appendFixture(t, ctx, a), appendFixture(t, ctx, a), appendFixture(t, ctx, a)
	first, err := FetchFolderDelta(ctx, a, "INBOX", 0, 0, 2)
	if err != nil || first.Complete || first.Reset || len(first.New) != 2 || first.New[0].UID != u1 || first.New[1].UID != u2 || first.NextUID != u2+1 {
		t.Fatalf("first page: %+v, %v", first, err)
	}
	if first.New[0].From != "sender@example.com" || first.New[0].Subject != "Fixture subject" || len(first.New[0].To) != 1 || first.New[0].Date.IsZero() {
		t.Fatalf("header fields: %+v", first.New[0])
	}
	second, err := FetchFolderDelta(ctx, a, "INBOX", first.UIDValidity, first.NextUID, 2)
	if err != nil || !second.Complete || len(second.New) != 1 || second.New[0].UID != u3 || len(second.Flags) != 3 {
		t.Fatalf("second page: %+v, %v", second, err)
	}
	if slices.Contains(second.Flags[u3], imap.FlagSeen) {
		t.Fatal("synchronization marked a message as read")
	}
	if err := ChangeFlag(ctx, a, "INBOX", u1, first.UIDValidity, imap.FlagSeen, true); err != nil {
		t.Fatal(err)
	}
	c, closeFn, err := connect(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if err := expungeUID(c, "INBOX", u2, first.UIDValidity, "message", "INBOX"); err != nil {
		t.Fatal(err)
	}
	closeFn()
	third, err := FetchFolderDelta(ctx, a, "INBOX", second.UIDValidity, second.NextUID, 2)
	if err != nil || !third.Complete || len(third.New) != 0 || len(third.Flags) != 2 {
		t.Fatalf("third pass: %+v, %v", third, err)
	}
	if _, present := third.Flags[u2]; present || !slices.Contains(third.Flags[u1], imap.FlagSeen) {
		t.Fatalf("presence and flags not refreshed: %+v", third.Flags)
	}
	reset, err := FetchFolderDelta(ctx, a, "INBOX", first.UIDValidity+1, third.NextUID, 10)
	if err != nil || !reset.Reset || len(reset.New) != 2 {
		t.Fatalf("UIDVALIDITY change: %+v, %v", reset, err)
	}
}

func TestFetchFolderDeltaHandlesEmptyFolderAndBounds(t *testing.T) {
	ctx, a := localIMAP(t, false)
	empty, err := FetchFolderDelta(ctx, a, "Archive", 0, 0, 10)
	if err != nil || !empty.Complete || len(empty.New) != 0 || empty.Flags == nil {
		t.Fatalf("empty folder: %+v, %v", empty, err)
	}
	if _, err := FetchFolderDelta(ctx, a, "Archive", 0, 0, 0); err == nil {
		t.Fatal("zero page size accepted")
	}
	if _, err := FetchFolderDelta(ctx, a, "Archive", 0, 0, maxDeltaHeaders+1); err == nil {
		t.Fatal("oversized page accepted")
	}
}

func TestSearchOldestCoversWholeFolderAndFetchHeaders(t *testing.T) {
	ctx, a := localIMAP(t, false)
	u1, u2, u3 := appendFixture(t, ctx, a), appendFixture(t, ctx, a), appendFixture(t, ctx, a)
	validity, uids, err := SearchOldest(ctx, a, "INBOX", SearchFilter{From: "sender@example.com"}, 2)
	if err != nil || validity == 0 || len(uids) != 2 || uids[0] != u1 || uids[1] != u2 {
		t.Fatalf("oldest matches: validity=%d uids=%v err=%v", validity, uids, err)
	}
	if err := ChangeFlag(ctx, a, "INBOX", u1, validity, imap.FlagSeen, true); err != nil {
		t.Fatal(err)
	}
	_, unread, err := SearchOldest(ctx, a, "INBOX", SearchFilter{From: "sender@example.com", Unread: true}, 20)
	if err != nil || len(unread) != 2 || unread[0] != u2 || unread[1] != u3 {
		t.Fatalf("unread matches: %v, %v", unread, err)
	}
	headers, err := FetchHeaders(ctx, a, "INBOX", validity, []uint32{u3, u2})
	if err != nil || len(headers) != 2 || headers[0].UID != u2 || headers[1].Subject != "Fixture subject" {
		t.Fatalf("headers: %+v, %v", headers, err)
	}
	if _, err := FetchHeaders(ctx, a, "INBOX", validity+1, []uint32{u2}); err == nil {
		t.Fatal("stale UIDVALIDITY accepted")
	}
	if _, _, err := SearchOldest(ctx, a, "INBOX", SearchFilter{}, maxDeltaHeaders+1); err == nil {
		t.Fatal("oversized limit accepted")
	}
}
