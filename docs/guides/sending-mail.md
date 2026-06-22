# Sending mail

This guide walks through composing a [`*Message`](../concepts/architecture.md), attaching
files, and delivering it with a [`Mailer`](../concepts/architecture.md) over a
[transport](../concepts/transports.md). Sending is **synchronous**: `Send` returns
only once the transport has accepted (or failed) the message.

## Compose a message

`NewMessage()` returns a builder. Every setter returns the receiver, so calls
chain. Addresses are built with `MustAddress(email, name)` (panics on an invalid
address) or the error-returning `NewAddress` / `ParseAddress` / `ParseAddressList`.

```go
import "github.com/shyim/go-mailer"

msg := gomailer.NewMessage().
    SetFrom(gomailer.MustAddress("alice@example.com", "Alice")).
    SetTo(gomailer.MustAddress("bob@example.com", "Bob")).
    SetSubject("Hello").
    SetText([]byte("Hello, Bob!"))
```

!!! note "Address helpers"
    Use `NewAddress` when the input comes from outside your program so you can
    handle the error instead of panicking:

    ```go
    from, err := gomailer.NewAddress(userInput, "")
    if err != nil {
        return err
    }
    ```

### Text and HTML bodies

Set both a plain-text and an HTML body and gomailer emits a
`multipart/alternative` part automatically, with the text part first (least to
most faithful, per RFC 2046):

```go
msg.
    SetText([]byte("Hello, Bob! View this email in an HTML client.")).
    SetHTML([]byte("<h1>Hello, Bob!</h1><p>Welcome aboard.</p>"))
```

Bodies are `[]byte`. Both are serialized as UTF-8 with quoted-printable
encoding.

### Cc, Bcc and Reply-To

```go
msg.
    SetCc(gomailer.MustAddress("carol@example.com", "Carol")).
    SetBcc(gomailer.MustAddress("audit@example.com", "")).
    SetReplyTo(gomailer.MustAddress("support@example.com", "Support"))
```

Each setter accepts a variadic list and **replaces** any previous value:

```go
msg.SetTo(
    gomailer.MustAddress("bob@example.com", "Bob"),
    gomailer.MustAddress("dave@example.com", "Dave"),
)
```

!!! warning "Bcc is kept out of the wire headers"
    `Bcc` recipients are deliberately **not** written into the serialized
    message headers — otherwise every recipient would see them. They are still
    delivered: when the [envelope](../concepts/architecture.md) is derived from the
    message, recipients are the union of `To`, `Cc` **and** `Bcc`. So a Bcc'd
    address receives the mail without appearing in any header.

### Custom headers

`SetHeader(name, value)` adds or replaces a header (case-insensitive name
match). Structural MIME headers (`Content-Type`, `From`, `To`, `Subject`, …)
cannot be overridden this way, and CRLF in the name or value is rejected to
prevent header injection.

```go
msg.
    SetHeader("X-Campaign", "welcome-2026").
    SetHeader("X-Priority", "1")
```

!!! tip "Routing header"
    `X-Transport` selects a named transport when you send through a
    [`Transports` router](../concepts/transports.md). The router reads and then
    strips it before delivery.

## Attachments

Attach files with `Attach(Attachment{...})`. The `Attachment` struct:

```go
type Attachment struct {
    Filename    string // filename for the Content-Disposition
    ContentType string // MIME type; sniffed from data/filename if empty
    Data        []byte // raw bytes (base64-encoded on write)
    Inline      bool   // true => Content-Disposition: inline + a Content-ID
    ContentID   string // referenced by HTML via cid:; auto-generated if Inline and empty
}
```

### Regular attachment

Leave `ContentType` empty to let gomailer sniff it from the filename extension
(then the data, falling back to `application/octet-stream`):

```go
msg.Attach(gomailer.Attachment{
    Filename: "invoice.pdf",
    Data:     pdfBytes, // []byte
})
```

### Inline image referenced from HTML

Set `Inline: true` and reference the part from your HTML with `cid:`. If you
leave `ContentID` empty, gomailer generates one — so set it explicitly when the
HTML needs to point at it:

```go
msg.
    SetHTML([]byte(`<p>Logo: <img src="cid:logo@example.com"></p>`)).
    Attach(gomailer.Attachment{
        Filename:    "logo.png",
        ContentType: "image/png",
        Data:        logoBytes,
        Inline:      true,
        ContentID:   "logo@example.com",
    })
```

Inline parts are placed in a `multipart/related` container alongside the body;
regular attachments wrap everything in `multipart/mixed`. gomailer picks the
simplest structure that fits the parts you set.

## Send it

Wrap a transport in a `Mailer` and call `Send`. Pass `nil` for the envelope to
have it derived from the message.

```go
package main

import (
    "context"
    "log"

    "github.com/shyim/go-mailer"
    "github.com/shyim/go-mailer/transport"
    _ "github.com/shyim/go-mailer/transport/smtp"
)

func main() {
    t, err := transport.FromDSN("smtp://user:pass@smtp.example.com:587", transport.Deps{})
    if err != nil {
        log.Fatal(err)
    }
    mailer := gomailer.NewMailer(t)
    defer mailer.Close()

    msg := gomailer.NewMessage().
        SetFrom(gomailer.MustAddress("alice@example.com", "Alice")).
        SetTo(gomailer.MustAddress("bob@example.com", "Bob")).
        SetSubject("Hello").
        SetText([]byte("Hello, Bob!")).
        SetHTML([]byte("<p>Hello, Bob!</p>"))

    if err := mailer.Send(context.Background(), msg, nil); err != nil {
        log.Fatalf("send failed: %v", err)
    }
}
```

### Explicit envelope vs derived

`Mailer.Send(ctx, msg, envelope, ...)` takes a `*Envelope`:

=== "Derived (nil)"

    The envelope sender is the first `From` (or `Sender`) address and the
    recipients are `To` + `Cc` + `Bcc`. This is what you want most of the time.

    ```go
    err := mailer.Send(ctx, msg, nil)
    ```

=== "Explicit"

    Supply your own when the SMTP sender or recipients must differ from the
    headers — for example a `Return-Path` for bounce handling, or a
    pre-serialized `RawMessage` that carries no addressing.

    ```go
    env, err := gomailer.NewEnvelope(
        gomailer.MustAddress("bounce@example.com", ""),       // MAIL FROM
        []gomailer.Address{
            gomailer.MustAddress("bob@example.com", ""),       // RCPT TO
            gomailer.MustAddress("audit@example.com", ""),
        },
    )
    if err != nil {
        return err
    }
    err = mailer.Send(ctx, msg, env)
    ```

!!! note "RawMessage requires an explicit envelope"
    A message wrapped with `gomailer.NewRawMessage(bytes)` carries no `From`/`To`,
    so it **must** be sent with an explicit `*Envelope`. Deriving one from raw
    bytes returns an error.

## Context and timeouts

The `context.Context` controls cancellation and deadlines for the whole send.
Pass a context with a deadline to bound a slow relay:

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()

if err := mailer.Send(ctx, msg, nil); err != nil {
    log.Printf("send failed: %v", err)
}
```

!!! tip "Default socket deadline"
    If the context has **no** deadline, the SMTP transport applies a default 30s
    per-operation socket deadline so a hung server returns an error instead of
    blocking the goroutine forever. A context deadline always takes precedence;
    override the default with `smtp.Transport.SetTimeout(d)`.

See [Errors](../concepts/errors.md) for classifying failures with
`errors.Is(err, gomailer.ErrTransport)` and reaching the SMTP `Code` via
`errors.As`.

## Graceful shutdown

`Mailer.Close()` fans out to the underlying transport. For SMTP (and the
RoundRobin / Failover / Transports composites, which implement `io.Closer`) this
sends `QUIT` and releases pooled connections. Call it on shutdown:

```go
mailer := gomailer.NewMailer(t)
defer mailer.Close()
```

`Close` is safe to call more than once, and is a no-op for transports that hold
no resources (such as the null and recording transports).
