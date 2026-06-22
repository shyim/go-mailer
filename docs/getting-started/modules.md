# Modules

gomailer is a **multi-module workspace**, not a single monolithic package. The
repository ships four independent Go modules so that the root stays light and
the heavier dependencies — the OpenTelemetry stack, the concrete network
transports — are strictly opt-in. You `go get` only the modules you actually
use, and your dependency graph reflects exactly that.

## Why split it up

The core mailer and transport abstractions are intentionally stdlib-first:
address parsing uses `net/mail`, MIME bodies use a hand-written multipart
writer, and DSN parsing is plain `net/url`. The only third-party requirement in
the root module is `golang.org/x/net` (for IDNA / internationalized domains).

Everything that would pull in larger or situational dependencies lives in its
own module:

- The **SMTP** and **sendmail** transports are separate so a project that only
  needs the `null` sink or the test recorder never compiles network code.
- The **OpenTelemetry adapter** is separate so that importing only the root
  module never drags `go.opentelemetry.io/*` into your `go list -m all`.

!!! note "Zero-dependency promise"
    If you import only the root module, your dependency graph contains the
    stdlib plus `golang.org/x/net` — and nothing else. No OTel, no transport
    extras unless you ask for them.

## The four modules

| Module | Import / `go get` | Brings |
|--------|-------------------|--------|
| `github.com/shyim/go-mailer` | (root) | abstractions, MIME builder, `null` transport, composites (round-robin / failover) + router, DSN registry, the `middleware` core, and the `mailertest` recorder |
| `github.com/shyim/go-mailer/transport/smtp` | `go get github.com/shyim/go-mailer/transport/smtp` | the ESMTP transport (STARTTLS, AUTH, restart/ping thresholds) |
| `github.com/shyim/go-mailer/transport/sendmail` | `go get github.com/shyim/go-mailer/transport/sendmail` | the local `sendmail` binary transport |
| `github.com/shyim/go-mailer/transport/ses` | `go get github.com/shyim/go-mailer/transport/ses` | the Amazon SES transport (isolates the AWS SDK) |
| `github.com/shyim/go-mailer/middleware/otelmw` | `go get github.com/shyim/go-mailer/middleware/otelmw` | OpenTelemetry traces + metrics adapter |

The root module already includes the `middleware` package (the stdlib-only
`Wrap` / `Chain` / `BeforeSend` / `AfterSend` / `Observability` core). Only the
`otelmw` adapter — the bit that touches real OpenTelemetry providers — is a
separate module.

## Adding what you need

=== "SMTP only"

    ```sh
    go get github.com/shyim/go-mailer
    go get github.com/shyim/go-mailer/transport/smtp
    ```

=== "SMTP + sendmail"

    ```sh
    go get github.com/shyim/go-mailer
    go get github.com/shyim/go-mailer/transport/smtp
    go get github.com/shyim/go-mailer/transport/sendmail
    ```

=== "With OpenTelemetry"

    ```sh
    go get github.com/shyim/go-mailer
    go get github.com/shyim/go-mailer/transport/smtp
    go get github.com/shyim/go-mailer/middleware/otelmw
    ```

## Blank-import to register a DSN scheme

The concrete transport modules register their DSN schemes in an `init()`
function — the same pattern as `database/sql` drivers. A scheme is only
resolvable by [`transport.FromDSN`](../guides/dsn.md) (or `FromDSNs`) if its
package has been imported, which means you must **blank-import** the transport
modules you want to address by DSN:

```go
import (
    "github.com/shyim/go-mailer/transport"

    _ "github.com/shyim/go-mailer/transport/sendmail" // registers sendmail://
    _ "github.com/shyim/go-mailer/transport/smtp"     // registers smtp:// + smtps://
)

t, err := transport.FromDSN(
    "smtp://user:pass@smtp.example.com:587?require_tls=true",
    transport.Deps{},
)
```

!!! warning "Forgetting the blank import"
    Without the `_ "…/transport/smtp"` import, `FromDSN("smtp://…")` fails with
    `ErrUnsupportedScheme` — the scheme was never registered. The `null://`
    scheme is the exception: it lives in the root module and is always
    available.

If you construct transports directly (for example
`smtp.NewTransport("smtp.example.com", 587, false)`) you don't need the blank
import — you only need it when resolving a scheme **from a DSN string**.

## Local development: replace directives

During development the three submodules resolve the root module through a local
`replace` directive in their own `go.mod`, for example:

```go
// transport/smtp/go.mod
module github.com/shyim/go-mailer/transport/smtp

replace github.com/shyim/go-mailer => ../..
```

This lets the modules build against the in-repo root without a published tag.
Once the project is tagged, those `replace` directives resolve to the published
version — and as a consumer of the published modules you never see them: you
just `go get` each module at a tagged version and Go resolves the root
dependency normally.

## Next steps

- [Quickstart](quickstart.md) — send your first message.
- [Transports](../concepts/transports.md) — what each transport does.
- [DSN configuration](../guides/dsn.md) — schemes, options, and composites.
