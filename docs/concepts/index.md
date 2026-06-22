# Concepts

gomailer is built from small Go interfaces and an embedded `BaseTransport` that provides a shared send pipeline. These pages explain how the pieces fit together.

- [Architecture](architecture.md) — how `Message`, `Transport`, `Mailer`, and `Envelope` compose into a single synchronous send pipeline.
- [Transports](transports.md) — the leaf transports (SMTP, sendmail, null) and the composites (round-robin, failover, named router) that deliver or route a message.
- [Middleware & hooks](middleware.md) — decorating a `Transport` to mutate, reject, or observe sends via `BeforeSend`/`AfterSend`, plus the observability layer.
- [Errors](errors.md) — the sentinel error hierarchy and `*TransportError`, classified with `errors.Is`/`errors.As`.
