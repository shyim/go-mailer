package gomailer_test

import (
	"bytes"
	"context"
	"fmt"
	"net/mail"

	gomailer "github.com/shyim/go-mailer"
	"github.com/shyim/go-mailer/mailertest"
)

// Example builds a Message, sends it through a Mailer backed by the in-memory
// recording transport, and inspects the captured wire bytes. It demonstrates
// the everyday API: construct a Message, wrap a Transport in a Mailer, Send with
// a context and a nil (auto-derived) envelope, then assert on what was sent.
func Example() {
	// Build the message. Setters chain and a nil envelope is derived from the
	// From/To/Cc/Bcc addresses at send time.
	msg := gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("alice@example.com", "Alice")).
		SetTo(gomailer.MustAddress("bob@example.com", "Bob")).
		SetSubject("Hello").
		SetText([]byte("Hello, Bob!")).
		SetHTML([]byte("<p>Hello, Bob!</p>"))

	// In production this would be an SMTP/sendmail/null transport resolved from a
	// DSN; in tests the recording transport captures messages for inspection.
	rec := mailertest.NewRecordingTransport("")
	mailer := gomailer.NewMailer(rec)

	if err := mailer.Send(context.Background(), msg, nil); err != nil {
		fmt.Println("send error:", err)
		return
	}

	sent, _ := rec.Last()
	parsed, _ := mail.ReadMessage(bytes.NewReader(sent.Bytes()))

	fmt.Println("count:", rec.Count())
	fmt.Println("subject:", parsed.Header.Get("Subject"))
	fmt.Println("multipart:", bytes.Contains(sent.Bytes(), []byte("multipart/alternative")))

	// Output:
	// count: 1
	// subject: Hello
	// multipart: true
}
