# Installation

gomailer is a **multi-module workspace**. The root module ships the abstractions,
the MIME builder, the `null` transport, the composites/router, the DSN registry,
the middleware core, and the test transport. Concrete network transports and the
OpenTelemetry adapter live in their own modules, so you pull in only what you use.

!!! note "Go 1.26 required"
    All modules declare `go 1.26`. Make sure your toolchain is at least Go 1.26
    (`go version`).

## Install the root module

```sh
go get github.com/shyim/go-mailer
```

The root module is stdlib-first: its only non-stdlib dependency is
`golang.org/x/net` (for IDNA / internationalized SMTP domains). Importing it never
pulls in OpenTelemetry or any third-party SMTP client.

```go
import "github.com/shyim/go-mailer"
```

This gives you `NewMessage`, the address helpers, `NewMailer`, the DSN registry
(`transport.FromDSN` / `transport.FromDSNs`), the `null` transport, the
round-robin / failover / named-transport composites, and the middleware core.

## Add the transports you need

The concrete network transports are separate modules. `go get` each one you use:

=== "SMTP"

    ```sh
    go get github.com/shyim/go-mailer/transport/smtp
    ```

    ```go
    import "github.com/shyim/go-mailer/transport/smtp"
    ```

=== "Sendmail"

    ```sh
    go get github.com/shyim/go-mailer/transport/sendmail
    ```

    ```go
    import "github.com/shyim/go-mailer/transport/sendmail"
    ```

## Optional: OpenTelemetry adapter

The `middleware` core is stdlib-only. The OpenTelemetry adapter that binds a real
`TracerProvider` / `MeterProvider` to that core lives in its own module so the
root module's dependency graph stays free of `go.opentelemetry.io/*`:

```sh
go get github.com/shyim/go-mailer/middleware/otelmw
```

```go
import "github.com/shyim/go-mailer/middleware/otelmw"
```

See [Observability](../guides/observability.md) for wiring details.

## Why blank-import a transport

The concrete transport packages register their DSN schemes in `init()` (the same
pattern as `database/sql` drivers). If you resolve transports from a DSN string
with [`transport.FromDSN`](../guides/dsn.md), you must **blank-import** the
matching package so its scheme is registered — otherwise the scheme is unknown and
resolution fails with `gomailer.ErrUnsupportedScheme`.

```go
import (
    "github.com/shyim/go-mailer/transport"

    _ "github.com/shyim/go-mailer/transport/sendmail" // registers sendmail://
    _ "github.com/shyim/go-mailer/transport/smtp"     // registers smtp:// and smtps://
)

func main() {
    t, err := transport.FromDSN("smtp://user:pass@smtp.example.com:587", transport.Deps{})
    // ...
}
```

The `null://` scheme is always available without an extra import.

!!! tip "Constructing a transport directly"
    Blank imports are only needed for DSN resolution. If you call a constructor
    directly — `smtp.NewTransport(host, port, tlsOnConnect)` or
    `sendmail.NewSendmailTransport(command)` — a regular import is enough.

## A note on the local replace

While the project is unreleased, each submodule resolves the root module through a
local `replace` directive in the repository. Once the project is tagged, those
`replace` directives drop away and `go get` resolves the published version.

If you are working against a checkout of the repo rather than a tagged release
(for example a private fork before it is tagged), point your own `go.mod` at the
local sources with a `replace`:

```toml
require github.com/shyim/go-mailer v0.0.0

replace github.com/shyim/go-mailer => ../path/to/gomailer
```

## Next steps

- [Quickstart](quickstart.md) — build and send your first message.
- [Transports](../concepts/transports.md) — pick and configure a delivery backend.
- [DSN format](../guides/dsn.md) — configure transports from a string.
