# DSN options

A DSN configures a transport from a single string. The grammar is:

```
scheme://[user[:password]@]host[:port][?option=value&...]
```

Resolve one with [`transport.FromDSN`](../concepts/transports.md), or a name to DSN map
with `transport.FromDSNs`. Concrete transport packages register their schemes
via `init()` (the `database/sql` driver pattern), so they must be
**blank-imported** to be resolvable:

```go
import (
    "github.com/shyim/go-mailer/transport"
    _ "github.com/shyim/go-mailer/transport/sendmail" // register sendmail://
    _ "github.com/shyim/go-mailer/transport/smtp"     // register smtp:// / smtps://
)

t, err := transport.FromDSN("smtp://user:pass@smtp.example.com:587?require_tls=true", transport.Deps{})
```

The `null://` scheme is always available from the root module.

!!! note "Boolean parsing"
    Boolean options accept a small set of truthy spellings: `1`, `true`, `on`
    and `yes` (case-insensitive) are true; every other value is false.

## Supported schemes

| Scheme | Transport | Module to import |
|--------|-----------|------------------|
| `null://` | discards everything | root (always available) |
| `smtp://` | ESMTP, opportunistic STARTTLS | `…/transport/smtp` |
| `smtps://` | ESMTP, implicit TLS on connect | `…/transport/smtp` |
| `sendmail://` | local sendmail binary (`-t` mode) | `…/transport/sendmail` |

!!! warning "`sendmail+smtp://` is unsupported"
    The interactive `-bs` mode, which speaks SMTP over the sendmail process's
    stdin/stdout instead of piping raw MIME, is intentionally not implemented
    and is rejected at resolution time.

## SMTP DSN options

User and password come from the authority (`user:password@host`). The query
options below are parsed by the SMTP factory.

| Option | Type | Default | Meaning |
|--------|------|---------|---------|
| `auto_tls` | bool | `true` | Opportunistic TLS. For `smtp://` with no explicit port, a remote host (or port `465`) selects implicit TLS; `localhost`/`127.0.0.1`/`::1` stays on plain port 25. An empty value leaves it enabled. |
| `require_tls` | bool | `false` | Require an encrypted connection; abort if TLS cannot be established. |
| `verify_peer` | bool | `true` | Certificate chain and hostname verification. An explicit falsey value sets `InsecureSkipVerify` (disables both). Nothing else disables verification implicitly. |
| `peer_fingerprint` | string (SHA-256 hex) | _unset_ | Pin the leaf certificate by its SHA-256 fingerprint. Colons and spaces are stripped; must be 64 hex characters or resolution fails with `ErrInvalidArgument`. |
| `source_ip` | string (IP) | _unset_ | Bind outbound connections to a specific local source address. |
| `local_domain` | string | _unset_ | Hostname sent in `EHLO`/`HELO`. |
| `max_per_second` | float | _unset_ | Throttle delivery starts to this rate (messages per second). Invalid values fail with `ErrInvalidArgument`. |
| `restart_threshold` | int | _unset_ | Reconnect after this many messages on one connection. |
| `restart_threshold_sleep` | int (seconds) | `0` | Sleep this many seconds during a restart. Only meaningful alongside `restart_threshold`. |
| `ping_threshold` | int (seconds) | _unset_ | Send a `NOOP` keep-alive if the connection has been idle longer than this. |

!!! note "`timeout` is not a DSN option"
    The per-operation socket timeout is **not** parsed from the DSN. Configure
    it with `smtp.Transport.SetTimeout(d)`, or pass a context with a deadline
    (which takes precedence). When neither is set, a 30s default applies. See
    [Transports](../concepts/transports.md).

### Examples

```sh
# Submission with STARTTLS required
smtp://user:pass@smtp.example.com:587?require_tls=true

# Implicit TLS on connect
smtps://user:pass@smtp.example.com:465

# Local relay, no encryption, explicit HELO name
smtp://localhost:25?local_domain=app.internal

# Pin the certificate fingerprint and throttle
smtp://user:pass@smtp.example.com:587?peer_fingerprint=AA:BB:CC...&max_per_second=10

# Recycle the connection every 100 messages, pausing 1s
smtp://user:pass@smtp.example.com:587?restart_threshold=100&restart_threshold_sleep=1
```

## Sendmail DSN options

| Option | Type | Default | Meaning |
|--------|------|---------|---------|
| `command` | string | `/usr/sbin/sendmail -t -i` | The binary and arguments to pipe raw MIME into. Must be a `-t` mode command; `-bs` is rejected. |

### Examples

```sh
# Default command
sendmail://default

# Custom binary path and flags
sendmail://default?command=/usr/local/bin/sendmail%20-t%20-i
```

!!! tip "URL-encode the command"
    Spaces and other reserved characters in `command` must be percent-encoded
    (`%20` for space) so the query parses correctly.

## Composite DSN syntax

`failover(...)` and `roundrobin(...)` wrap two or more child DSNs, separated by
spaces. They nest recursively. See [Routing](../concepts/transports.md).

```
failover(smtp://a.example.com smtp://b.example.com)
roundrobin(smtp://a.example.com smtp://b.example.com)
```

An optional `?retry_period=<seconds>` suffix sets how long a failed child stays
marked dead before it is retried (integer seconds, default `0`):

```
roundrobin(smtp://a.example.com smtp://b.example.com)?retry_period=15
```

=== "Failover"

    ```go
    t, err := transport.FromDSN(
        "failover(smtp://user:pass@primary.example.com smtp://user:pass@backup.example.com)?retry_period=60",
        transport.Deps{},
    )
    ```

    Sticky: stays on the current transport, advancing only on failure.

=== "Round-robin"

    ```go
    t, err := transport.FromDSN(
        "roundrobin(smtp://a.example.com smtp://b.example.com)?retry_period=15",
        transport.Deps{},
    )
    ```

    Rotates per send, skipping dead transports until `retry_period` elapses.

!!! note "`retry_period` is seconds; an invalid value fails resolution"
    The value must be a non-negative integer; anything else returns an error
    wrapping `gomailer.ErrInvalidArgument`.

## Named transports

`transport.FromDSNs` builds a `Transports` router from a name to DSN map. The
router dispatches by the `X-Transport` message header and falls back to the
first transport.

```go
router, err := transport.FromDSNs(map[string]string{
    "main":   "smtp://user:pass@a.example.com",
    "backup": "sendmail://default",
}, transport.Deps{})
```

See [Transports](../concepts/transports.md) for routing and graceful shutdown
details.
