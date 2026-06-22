# Getting started

gomailer is an idiomatic Go email library. It builds RFC 5322 / MIME messages
and delivers them over SMTP, a local `sendmail` binary, or a null sink — with
DSN-based configuration, a transport-middleware pipeline, rate throttling, and
round-robin / failover / named-transport routing.

This section gets you from zero to a sent email.

## What you'll learn

- **[Installation](installation.md)** — how to add the root module and the optional
  transport submodules, and why the codebase is split into separate Go modules.
- **[Quickstart](quickstart.md)** — build a `Message`, resolve a `Transport`
  from a DSN, and send it with a `Mailer`.
- **[Modules](modules.md)** — the multi-module layout: what lives in the root
  module versus the `transport/smtp`, `transport/sendmail`, and
  `middleware/otelmw` submodules, and how DSN scheme registration works.

!!! tip "Stdlib-first"
    The core mailer and transport packages are intentionally stdlib-first.
    Address parsing uses `net/mail`, MIME bodies use a hand-written multipart
    writer, and the SMTP conversation runs directly over a `net.Conn`. You only
    pull in third-party dependencies for the submodules you actually import.

## A 30-second taste

```go
package main

import (
    "context"
    "log"

    "github.com/shyim/go-mailer"
    "github.com/shyim/go-mailer/transport"
    _ "github.com/shyim/go-mailer/transport/smtp" // register the smtp:// scheme
)

func main() {
    t, err := transport.FromDSN("smtp://user:pass@smtp.example.com:587", transport.Deps{})
    if err != nil {
        log.Fatal(err)
    }

    msg := gomailer.NewMessage().
        SetFrom(gomailer.MustAddress("alice@example.com", "Alice")).
        SetTo(gomailer.MustAddress("bob@example.com", "Bob")).
        SetSubject("Hello").
        SetText([]byte("Hello, Bob!"))

    mailer := gomailer.NewMailer(t)
    if err := mailer.Send(context.Background(), msg, nil); err != nil {
        log.Fatalf("send failed: %v", err)
    }
}
```

Head to the **[Installation](installation.md)** page next, then walk through the
**[Quickstart](quickstart.md)**.
