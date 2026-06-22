package gomailer_test

import (
	"bytes"
	"context"
	"net/mail"
	"testing"

	gomailer "github.com/shyim/go-mailer"
	"github.com/shyim/go-mailer/mailertest"
)

// TestMailerInMemorySend builds a Message, sends it through the Mailer backed by
// the in-memory recording transport, and asserts the recorded count plus that
// the captured MIME bytes parse with net/mail.ReadMessage.
func TestMailerInMemorySend(t *testing.T) {
	from := gomailer.MustAddress("alice@example.com", "Alice")
	to := gomailer.MustAddress("bob@example.com", "Bob")

	msg := gomailer.NewMessage().
		SetFrom(from).
		SetTo(to).
		SetSubject("Hello").
		SetText([]byte("Hello, Bob!")).
		SetHTML([]byte("<p>Hello, Bob!</p>"))

	rec := mailertest.NewRecordingTransport("")
	mailer := gomailer.NewMailer(rec)

	if err := mailer.Send(context.Background(), msg, nil); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	if got := rec.Count(); got != 1 {
		t.Fatalf("recorded message count = %d, want 1", got)
	}

	sent, ok := rec.Last()
	if !ok {
		t.Fatal("expected a recorded message, got none")
	}

	parsed, err := mail.ReadMessage(bytes.NewReader(sent.Bytes()))
	if err != nil {
		t.Fatalf("net/mail.ReadMessage failed to parse sent bytes: %v", err)
	}
	if h := parsed.Header.Get("Subject"); h != "Hello" {
		t.Fatalf("parsed Subject = %q, want %q", h, "Hello")
	}
	if h := parsed.Header.Get("From"); h == "" {
		t.Fatal("parsed From header is empty")
	}
}

// Mailer.Close fans out to a closable transport. (#9)
func TestMailerCloseDelegates(t *testing.T) {
	c := &closeCountingTransport{}
	m := gomailer.NewMailer(c)
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if c.closed != 1 {
		t.Errorf("Mailer.Close did not reach the transport: closed=%d", c.closed)
	}
}

type closeCountingTransport struct{ closed int }

func (c *closeCountingTransport) Send(context.Context, gomailer.RawMessage, *gomailer.Envelope) (*gomailer.SentMessage, error) {
	return &gomailer.SentMessage{}, nil
}
func (c *closeCountingTransport) String() string { return "counting://" }
func (c *closeCountingTransport) Close() error   { c.closed++; return nil }
