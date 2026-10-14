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

func TestParseMIMEReaderTruncatesLargeText(t *testing.T) {
	raw := "From: sender@example.com\r\nContent-Type: text/plain\r\n\r\n" + strings.Repeat("x", 70000)
	got, err := parseMIMEReader(strings.NewReader(raw))
	if err != nil || !got.BodyTruncated || len(got.Plain) != 65536 {
		t.Fatalf("large text: length=%d truncated=%v error=%v", len(got.Plain), got.BodyTruncated, err)
	}
}

func TestParseMIMEPartCountBoundary(t *testing.T) {
	for _, count := range []int{100, 101} {
		var raw strings.Builder
		raw.WriteString("MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=parts\r\n\r\n")
		for i := 0; i < count; i++ {
			raw.WriteString("--parts\r\nContent-Type: text/plain\r\n\r\nx\r\n")
		}
		raw.WriteString("--parts--\r\n")
		_, err := ParseMIME([]byte(raw.String()))
		if count == 100 && err != nil {
			t.Fatalf("rejected exactly 100 parts: %v", err)
		}
		if count == 101 && err == nil {
			t.Fatal("accepted 101 parts")
		}
	}
}
