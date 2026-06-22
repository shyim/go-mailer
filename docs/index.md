# gomailer

**gomailer** is an idiomatic Go email library. It builds RFC 5322 / MIME messages and delivers them over SMTP, a local `sendmail` binary, or a null sink — with DSN-based configuration, a transport-middleware pipeline, rate throttling, round-robin / failover / named-transport routing, and optional OpenTelemetry (OTLP) tracing and metrics. The core mailer and transport packages are stdlib-first; the network transports and the OTel adapter are opt-in plug-in modules, so you only pull in what you use.

!!! warning "Pre-1.0"
    gomailer is pre-1.0. The public API may still change between releases. Pin a specific version in your `go.mod`.

## Why gomailer

- **Small, composable interfaces.** A `Transport` is just one method; concrete transports embed a shared `BaseTransport` that handles envelope derivation and throttling, so each only implements the actual send.
- **Real transports.** Production ESMTP (STARTTLS, AUTH PLAIN/LOGIN/CRAM-MD5, SMTPUTF8), a `sendmail` binary transport, and a `null` sink.
- **Routing & resilience.** Compose `RoundRobinTransport`, `FailoverTransport`, and a header-routed `Transports` router — all driven from a DSN if you want.
- **Testable by design.** `mailertest.RecordingTransport` is a drop-in `Transport` that captures messages in memory, with ready-made assertion helpers.
- **Optional OTLP.** A provider-agnostic `middleware` core (zero third-party deps) plus an `otelmw` adapter in its own module for real OpenTelemetry traces and metrics.

## Hello, world

```go
package main

import (
	"context"
	"log"

	"github.com/shyim/go-mailer"
	"github.com/shyim/go-mailer/transport/smtp"
)

func main() {
	t := smtp.NewTransport("smtp.example.com", 587, false).
		SetUsername("user").SetPassword("pass")

	msg := gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("alice@example.com", "Alice")).
		SetTo(gomailer.MustAddress("bob@example.com", "Bob")).
		SetSubject("Hello").
		SetText([]byte("Hello, Bob!"))

	mailer := gomailer.NewMailer(t)
	defer mailer.Close()

	if err := mailer.Send(context.Background(), msg, nil); err != nil {
		log.Fatalf("send failed: %v", err)
	}
}
```

## Explore the docs

<div class="grid cards" markdown>

- :material-rocket-launch: **[Getting started](getting-started/index.md)**

    Install the modules you need and send your first message in minutes.

- :material-cube-outline: **[Concepts](concepts/index.md)**

    How transports, the middleware pipeline, and the error model fit together.

- :material-book-open-variant: **[Guides](guides/index.md)**

    Task-focused walkthroughs: sending mail, DSN config, observability, testing, production.

- :material-format-list-bulleted: **[Reference](reference/index.md)**

    DSN options and the supported feature set, with notes on what is intentionally out of scope.

</div>

## Feature matrix

| Capability | Status | Where |
|---|---|---|
| MIME builder (text + HTML + attachments) | Built in (root module) | [Sending mail](guides/sending-mail.md) |
| SMTP / ESMTP transport | Plug-in module `transport/smtp` | [Transports](concepts/transports.md) |
| Sendmail transport | Plug-in module `transport/sendmail` | [Transports](concepts/transports.md) |
| Null transport | Built in (root module) | [Transports](concepts/transports.md) |
| RoundRobin / Failover / router | Built in (root module) | [Transports](concepts/transports.md) |
| DSN-based configuration | Built in (root module) | [DSN configuration](guides/dsn.md) |
| Middleware (BeforeSend / AfterSend) | Built in (root module) | [Middleware & hooks](concepts/middleware.md) |
| OpenTelemetry traces + metrics | Plug-in module `middleware/otelmw` | [Observability (OTLP)](guides/observability.md) |
| In-memory test transport + asserts | Built in (`mailertest`) | [Testing](guides/testing.md) |
| Async / queue delivery | Not supported (synchronous send only) | [Transports](concepts/transports.md) |

The **root module** (`github.com/shyim/go-mailer`) is stdlib-only apart from `golang.org/x/net` (IDNA). The concrete network transports and the OTel adapter live in separate modules — see [Modules](getting-started/modules.md).
