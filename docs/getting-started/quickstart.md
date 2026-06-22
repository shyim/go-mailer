# Quickstart

This page gets you from an empty `main.go` to a sent email. We cover three paths:
build and send over SMTP explicitly, configure a transport from a DSN string, and
run everything in-memory for a local test.

!!! note "Modules"
    The core library (`github.com/shyim/go-mailer`) is stdlib-first. Concrete
    network transports live in their own modules, so `go get` the ones you use:

    ```sh
    go get github.com/shyim/go-mailer
    go get github.com/shyim/go-mailer/transport/smtp
    ```

## Send over SMTP

Build a [Message](../guides/sending-mail.md), construct an `smtp.Transport` with
your credentials, wrap it in a `Mailer`, and `Send` with a context.

```go
package main

import (
	"context"
	"log"

	"github.com/shyim/go-mailer"
	"github.com/shyim/go-mailer/transport/smtp"
)

func main() {
	// host, port, tlsOnConnect — false uses STARTTLS upgrade on a plaintext port.
	t := smtp.NewTransport("smtp.example.com", 587, false).
		SetUsername("user").
		SetPassword("secret")

	msg := gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("alice@example.com", "Alice")).
		SetTo(gomailer.MustAddress("bob@example.com", "Bob")).
		SetSubject("Hello from gomailer").
		SetText([]byte("Hello, Bob! (plain text)")).
		SetHTML([]byte("<p>Hello, <b>Bob</b>! (HTML)</p>"))

	mailer := gomailer.NewMailer(t)
	defer mailer.Close()

	if err := mailer.Send(context.Background(), msg, nil); err != nil {
		log.Fatalf("send failed: %v", err)
	}
}
```

!!! warning "TLS is required for AUTH"
    The SMTP transport refuses to send credentials over an unprotected
    connection. Use `smtps://` (set `tlsOnConnect` to `true`), let opportunistic
    STARTTLS run, or — only for a trusted local relay — opt in with
    `SetAllowPlaintextAuth(true)`. See [Transports](../concepts/transports.md).

The third argument to `Send` is the [Envelope](../concepts/architecture.md). Passing
`nil` derives it from the message's `From`/`To`/`Cc`/`Bcc`.

## Configure from a DSN

Instead of wiring a transport by hand, resolve one from a DSN string. The
concrete transport packages self-register their schemes in `init()`, so they must
be **blank-imported** to be resolvable.

```go
package main

import (
	"context"
	"log"

	"github.com/shyim/go-mailer"
	"github.com/shyim/go-mailer/transport"
	_ "github.com/shyim/go-mailer/transport/smtp" // registers smtp:// and smtps://
)

func main() {
	t, err := transport.FromDSN(
		"smtp://user:pass@smtp.example.com:587?require_tls=true",
		transport.Deps{},
	)
	if err != nil {
		log.Fatal(err)
	}

	msg := gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("alice@example.com", "Alice")).
		SetTo(gomailer.MustAddress("bob@example.com", "Bob")).
		SetSubject("Hello via DSN").
		SetText([]byte("Configured from a DSN string."))

	mailer := gomailer.NewMailer(t)
	defer mailer.Close()

	if err := mailer.Send(context.Background(), msg, nil); err != nil {
		log.Fatalf("send failed: %v", err)
	}
}
```

!!! tip "Forgot the blank import?"
    Without `_ "…/transport/smtp"`, `FromDSN` returns `ErrUnsupportedScheme` for
    `smtp://`. The `null://` scheme is always available without any import. See
    the full [DSN reference](../guides/dsn.md).

## Run it locally with the test transport

You don't need a real SMTP server to exercise your sending code. The
`mailertest.RecordingTransport` is a drop-in `Transport` that captures every
message in memory, with assertion helpers for tests.

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/shyim/go-mailer"
	"github.com/shyim/go-mailer/mailertest"
)

func main() {
	rec := mailertest.NewRecordingTransport("")
	mailer := gomailer.NewMailer(rec)

	msg := gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("alice@example.com", "Alice")).
		SetTo(gomailer.MustAddress("bob@example.com", "Bob")).
		SetSubject("Welcome").
		SetText([]byte("hi"))

	if err := mailer.Send(context.Background(), msg, nil); err != nil {
		log.Fatal(err)
	}

	fmt.Println("captured:", rec.Count()) // 1
	if sent, ok := rec.Last(); ok {
		fmt.Println(string(sent.Bytes())) // the serialized MIME message
	}
}
```

Inside a real test, prefer the assertion helpers:

```go
func TestSendsWelcome(t *testing.T) {
	rec := mailertest.NewRecordingTransport("")
	mailer := gomailer.NewMailer(rec)

	msg := gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("alice@example.com", "")).
		SetTo(gomailer.MustAddress("bob@example.com", "")).
		SetSubject("Welcome").
		SetText([]byte("hi"))

	if err := mailer.Send(context.Background(), msg, nil); err != nil {
		t.Fatal(err)
	}

	mailertest.AssertEmailCount(t, rec, 1)
	mailertest.AssertEmailContains(t, rec, "Subject: Welcome")
}
```

`rec.FailNext(err)` forces the next `Send` to fail — handy for exercising
failover and round-robin paths. More in [Testing](../guides/testing.md).

## Next steps

- [Sending mail](../guides/sending-mail.md) — addresses, HTML/text bodies, attachments, headers.
- [Transports](../concepts/transports.md) — SMTP, sendmail, null, and the composites.
- [DSN reference](../guides/dsn.md) — every scheme and option.
- [Middleware](../concepts/middleware.md) — mutate, reject, and observe sends.
