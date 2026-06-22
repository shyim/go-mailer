# Architecture

gomailer builds MIME messages and delivers them over SMTP, sendmail, or a null
sink. Its design rests on three ideas: **small interfaces**, an **embedded
struct that holds a shared send pipeline**, and **transport decorators** that
nest freely. This page explains the core abstractions and how they fit
together.

## The layering at a glance

```
 caller
   │  NewMessage().SetFrom(...).SetTo(...).SetSubject(...)        ── MIME builder
   ▼
 *Mailer ──────────────────────────────────────────  wraps one Transport
   │  Send(ctx, msg RawMessage, envelope *Envelope)
   ▼
 Transport (interface)
   │  Send(ctx, RawMessage, *Envelope) (*SentMessage, error)
   │  String() string
   │
   ├─ middleware.Wrap(...)        decorators: BeforeSend / AfterSend / Observability
   │      │  (transparent: String() delegates to the wrapped transport)
   │      ▼
   ├─ composites (decorate other Transports)
   │      ├─ RoundRobinTransport   rotate per send, skip dead leaves
   │      ├─ FailoverTransport     sticky leaf, advance on failure
   │      └─ Transports            route by the X-Transport header
   │
   └─ leaf transports = BaseTransport + DoSend hook
          ├─ smtp.Transport       ESMTP over net.Conn
          ├─ sendmail.Transport   pipe raw MIME to a local binary
          ├─ NullTransport        discards everything
          └─ RecordingTransport   in-memory capture (mailertest)
```

Everything below `Transport` satisfies the same interface, so composites,
decorators and leaves nest freely.

## The `Transport` interface

`Transport` is the single seam every concrete transport, decorator and router
satisfies:

```go
type Transport interface {
    // Send delivers msg using envelope. A nil envelope is derived from the
    // message. A nil *SentMessage with a nil error means the send was rejected
    // by middleware (see middleware.BeforeSend / middleware.ErrReject).
    Send(ctx context.Context, msg RawMessage, envelope *Envelope) (*SentMessage, error)
    // String returns the transport's DSN-like identity, e.g. "smtp://host".
    String() string
}
```

Because the interface is so small, a decorator can wrap a transport without the
layers above knowing — and a composite can hold a slice of them.

## `BaseTransport`: one shared pipeline, one pluggable step

Every transport runs the same send pipeline — derive the envelope, serialize the
message, apply throttling, then hand off the actual delivery. Only that last
step differs between an SMTP transport, a sendmail transport, and a null sink.

gomailer implements that pipeline **once** in `BaseTransport` and exposes the
variable step as a function field, `DoSend`. A concrete transport **embeds
`BaseTransport` by value** and sets two fields:

```go
type NullTransport struct{ gomailer.BaseTransport }

func NewNullTransport() *NullTransport {
    t := &NullTransport{}
    t.Name = "null://"
    t.DoSend = func(ctx context.Context, sm *gomailer.SentMessage) error {
        return nil // the per-transport "doSend" step
    }
    return t
}
```

`BaseTransport.Send` runs the shared steps in order:

1. **Guard** — return early if the context is already cancelled or the message
   is nil.
2. **Clone** — if `msg` is a `*Message`, clone it so delivery never mutates the
   caller's retained message (header rewrites stay local to the send).
3. **Envelope** — use the supplied `*Envelope` (cloned) or derive one from the
   message via `EnvelopeFromMessage`.
4. **Build** — serialize to a `*SentMessage` (wire bytes + envelope + Message-ID).
5. **Throttle** — pace delivery starts, sleeping while honoring `ctx`
   cancellation.
6. **`DoSend`** — invoke the hook; wrap any failure so it is classifiable via
   `errors.Is(err, ErrTransport)`.

```go
type SenderFunc func(ctx context.Context, sm *SentMessage) error
```

!!! note "One varying step via a func field"
    `DoSend SenderFunc` is the one step a leaf supplies; `BaseTransport` owns
    the invariant pipeline around it. No interface dance — just a struct
    embedded by value with one function field set.

Throttle state (`maxPerSecond`, `lastSent`) is mutex-guarded. A value-embedded
`BaseTransport` may be shared across goroutines, so the throttle clock needs
synchronization.

## `Mailer`: a thin wrapper over one `Transport`

`Mailer` is the public entry point. It holds exactly one `Transport` and
forwards to it, discarding the returned `*SentMessage` and surfacing only the
error:

```go
func (m *Mailer) Send(ctx context.Context, msg RawMessage, envelope *Envelope, opts ...SendOption) error {
    _, err := m.transport.Send(ctx, msg, envelope)
    return err
}
```

All cross-cutting behavior is layered onto the `Transport` **before** it reaches
`NewMailer` — see [Middleware](middleware.md). `Mailer` also
implements `io.Closer`; `Close()` delegates to the transport if it too is a
closer, so pooled SMTP connections are QUIT on shutdown.

!!! warning "Synchronous send only"
    `Mailer.Send` blocks until delivery completes or fails. There is no queue,
    worker, or async path. The `SendOption` variadic is a reserved seam for a
    future queue; no options are defined today.

## Message, Envelope, SentMessage

These three types carry the data through the pipeline:

| Type | Role |
|------|------|
| `*Message` | The **MIME builder**. Fluent setters (`SetFrom`, `SetTo`, `SetSubject`, `SetText`, `SetHTML`, `Attach`, `SetHeader`) build an RFC 5322 / multipart message; it serializes itself to bytes. Implements `RawMessage`. |
| `*Envelope` | The **SMTP-level addressing** — sender + recipients — independent of the message headers. Derived from the message (From/Sender as sender, To+Cc+Bcc as recipients) or supplied explicitly via `NewEnvelope`. |
| `*SentMessage` | The **result** of a send: the serialized wire bytes, the envelope used, the transport-level message id, and an optional debug transcript. |

`RawMessage` is the minimal interface a transport actually needs (it can produce
bytes). A `*Message` is the rich builder; `NewRawMessage(data []byte)` wraps
pre-serialized bytes when you already have a MIME blob. Note that a bare
`RawMessage` has no addressing, so it **requires an explicit envelope** —
`EnvelopeFromMessage` only works on a `*Message`.

!!! tip "Envelope vs. headers"
    The envelope is what the SMTP server sees in `MAIL FROM` / `RCPT TO`; the
    headers are what the recipient sees. They can differ. `Bcc` recipients are
    kept **in the envelope** but **out of the headers**, so blind recipients
    stay hidden. Message-ID and Date are materialized once, making
    `Message.Bytes()` deterministic.

See [Sending mail](../guides/sending-mail.md) and [Transports](transports.md)
for the per-type guides.

## Composites decorate `Transport`

Because routers and decorators implement the same `Transport` interface, they
compose recursively:

- **`RoundRobinTransport`** rotates to the next leaf on each send and skips
  leaves it has marked dead until a `retryPeriod` elapses.
- **`FailoverTransport`** sticks to the current leaf and only advances when it
  fails.
- **`Transports`** routes by the `X-Transport` header, defaulting to the first
  named transport.
- **`middleware.Wrap`** decorates a single transport with `BeforeSend` /
  `AfterSend` / observability layers; its `String()` delegates to the wrapped
  transport so routers see unchanged identities. See [Middleware](middleware.md).

```go
fo, _ := transport.NewFailoverTransport([]gomailer.Transport{primary, backup}, 0)
mailer := gomailer.NewMailer(fo)
```

!!! note "Composites are single-flight"
    `RoundRobin` and `Failover` **serialize** their sends to keep rotation and
    dead-tracking stable; they do not parallelize delivery across servers. For
    concurrent throughput, fan out across independent transports yourself. The
    composites (and `Mailer`) implement `io.Closer`, and `Close()` fans out to
    every leaf. The routers clone the message per delivery attempt, so a
    `BeforeSend` mutation on one failover attempt never bleeds into the next.

## Where to go next

- [Transports](transports.md) — the leaf and composite transport catalog.
- [DSN configuration](../guides/dsn.md) — build transports from a URL.
- [Middleware](middleware.md) — `BeforeSend` / `AfterSend` hooks and
  observability.
- [Error handling](errors.md) — the sentinel hierarchy and
  `*TransportError`.
