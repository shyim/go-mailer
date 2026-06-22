# Production

gomailer is safe by default: it refuses cleartext credential auth, verifies TLS,
and won't let a hung relay block a goroutine forever. This page collects the
operational behaviors and the knobs that override them.

## Cleartext AUTH is refused

The SMTP transport will **not** send `AUTH` credentials over a connection that is
not protected by TLS. An active attacker can strip the server's `STARTTLS`
advertisement to downgrade the session, so sending credentials there would leak
them. The send fails with a [`*TransportError`](../concepts/errors.md) instead.

Safe ways to authenticate:

=== "Implicit TLS"

    ```go
    // smtps:// — TLS on connect, port 465.
    t := smtp.NewTransport("smtp.example.com", 465, true).
        SetUsername("user").
        SetPassword("pass")
    ```

=== "STARTTLS"

    ```go
    // smtp:// — opportunistic STARTTLS upgrades the connection before AUTH.
    t := smtp.NewTransport("smtp.example.com", 587, false).
        SetUsername("user").
        SetPassword("pass")
    ```

Only opt out for a **trusted local relay** where the network path cannot be
observed or tampered with:

```go
t := smtp.NewTransport("127.0.0.1", 25, false).
    SetUsername("user").
    SetPassword("pass").
    SetAllowPlaintextAuth(true) // last resort; disables the safety check
```

!!! warning
    `SetAllowPlaintextAuth(true)` exposes your credentials to anyone on the
    network path. Never enable it for a relay you reach over the public internet.

## TLS is verified by default

The default TLS config sets a **TLS 1.2 minimum** and the `ServerName` to the
connection host, so the server certificate and hostname are fully verified — for
both `smtps://` and STARTTLS. Nothing disables verification implicitly.

Tune it explicitly via DSN options or setters:

| DSN option | Effect |
|---|---|
| `verify_peer=false` | disable certificate/hostname verification (not recommended) |
| `peer_fingerprint=<sha256-hex>` | pin the server certificate by SHA-256 fingerprint |

```go
t, err := transport.FromDSN(
    "smtps://user:pass@smtp.example.com?peer_fingerprint=ab12...ef",
    transport.Deps{},
)
```

For full control over the `tls.Config` (custom CA pool, client certs), use
`SetTLSConfig`:

```go
t := smtp.NewTransport("smtp.example.com", 587, false).
    SetTLSConfig(&tls.Config{
        MinVersion: tls.VersionTLS12,
        RootCAs:    pool,
    })
```

!!! note
    If your custom `tls.Config` leaves `ServerName` empty, the transport injects
    the connection host at dial time so hostname verification still works.

## A hung server cannot block forever

When a send's context carries **no deadline**, a default **30s per-operation
socket deadline** applies, so a stalled relay returns an error instead of
hanging the goroutine indefinitely. There are two ways to change this:

=== "Context deadline (takes precedence)"

    ```go
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    err := mailer.Send(ctx, msg, nil)
    ```

=== "Transport-level timeout"

    ```go
    t := smtp.NewTransport("smtp.example.com", 587, false).
        SetTimeout(15 * time.Second)
    ```

A context deadline always wins over `SetTimeout`. Use the context for
per-request budgets and `SetTimeout` for a transport-wide default.

## Graceful shutdown

`Mailer`, and the `RoundRobin` / `Failover` / `Transports` composites, all
implement `io.Closer`. `Close()` fans out to every underlying transport so
pooled SMTP connections are sent `QUIT` and released cleanly. Call it on
shutdown:

```go
mailer := gomailer.NewMailer(t)
defer mailer.Close()
```

!!! tip
    Closing the `Mailer` is enough — it propagates `Close()` down through any
    composites and middleware to the leaf transports.

## Branching on failover error codes

A composite's aggregate error surfaces the most recent underlying SMTP response
code, so you can branch on transient (4xx) vs permanent (5xx) failures with
`errors.As`:

```go
err := mailer.Send(ctx, msg, nil)

var te *gomailer.TransportError
if errors.As(err, &te) {
    switch {
    case te.Code >= 400 && te.Code < 500:
        // transient — safe to retry later
    case te.Code >= 500:
        // permanent — do not retry as-is
    }
}
```

`errors.Is(err, gomailer.ErrTransport)` also holds for these errors. See
[Errors](../concepts/errors.md) for the full sentinel hierarchy.

## Composite concurrency tradeoff

`RoundRobin` and `Failover` **serialize sends** (single-flight) so rotation and
dead-transport tracking stay consistent. They do **not** parallelize delivery
across servers. For concurrent throughput, fan out across independent transports
in your own goroutines rather than relying on a single composite to spread load.

Surface dead-marking events for diagnostics with `SetLogger`:

```go
fo, _ := transport.NewFailoverTransport(leaves, 15*time.Second)
fo.SetLogger(slog.Default()) // logs when a transport is marked dead / retried
```

See [Transports](../concepts/transports.md) for the routing semantics.

## Deterministic Message-ID and Date

A `Message`'s `Message-ID` and `Date` headers are materialized **once**, the
first time the message is serialized, and then reused. Repeated `Bytes()` calls
on the same message therefore produce identical output — handy for golden-file
tests and idempotent logging. To get the ID that was actually sent, read it from
the result rather than re-serializing:

```go
sm, err := t.Send(ctx, msg, nil)
if err == nil {
    log.Printf("sent %s", sm.MessageID())
}
```

`Bcc` recipients are kept in the envelope but never written into the headers.

## Pre-1.0 caveats

!!! warning "Before 1.0"
    - **Synchronous only.** `Mailer.Send` blocks until delivery completes or
      fails; there is no built-in queue or async path. Wrap it in your own
      worker pool or job system if you need background delivery. The
      `SendOption` parameter leaves a seam for a future queue.
    - **Pin and update deliberately.** During development the submodules
      (`transport/smtp`, `transport/sendmail`, `middleware/otelmw`) depend on the
      root via a local `replace`; once tagged, `go get` resolves them to the
      published version. Pin the same tag across the root and any submodules you
      use and update them together:

      ```sh
      go get github.com/shyim/go-mailer@v0.x.y
      go get github.com/shyim/go-mailer/transport/smtp@v0.x.y
      ```
