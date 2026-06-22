# Transports

A `Transport` is the thing that actually delivers a message. Everything in gomailer
ultimately calls one, and the interface is tiny:

```go
type Transport interface {
    Send(ctx context.Context, msg RawMessage, envelope *Envelope) (*SentMessage, error)
    String() string
}
```

`String()` is the transport's stable identity — it shows up in debug output,
observability spans, and the composite/router labels below.

## The transports

| Transport | Constructor | Identity (`String()`) | Notes |
|-----------|-------------|-----------------------|-------|
| SMTP / ESMTP | `smtp.NewTransport(host, port, tlsOnConnect)` | `smtp://host` / `smtps://host` | STARTTLS, AUTH PLAIN/LOGIN/CRAM-MD5, restart & ping thresholds, SMTPUTF8 |
| Sendmail | `sendmail.NewSendmailTransport(command)` | `smtp://sendmail` | pipes raw MIME to a `-t` mode binary (`-bs` not supported) |
| Amazon SES | `ses.New(ctx, opts...)` | `ses://` / `ses://<region>` | SES v2 `SendEmail` with raw MIME; see the [SES guide](../guides/ses.md) |
| Null | `transport.NewNullTransport()` | `null://` | discards everything |
| RoundRobin | `transport.NewRoundRobinTransport(ts, retryPeriod)` | `roundrobin(a b)` | rotates per send, skips dead transports until `retryPeriod` |
| Failover | `transport.NewFailoverTransport(ts, retryPeriod)` | `failover(a b)` | sticky current transport, advances on failure |
| Transports (router) | `transport.NewTransports(map, order)` | `[main,backup]` | routes by the `X-Transport` header, defaults to the first transport |
| Recording (test) | `mailertest.NewRecordingTransport(name)` | `test://` | in-memory capture for tests |

!!! note "Each network transport is its own module"
    The root module (`github.com/shyim/go-mailer`) ships the abstractions, the
    `null` transport, the composites, the router, and the test transport. The SMTP
    and sendmail transports live in separate modules — `go get` the ones you use,
    and blank-import them if you resolve them from a [DSN](../guides/dsn.md).

## Leaf transports

### SMTP / ESMTP

```go
import "github.com/shyim/go-mailer/transport/smtp"

t := smtp.NewTransport("smtp.example.com", 587, false). // tlsOnConnect=false → STARTTLS
    SetUsername("user").
    SetPassword("pass").
    SetRequireTLS(true)
```

Setters are chainable: `SetUsername`, `SetPassword`, `SetAutoTLS`, `SetRequireTLS`,
`SetAllowPlaintextAuth`, `SetTimeout`, `SetLocalDomain`, `SetTLSConfig`,
`SetAuthenticators`, `SetRestartThreshold`, `SetPingThreshold`. Pass
`tlsOnConnect=true` (or use port 465 / `smtps://`) for implicit TLS.

!!! warning "Safe by default"
    Cleartext AUTH is refused over an unprotected connection, and TLS is verified
    against the server certificate and hostname. Only opt out for a trusted local
    relay with `SetAllowPlaintextAuth(true)`. See [Production](../guides/production.md).

### Sendmail

```go
import "github.com/shyim/go-mailer/transport/sendmail"

t, err := sendmail.NewSendmailTransport("/usr/sbin/sendmail -t -i")
```

Pipes the raw MIME bytes to a local `sendmail`-compatible binary in `-t` mode. The
default command is `/usr/sbin/sendmail -t -i`; the interactive `-bs` SMTP-over-pipe
mode is intentionally unsupported.

### Null

```go
t := transport.NewNullTransport()
```

Accepts and discards everything. Useful as a wiring placeholder or in environments
where mail must never actually leave.

## Composites: RoundRobin vs Failover

Both wrap a slice of transports and fail over on a transport-level error, but they
differ in how they pick the next one.

=== "RoundRobin"

    **Rotates on every send.** Each call advances the cursor to the next live
    transport, spreading load across the pool. A transport that fails is marked
    dead and skipped until `retryPeriod` has elapsed, then retried.

=== "Failover"

    **Sticky.** It stays on the current transport for every send and only advances
    when that transport is marked dead. Effectively "use the primary until it
    breaks, then the backup." Its initial cursor is deterministic (`0`), so the
    first transport in the slice is the primary.

Shared mechanics:

- **Dead-tracking + `retryPeriod`.** A failed transport is marked dead and skipped
  until `retryPeriod` passes (`<= 0` selects the 60s default), then it is retried.
- **Only transport errors fail over.** Failover triggers only when the underlying
  error satisfies `errors.Is(err, gomailer.ErrTransport)`. Anything else (context
  cancellation, validation) is returned immediately.
- **Aggregate error.** When every transport is dead the composite returns a
  `*gomailer.TransportError` whose `Debug()` transcript lists each attempt. The
  most recent underlying SMTP `Code` is lifted onto the aggregate, so
  `errors.As(err, &te)` lets you branch on `te.Code` (4xx retryable vs 5xx permanent).
- **Per-attempt clone.** The message and envelope are cloned before each attempt,
  so a failing transport's header mutations never leak into the next attempt or
  back into your retained message.

```go
import (
    "github.com/shyim/go-mailer"
    "github.com/shyim/go-mailer/transport"
    "github.com/shyim/go-mailer/transport/smtp"
)

primary := smtp.NewTransport("a.example.com", 587, false)
backup := smtp.NewTransport("b.example.com", 587, false)

fo, err := transport.NewFailoverTransport(
    []gomailer.Transport{primary, backup},
    15*time.Second, // retryPeriod; 0 → 60s default
)
if err != nil {
    log.Fatal(err)
}

mailer := gomailer.NewMailer(fo)
```

Swap `NewFailoverTransport` for `NewRoundRobinTransport` (same signature) to rotate
instead of stick.

### Single-flight (serialized) behavior

!!! warning "Composites serialize sends"
    `RoundRobin` and `Failover` run one `Send` at a time across the whole pool. The
    blocking network round-trip happens under that lock, so the composite does **not**
    parallelize delivery across servers — and a slow server stalls other senders for
    up to its IO timeout. This is deliberate: it keeps cursor and dead-tracking
    semantics stable when the composite is shared across goroutines.

    For concurrent throughput, run several independent composites (or wrap leaves
    and fan out yourself) rather than expecting one composite to parallelize.

### SetLogger

Both composites expose `SetLogger(*slog.Logger)`, which emits a debug record each
time a transport is marked dead — operator visibility into "why did mail go out the
backup MX":

```go
fo.SetLogger(slog.Default())
```

## Transports router

`Transports` routes each message to a **named** transport based on its
`X-Transport` header, falling back to the default (first in `order`) when the header
is absent or the message is a header-less `RawMessage`.

```go
router, err := transport.NewTransports(map[string]gomailer.Transport{
    "main":   primary,
    "backup": backup,
}, []string{"main", "backup"}) // order; first → default
```

Pick a transport per message with `SetHeader`:

```go
msg := gomailer.NewMessage().
    SetFrom(gomailer.MustAddress("alice@example.com", "Alice")).
    SetTo(gomailer.MustAddress("bob@example.com", "Bob")).
    SetSubject("Receipt").
    SetText([]byte("...")).
    SetHeader("X-Transport", "backup") // route this one to "backup"
```

The `X-Transport` header is stripped from the send-local clone before delivery, so
it never reaches the wire and concurrent sends of the same message stay race-free.
An unknown transport name returns an `ErrInvalidArgument`-classified error.

## Wrapping leaves with middleware

Composites and the router delegate `String()` to their leaves and loop over them on
each attempt. To get one observability span (or `BeforeSend`/`AfterSend` hook) per
**delivery attempt** — including every failover retry — wrap each leaf, not the
composite. Use `middleware.Chain` to reuse the same stack:

```go
import "github.com/shyim/go-mailer/middleware"

stack := middleware.Chain(mw) // mw is a middleware.Middleware

fo, _ := transport.NewFailoverTransport([]gomailer.Transport{
    stack(primary),
    stack(backup),
}, 0)
```

Wrapping the composite instead yields a single span covering all retries. See
[Middleware](./middleware.md) for the hook pipeline and observability wiring.

## Graceful shutdown

`Mailer`, and the `RoundRobin` / `Failover` / `Transports` composites, implement
`io.Closer`. `Close()` fans out to every underlying transport so pooled SMTP
connections are QUIT and released — call it on shutdown:

```go
defer mailer.Close()
```

## Testing

For tests, use the recording transport instead of a real one — it captures every
message in memory:

```go
import "github.com/shyim/go-mailer/mailertest"

rec := mailertest.NewRecordingTransport("")
rec.FailNext(gomailer.NewTransportError("boom")) // force the next Send to fail
```

`Messages()`, `Last()`, `Count()`, and `Reset()` inspect captured sends;
`FailNext(err)` is handy for exercising failover and round-robin behavior. See
[Testing](../guides/testing.md) for the assertion helpers.
