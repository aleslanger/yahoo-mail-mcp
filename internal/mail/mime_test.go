package mail

import (
	"strings"
	"testing"
)

func TestParseMIMEMultipart(t *testing.T) {
	raw := "From: sender@example.com\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=abc\r\n\r\n" +
		"--abc\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHello\r\n" +
		"--abc\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=report.txt\r\nContent-Transfer-Encoding: base64\r\n\r\nQUJD\r\n" +
		"--abc--\r\n"
	got, err := ParseMIME([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Plain, "Hello") {
		t.Fatalf("plain body missing: %+v", got)
	}
	if len(got.Attachments) != 1 || got.Attachments[0].Filename != "report.txt" || got.Attachments[0].Size != 3 {
		t.Fatalf("attachment metadata wrong: %+v", got.Attachments)
	}
}
func TestParseMIMELimit(t *testing.T) {
	if _, err := ParseMIME(make([]byte, 262145)); err == nil {
		t.Fatal("oversized MIME accepted")
	}
}
