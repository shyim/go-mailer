# Errors

gomailer classifies failures with a small set of **sentinel errors** plus a
structured `*TransportError`. You match a failure category with `errors.Is` and
reach transport details (like the SMTP response code) with `errors.As` — the
standard Go error idioms, no custom error machinery to learn.

## Sentinel errors

The sentinels live in the root package (`github.com/shyim/go-mailer`) and are
matched with `errors.Is`.

| Sentinel | Meaning |
|---|---|
| `ErrTransport` | A delivery attempt failed (connection, AUTH, SMTP reply, …). |
| `ErrInvalidArgument` | A bad value was passed (e.g. malformed address). |
| `ErrLogic` | A programming error — calling in an invalid state. |
| `ErrRuntime` | A runtime failure outside the transport layer. |
| `ErrIncompleteDSN` | A DSN is missing required parts (host, etc.). |
| `ErrUnsupportedScheme` | A DSN scheme has no registered transport — usually a missing blank import. |

```go
err := mailer.Send(ctx, msg, nil)
switch {
case err == nil:
    // delivered
case errors.Is(err, gomailer.ErrTransport):
    log.Printf("delivery failed: %v", err)
case errors.Is(err, gomailer.ErrInvalidArgument):
    log.Printf("bad input: %v", err)
default:
    log.Printf("send error: %v", err)
}
```

!!! tip "Unsupported scheme usually means a missing import"
    `transport.FromDSN("smtp://…")` returns `ErrUnsupportedScheme` when the
    `smtp` package was never imported. Concrete transports self-register via
    `init()`, so they must be **blank-imported** to be DSN-resolvable — see
    [Transports](transports.md).

## `*TransportError`

Transport failures carry a `*TransportError` value, which satisfies
`errors.Is(err, ErrTransport)` and exposes the underlying detail:

```go
type TransportError struct {
    Msg   string // human-readable message
    Code  int    // SMTP response code, 0 if not applicable
    Cause error  // wrapped underlying error, if any
    // ... plus an appendable debug transcript, reached via Debug()
}
```

- **`Code`** — the SMTP response code (e.g. `421`, `550`), or `0` when the
  failure happened before/outside an SMTP reply (DNS, dial, TLS handshake).
- **`Cause`** — the wrapped underlying error. `Unwrap()` returns it, so
  `errors.Is`/`errors.As` traverse into it.
- **`Debug()`** — the accumulated protocol transcript, invaluable for diagnosing
  SMTP conversations. The SMTP transport and the composites append to it.

## Reaching the SMTP code with `errors.As`

Use `errors.As` to pull out the `*TransportError` and branch on its `Code`. The
SMTP convention is **4xx = transient (retry)** and **5xx = permanent (give up)**.

```go
import (
    "errors"

    "github.com/shyim/go-mailer"
)

err := mailer.Send(ctx, msg, nil)

var te *gomailer.TransportError
if errors.As(err, &te) {
    switch {
    case te.Code >= 400 && te.Code < 500:
        // 4xx — temporary failure (greylisting, rate limit, server busy).
        // Safe to retry later.
        log.Printf("transient failure (%d): %s", te.Code, te.Msg)
        scheduleRetry(msg)
    case te.Code >= 500:
        // 5xx — permanent failure (mailbox unknown, message rejected).
        // Do not retry; surface to the user.
        log.Printf("permanent failure (%d): %s", te.Code, te.Msg)
    default:
        // Code == 0 — pre-SMTP failure (dial/TLS/DNS). Inspect te.Cause.
        log.Printf("connection failure: %v", te.Cause)
    }
    log.Printf("transcript:\n%s", te.Debug())
}
```

!!! note "Code 0 is not 4xx/5xx"
    A `Code` of `0` means the failure never reached an SMTP reply — for example a
    dial timeout or TLS verification error. Inspect `te.Cause` (and treat it as
    your retry policy dictates) rather than the code in that case.

## Composites surface the underlying code

The [composite transports](transports.md) — `RoundRobin`, `Failover`, and the
`Transports` router — aggregate failures from their leaves. Their aggregate
error **surfaces the most recent underlying SMTP code**, so `errors.As` works the
same way against a composite as against a single SMTP transport:

```go
fo, _ := transport.NewFailoverTransport(leaves, 0)
mailer := gomailer.NewMailer(fo)

if err := mailer.Send(ctx, msg, nil); err != nil {
    var te *gomailer.TransportError
    if errors.As(err, &te) && te.Code >= 500 {
        // Every backend permanently rejected the message — don't retry.
        return fmt.Errorf("undeliverable: %w", err)
    }
    // 4xx (or pre-SMTP) — retry later against the failover chain.
}
```

This lets you keep one retry-vs-permanent decision at the top of your send path,
regardless of whether you wired a single transport or a failover chain beneath
it.

## Errors are passed through unchanged

Observability and lifecycle middleware never rewrap your errors. The
`middleware.AfterSend` observer and the `otelmw` adapter both record the failure
but return the **original** error, so `errors.Is(err, gomailer.ErrTransport)` and
`errors.As(err, &te)` keep working through outer layers. See
[Middleware](middleware.md) for details.

!!! warning "One exception: rejected sends are not errors"
    A `middleware.BeforeSend` hook that returns `middleware.ErrReject` **skips**
    the send and reports **success** — `Mailer.Send` returns `nil`. That is by
    design: a deliberate rejection is not a delivery failure. It is not a
    `*TransportError`, so it will not appear in your error-handling branches.
