package gomailer

import (
	"bufio"
	"bytes"
	"mime"
	"net/mail"
	"regexp"
	"strings"
	"testing"
)

func TestAttachmentFilenameUsesMediaTypeParameterEncoding(t *testing.T) {
	const filename = "résumé 2026.pdf"
	msg := testMessage(t).Attach(Attachment{Filename: filename, Data: []byte("pdf")})
	raw, err := msg.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("filename=\"=?utf-8?")) || bytes.Contains(raw, []byte("name=\"=?utf-8?")) {
		t.Fatalf("attachment filename used RFC 2047 encoded-word parameter:\n%s", raw)
	}

	var sawName, sawFilename bool
	s := bufio.NewScanner(bytes.NewReader(raw))
	for s.Scan() {
		line := strings.TrimRight(s.Text(), "\r")
		if strings.HasPrefix(line, "Content-Type:") && strings.Contains(line, "name") {
			_, params, err := mime.ParseMediaType(strings.TrimSpace(strings.TrimPrefix(line, "Content-Type:")))
			if err != nil {
				t.Fatalf("parse Content-Type %q: %v", line, err)
			}
			if params["name"] == filename {
				sawName = true
			}
		}
		if strings.HasPrefix(line, "Content-Disposition:") && strings.Contains(line, "filename") {
			_, params, err := mime.ParseMediaType(strings.TrimSpace(strings.TrimPrefix(line, "Content-Disposition:")))
			if err != nil {
				t.Fatalf("parse Content-Disposition %q: %v", line, err)
			}
			if params["filename"] == filename {
				sawFilename = true
			}
		}
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	if !sawName || !sawFilename {
		t.Fatalf("did not recover encoded filename parameters: name=%v filename=%v\n%s", sawName, sawFilename, raw)
	}
}

func TestInlineAttachmentGeneratedContentIDIsStable(t *testing.T) {
	msg := testMessage(t).
		SetHTML([]byte(`<img src="cid:auto">`)).
		Attach(Attachment{Filename: "logo.png", Data: []byte("png"), Inline: true})
	a, err := msg.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	b, err := msg.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`Content-ID: <([^>]+)>`)
	ma := re.FindSubmatch(a)
	mb := re.FindSubmatch(b)
	if ma == nil || mb == nil {
		t.Fatalf("Content-ID header missing\nfirst:\n%s\nsecond:\n%s", a, b)
	}
	if string(ma[1]) != string(mb[1]) {
		t.Fatalf("generated Content-ID changed between serializations: %q vs %q", ma[1], mb[1])
	}
}

func TestSerializedHeaderLinesStayBelowHardLimit(t *testing.T) {
	long := strings.Repeat("a", 1200)
	msg := testMessage(t).
		SetSubject(long).
		Attach(Attachment{Filename: long + ".txt", Data: []byte("x")})
	raw, err := msg.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range bytes.Split(raw, []byte("\r\n")) {
		if len(line) > 998 {
			t.Fatalf("line %d is %d bytes, exceeds RFC 5322 hard limit", i+1, len(line))
		}
	}
}

func TestLongUnbrokenSubjectRoundTripsWithoutInsertedSpaces(t *testing.T) {
	subject := strings.Repeat("a", 1200)
	raw, err := testMessage(t).SetSubject(subject).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil {
		t.Fatal(err)
	}
	if decoded != subject {
		t.Fatalf("decoded subject length/value changed: len=%d want=%d", len(decoded), len(subject))
	}
}

func TestInvalidInlineContentIDRejected(t *testing.T) {
	msg := testMessage(t).Attach(Attachment{Inline: true, ContentID: "foo> <bar@example", Data: []byte("x")})
	if _, err := msg.Bytes(); err == nil {
		t.Fatal("expected invalid Content-ID error")
	}
	long := testMessage(t).Attach(Attachment{Inline: true, ContentID: strings.Repeat("a", 1200) + "@example.com", Data: []byte("x")})
	if _, err := long.Bytes(); err == nil {
		t.Fatal("expected overlong Content-ID error")
	}
}

// Repeated serializations of an un-sent message must be byte-identical: the
// Message-ID and Date are materialized once, not regenerated per Bytes(). (#14)
func TestMessageBytesDeterministic(t *testing.T) {
	m := NewMessage().
		SetFrom(MustAddress("s@example.com", "")).
		SetTo(MustAddress("r@example.com", "")).
		SetText([]byte("body"))
	b1, err := m.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	b2, err := m.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) != string(b2) {
		t.Error("two Bytes() calls on the same message differ; Message-ID/Date not materialized once")
	}
	// MessageID() must match the value embedded in the serialized bytes.
	if id := m.MessageID(); !strings.Contains(string(b1), "<"+id+">") {
		t.Errorf("MessageID() %q does not match the serialized Message-ID", id)
	}
}

// From() must return a copy so a caller cannot corrupt the message. (#15)
func TestMessageFromReturnsCopy(t *testing.T) {
	orig := MustAddress("a@example.com", "A")
	m := NewMessage().SetFrom(orig).SetTo(MustAddress("r@example.com", ""))
	got := m.From()
	got[0] = MustAddress("evil@attacker.test", "")
	if m.From()[0].Email() != "a@example.com" {
		t.Error("mutating the slice returned by From() corrupted the message")
	}
}

// MessageID() must be race-free when called concurrently on a shared message. (#13)
func TestMessageIDConcurrent(t *testing.T) {
	m := NewMessage().
		SetFrom(MustAddress("s@example.com", "")).
		SetTo(MustAddress("r@example.com", "")).
		SetText([]byte("body"))
	ids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		go func() { ids <- m.MessageID() }()
	}
	first := <-ids
	for i := 0; i < 7; i++ {
		if id := <-ids; id != first {
			t.Fatalf("MessageID() returned different values concurrently: %q vs %q", first, id)
		}
	}
}
