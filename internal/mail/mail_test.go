package mail

import (
	"errors"
	"testing"
)

func TestMessageReferenceValidation(t *testing.T) {
	if !errors.Is(checkValidity(10, 11), ErrStale) {
		t.Fatal("stale UIDVALIDITY accepted")
	}
	if err := checkValidity(10, 0); err == nil {
		t.Fatal("missing UIDVALIDITY accepted")
	}
	if err := checkValidity(10, 10); err != nil {
		t.Fatal(err)
	}
	if err := validateUID(0); err == nil {
		t.Fatal("zero UID accepted")
	}
}
func TestRejectFolderInjection(t *testing.T) {
	for _, name := range []string{"", "INBOX\r\nDELETE Other", "a\x00b"} {
		if err := folderName(name); err == nil {
			t.Fatalf("accepted folder %q", name)
		}
	}
}
