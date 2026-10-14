package mail

import (
	"errors"
	"fmt"
	"testing"
)

func TestLoginRejectionIsTemporaryButMissingFolderIsNot(t *testing.T) {
	ctx, a := localIMAP(t, true)
	if _, _, err := SearchOldest(ctx, a, "Missing", SearchFilter{From: "x"}, 1); err == nil || IsTemporary(err) {
		t.Fatalf("missing folder classified as temporary: %v", err)
	}
	t.Setenv("YAHOO_MCP_PASSWORD_FIXTURE", "wrong-secret")
	_, err := Capabilities(ctx, a)
	if err == nil || err.Error() != "IMAP authentication failed" || !IsTemporary(err) {
		t.Fatalf("login rejection: %v temporary=%t", err, IsTemporary(err))
	}
}

func TestIsTemporarySeesWrappedErrors(t *testing.T) {
	if !IsTemporary(fmt.Errorf("step: %w", NewTemporaryError("IMAP TLS connection failed"))) {
		t.Fatal("wrapped temporary error not recognized")
	}
	if IsTemporary(errors.New("folder selection failed")) || IsTemporary(nil) {
		t.Fatal("plain error classified as temporary")
	}
}
