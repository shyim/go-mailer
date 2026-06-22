<div align="center">

# gomailer

**An idiomatic, stdlib-first email library for Go.**

Build RFC 5322 / MIME messages and deliver them over SMTP, `sendmail`, or a null
sink — with DSN configuration, a transport-middleware pipeline, rate throttling,
round-robin / failover / named-transport routing, and optional OpenTelemetry.

[![CI](https://github.com/shyim/go-mailer/actions/workflows/ci.yml/badge.svg)](https://github.com/shyim/go-mailer/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/shyim/go-mailer.svg)](https://pkg.go.dev/github.com/shyim/go-mailer)
[![Go Report Card](https://goreportcard.com/badge/github.com/shyim/go-mailer)](https://goreportcard.com/report/github.com/shyim/go-mailer)
[![Go Version](https://img.shields.io/github/go-mod/go-version/shyim/go-mailer)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

</div>

---

## Features

- **Stdlib-first core** — addresses via `net/mail`, a hand-written MIME multipart
  writer, and the SMTP conversation driven directly over a `net.Conn`. No
  third-party dependencies in the core beyond `golang.org/x/net` (IDNA).
- **Multiple transports** — SMTP / ESMTP (STARTTLS, AUTH, SMTPUTF8), local
  `sendmail`, Amazon SES, and a null sink.
- **Resilient routing** — round-robin, failover, and named-transport routing,
  composable as decorators.
- **Middleware pipeline** — `BeforeSend` (mutate / reject) and `AfterSend`
  (observe) hooks wrap any transport.
- **DSN configuration** — build a transport from a `smtp://…` string, including
  `failover(…)` / `roundrobin(…)` composites.
- **Observability** — opt-in OpenTelemetry traces & metrics via a separate
  module, so non-users pay nothing.
- **Secure by default** — verified TLS, cleartext-auth refused, sane timeouts.
- **Testable** — an in-memory recording transport with assertion helpers.

## Install

```sh
go get github.com/shyim/go-mailer                   # core: MIME, null, composites, DSN, middleware
go get github.com/shyim/go-mailer/transport/smtp    # SMTP / ESMTP transport
go get github.com/shyim/go-mailer/transport/sendmail
go get github.com/shyim/go-mailer/transport/ses     # Amazon SES (isolates the AWS SDK)
go get github.com/shyim/go-mailer/middleware/otelmw # OpenTelemetry traces + metrics
```

Requires **Go 1.26+**.

## Quickstart

```go
package main

import (
	"context"

	"github.com/shyim/go-mailer"
	"github.com/shyim/go-mailer/transport/smtp"
)

func main() {
	msg := gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("alice@example.com", "Alice")).
		SetTo(gomailer.MustAddress("bob@example.com", "Bob")).
		SetSubject("Hello").
		SetText([]byte("Hello, Bob!"))

	tr := smtp.NewTransport("smtp.example.com", 587, false).
		SetUsername("alice@example.com").
		SetPassword("secret")

	mailer := gomailer.NewMailer(tr)
	defer mailer.Close()

	if err := mailer.Send(context.Background(), msg, nil); err != nil {
		panic(err)
	}
}
```

<details>
<summary><b>A richer example</b> — HTML body, attachment, and DSN-based transport</summary>

```go
import (
	"github.com/shyim/go-mailer"
	"github.com/shyim/go-mailer/transport"
	_ "github.com/shyim/go-mailer/transport/smtp" // registers smtp:// and smtps://
)

func sendReport(ctx context.Context, pdfBytes []byte) error {
	msg := gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("alice@example.com", "Alice")).
		SetTo(gomailer.MustAddress("bob@example.com", "Bob")).
		SetSubject("Your report").
		SetText([]byte("See the attached report.")).
		SetHTML([]byte("<p>See the attached <b>report</b>.</p>")).
		Attach(gomailer.Attachment{
			Filename:    "report.pdf",
			ContentType: "application/pdf",
			Data:        pdfBytes,
		})

	// Build the transport from a DSN; the blank import above registers the scheme.
	tr, err := transport.FromDSN("smtp://alice:secret@smtp.example.com:587", transport.Deps{})
	if err != nil {
		return err
	}
	return gomailer.NewMailer(tr).Send(ctx, msg, nil)
}
```

</details>

## Documentation

Full documentation lives in [`docs/`](docs/) and is published as a
[Zensical](https://zensical.org) site.

| Section | Contents |
|---------|----------|
| **[Getting started](docs/getting-started/index.md)** | installation, quickstart, the module layout |
| **[Concepts](docs/concepts/index.md)** | architecture, transports, middleware & hooks, errors |
| **[Guides](docs/guides/index.md)** | sending mail, DSN, observability (OTLP), testing, production |
| **[Reference](docs/reference/index.md)** | DSN options, behavior reference |

Build the docs locally with `uvx zensical build` (or
`pip install zensical && zensical build`).

## Project layout

This is a multi-module workspace, so a consumer pulls in only what they use:

| Module | Brings |
|--------|--------|
| `github.com/shyim/go-mailer` | core: MIME, null transport, composites, DSN, middleware |
| `…/transport/smtp` | the SMTP / ESMTP transport |
| `…/transport/sendmail` | the `sendmail` transport |
| `…/transport/ses` | the Amazon SES transport (isolates the AWS SDK) |
| `…/middleware/otelmw` | OpenTelemetry traces & metrics |

## Contributing

Issues and pull requests are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) for
the project layout and local workflow. Each module is independently testable:

```sh
go test ./...                       # root module
(cd transport/smtp && go test ./...)
```

The project is `gofmt`- and [`golangci-lint`](https://golangci-lint.run)-clean;
please keep it that way.

## Status

Pre-1.0 — the API may still change.

## License

[MIT](LICENSE)
