# Reference

Lookup-oriented material: exhaustive option tables for configuring transports.

Reach for this section when you already know the concept and just need the exact
name, default, or behavior.

## Pages

- [DSN options](dsn-options.md) — every DSN scheme, every query option, defaults,
  and the composite wrapper syntax (`failover(...)` / `roundrobin(...)`).

## Quick orientation

A DSN is parsed into a `Transport` by the `transport` package. Concrete transport
packages self-register their schemes via `init()`, so blank-import the ones you
use:

```go
import (
    "github.com/shyim/go-mailer/transport"
    _ "github.com/shyim/go-mailer/transport/smtp"      // smtp:// smtps://
    _ "github.com/shyim/go-mailer/transport/sendmail"  // sendmail://
)

t, err := transport.FromDSN("smtp://user:pass@smtp.example.com:587?require_tls=true", transport.Deps{})
```

The DSN grammar is:

```
scheme://[user[:password]@]host[:port][?option=value&...]
```

!!! note
    The `null://` scheme is always available without any blank import. Every
    other scheme requires its transport module to be imported for side effects.

See also the conceptual material in [Transports](../concepts/index.md) and the
[Getting started](../getting-started/index.md) guide for end-to-end setup.
