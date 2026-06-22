# DSN configuration

A DSN (Data Source Name) is a single string that fully describes a transport:
which protocol to speak, where to connect, the credentials to use, and any
tuning options. It is the most convenient way to configure gomailer from an
environment variable or config file: a single string captures the entire
transport setup, so you can change delivery backends without touching code.

## Format

```
scheme://[user[:password]@]host[:port][?option=value&...]
```

| Part | Meaning |
|------|---------|
| `scheme` | the transport protocol — `null`, `smtp`, `smtps`, `sendmail` |
| `user` / `password` | optional credentials (URL-encode reserved characters) |
| `host` | server hostname (or a sentinel like `default` for schemes that ignore it) |
| `port` | optional; each scheme has a sensible default |
| `?option=value` | scheme-specific tuning options |

!!! warning "Credentials live in the DSN"
    The DSN embeds the username and password in clear text. Treat it as a
    secret: load it from an environment variable or secrets manager, and keep it
    out of logs. The parser never echoes credentials — error messages and
    `Transport.String()` report only the scheme and host, never the password —
    so don't reintroduce them by logging the raw DSN yourself.

## Resolving a DSN

Use [`transport.FromDSN`](../reference/dsn-options.md) to turn a single DSN into a
[`gomailer.Transport`](../concepts/transports.md):

```go
import (
    "github.com/shyim/go-mailer/transport"
    _ "github.com/shyim/go-mailer/transport/smtp" // register the smtp/smtps schemes
)

t, err := transport.FromDSN(
    "smtp://user:pass@smtp.example.com:587?require_tls=true",
    transport.Deps{},
)
if err != nil {
    log.Fatal(err)
}
```

`transport.Deps` carries optional dependencies (e.g. a `*log.Logger`); the
zero value `transport.Deps{}` is valid.

## Required blank imports

The concrete transport packages register their schemes from an `init()` function
— the same pattern as `database/sql` drivers. A scheme is only resolvable if its
package has been imported, so **blank-import the transports you reference**:

```go
import (
    "github.com/shyim/go-mailer/transport"

    _ "github.com/shyim/go-mailer/transport/sendmail" // sendmail://
    _ "github.com/shyim/go-mailer/transport/smtp"     // smtp://, smtps://
)
```

!!! note
    The `null://` scheme is built into the root module and is always available
    without a blank import. Every other scheme requires importing its package.
    Forgetting the import surfaces as an `ErrUnsupportedScheme` error at resolve
    time, not a compile error.

Remember each transport package is a separate Go module, so `go get` it first:

```sh
go get github.com/shyim/go-mailer/transport/smtp
go get github.com/shyim/go-mailer/transport/sendmail
```

## Supported schemes

| Scheme | Transport | Default port |
|--------|-----------|--------------|
| `null://` | discards every message (built in) | — |
| `smtp://` | SMTP / ESMTP with opportunistic STARTTLS | 25 |
| `smtps://` | SMTP over implicit TLS (TLS on connect) | 465 |
| `sendmail://` | pipes raw MIME to a local `sendmail` binary | — |
| `ses://` | Amazon SES (see the [SES guide](ses.md)) | — |

```go
transport.FromDSN("null://default", transport.Deps{})
transport.FromDSN("smtp://user:pass@mail.example.com:587", transport.Deps{})
transport.FromDSN("smtps://user:pass@mail.example.com", transport.Deps{})
transport.FromDSN("sendmail://default?command=/usr/sbin/sendmail+-t+-i", transport.Deps{})
transport.FromDSN("ses://default?region=us-east-1", transport.Deps{})
```

The full per-scheme option table lives in
[Reference: DSN options](../reference/dsn-options.md).

## Named transports (routing)

[`transport.FromDSNs`](../reference/dsn-options.md) resolves a `name → DSN` map
into a `*Transports` router. Each message is dispatched to a named transport via
the `X-Transport` header; messages without that header use the first transport.

```go
router, err := transport.FromDSNs(map[string]string{
    "main":   "smtp://user:pass@a.example.com",
    "backup": "sendmail://default",
}, transport.Deps{})
if err != nil {
    log.Fatal(err)
}

mailer := gomailer.NewMailer(router)
```

See [Transports](../concepts/transports.md) for how routing and the `X-Transport`
header behave.

## Composite DSNs

A single DSN string can wrap several inner DSNs in a `failover(...)` or
`roundrobin(...)` keyword. The wrappers are recursive, so they may nest, and the
inner DSNs are space-separated:

=== "Failover"

    ```go
    // Try the primary first; advance to the next only when one fails.
    transport.FromDSN(
        "failover(smtp://user:pass@a.example.com smtp://user:pass@b.example.com)",
        transport.Deps{},
    )
    ```

=== "Round-robin"

    ```go
    // Rotate per send; skip a dead transport until retry_period elapses.
    transport.FromDSN(
        "roundrobin(smtp://user:pass@a.example.com smtp://user:pass@b.example.com)?retry_period=15",
        transport.Deps{},
    )
    ```

The optional `?retry_period=<seconds>` suffix (an integer number of seconds)
controls how long a failed inner transport is treated as dead before it is
retried; it applies to both `failover(...)` and `roundrobin(...)`.

!!! tip
    These composites serialize their sends (single-flight) for stable rotation
    and dead-transport tracking — they do not parallelize delivery. See
    [Transports](../concepts/transports.md) for the full failover/round-robin
    semantics and concurrency notes.

## Next steps

- [Reference: DSN options](../reference/dsn-options.md) — the complete option table per scheme.
- [Transports](../concepts/transports.md) — what each transport and composite does.
