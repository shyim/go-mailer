# Guides

Task-focused walkthroughs for building, sending, configuring, observing, and
testing email with gomailer. Each guide is self-contained and links back to the
underlying [Concepts](../concepts/transports.md) where useful.

- [Sending mail](sending-mail.md) — Compose RFC 5322 / MIME messages with the
  `gomailer.NewMessage()` builder (addresses, subjects, text and HTML bodies,
  custom headers, and attachments) and deliver them through a `gomailer.Mailer`.

- [DSN configuration](dsn.md) — Wire up delivery via DSNs with
  `transport.FromDSN` / `transport.FromDSNs`, blank-import the SMTP/sendmail
  modules to register their schemes, and nest `failover(...)` / `roundrobin(...)`
  composites.

- [Observability](observability.md) — Emit OpenTelemetry traces and metrics with
  the `middleware/otelmw` adapter, wrapping each leaf transport so every delivery
  attempt gets its own span and metric sample.

- [Testing](testing.md) — Capture messages in memory with
  `mailertest.NewRecordingTransport`, assert on what was sent, and simulate
  failures with `FailNext` to exercise failover and round-robin behavior.

- [Production](production.md) — Operate gomailer safely: cleartext-auth refusal,
  verified TLS, the default 30s IO timeout, and graceful `Close()` shutdown of
  pooled SMTP connections.

For the decorator pipeline behind `BeforeSend` / `AfterSend` and the
`middleware.ErrReject` skip-and-succeed pattern, see
[Middleware & hooks](../concepts/middleware.md).
