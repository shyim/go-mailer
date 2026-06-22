# Testing

gomailer ships a dedicated test package, `mailertest`, so you can verify mail
code without touching the network. The centrepiece is
[`RecordingTransport`](../concepts/transports.md): a real `gomailer.Transport`
that captures every message instead of delivering it, paired with assertion
helpers built on the standard `testing` package.

## The recording transport

Drop a `RecordingTransport` in wherever your code expects a
`gomailer.Transport` — typically behind `gomailer.NewMailer`:

```go
import (
    "context"
    "testing"

    "github.com/shyim/go-mailer"
    "github.com/shyim/go-mailer/mailertest"
)

func TestSendsWelcome(t *testing.T) {
    rec := mailertest.NewRecordingTransport("") // "" => identity "test://"
    mailer := gomailer.NewMailer(rec)

    msg := gomailer.NewMessage().
        SetFrom(gomailer.MustAddress("alice@example.com", "Alice")).
        SetTo(gomailer.MustAddress("bob@example.com", "Bob")).
        SetSubject("Welcome").
        SetText([]byte("hi"))

    if err := mailer.Send(context.Background(), msg, nil); err != nil {
        t.Fatal(err)
    }

    mailertest.AssertEmailCount(t, rec, 1)
    mailertest.AssertEmailContains(t, rec, "Subject: Welcome")
}
```

`NewRecordingTransport(name)` records nothing to the wire; it derives an
envelope from each message (or clones one you supply) and stores the resulting
`*gomailer.SentMessage`. It is safe for concurrent use.

!!! note
    `RecordingTransport` does **not** embed `BaseTransport`, so it applies no
    throttling and emits no middleware events. To test middleware behaviour,
    `middleware.Wrap` the recording transport — see
    [Middleware](../concepts/middleware.md).

## Inspecting recorded messages

Every send is captured as a `*gomailer.SentMessage`. Read them back with:

| Method | Returns |
|--------|---------|
| `rec.Messages()` | snapshot `[]*gomailer.SentMessage`, in send order |
| `rec.Last()` | `(*gomailer.SentMessage, bool)` — most recent, `false` if none |
| `rec.Count()` | number of messages recorded so far |
| `rec.Reset()` | discard all recorded messages |

`SentMessage` exposes the serialized wire bytes and envelope, so you can assert
on the exact MIME that would have been transmitted:

```go
func TestRendersHTMLBody(t *testing.T) {
    rec := mailertest.NewRecordingTransport("")
    mailer := gomailer.NewMailer(rec)

    msg := gomailer.NewMessage().
        SetFrom(gomailer.MustAddress("alice@example.com", "")).
        SetTo(gomailer.MustAddress("bob@example.com", "")).
        SetSubject("Receipt").
        SetHTML([]byte("<p>Thanks!</p>"))

    if err := mailer.Send(context.Background(), msg, nil); err != nil {
        t.Fatal(err)
    }

    sm, ok := rec.Last()
    if !ok {
        t.Fatal("no message recorded")
    }
    if !bytes.Contains(sm.Bytes(), []byte("text/html")) {
        t.Fatalf("expected an HTML part, got:\n%s", sm.Bytes())
    }

    env := sm.Envelope()
    if got := env.Recipients()[0].Email(); got != "bob@example.com" {
        t.Fatalf("unexpected recipient: %s", got)
    }
}
```

## Assertion helpers

The helpers in `mailertest` cover the common things you want to assert about
sent mail. Each takes a `TestingT` first, then the transport:

```go
mailertest.AssertEmailCount(t, rec, 2)            // exactly N sent
mailertest.AssertSent(t, rec)                     // at least one sent
mailertest.AssertNotSent(t, rec)                  // none sent
mailertest.AssertEmailContains(t, rec, "X-Audited: true") // any wire bytes contain s
```

### Why `AssertQueuedEmailCount` only accepts 0

gomailer sends **synchronously** — there is no async queue. A message is either
sent or it is not; nothing is ever "queued". `AssertQueuedEmailCount` therefore
passes only for a count of `0` and fails for any positive count:

```go
mailertest.AssertQueuedEmailCount(t, rec, 0) // OK
mailertest.AssertQueuedEmailCount(t, rec, 1) // always fails
```

!!! tip
    Need to assert that a positive number of messages were dispatched? Use
    `AssertEmailCount`, which checks the (synchronous) sent count.

### The `TestingT` interface

The assertion helpers accept an interface, not `*testing.T` directly, so
`mailertest` carries no hard dependency on the `testing` package and you can
supply a fake to test the helpers themselves:

```go
type TestingT interface {
    Helper()
    Errorf(format string, args ...any)
}
```

`*testing.T` satisfies it, so you normally just pass `t`.

## Simulating transport failures

`FailNext(err)` forces the **next** `Send` to return `err` without recording the
message; a `nil` argument clears a pending failure. This is the simplest way to
test your error handling, and to exercise failover / round-robin routing:

```go
import (
    "errors"

    "github.com/shyim/go-mailer"
)

func TestSurfacesSendError(t *testing.T) {
    rec := mailertest.NewRecordingTransport("")
    mailer := gomailer.NewMailer(rec)

    boom := &gomailer.TransportError{Msg: "relay down", Code: 421}
    rec.FailNext(boom)

    err := mailer.Send(context.Background(), buildMessage(), nil)
    if err == nil {
        t.Fatal("expected the send to fail")
    }
    if !errors.Is(err, gomailer.ErrTransport) {
        t.Fatalf("want ErrTransport, got %v", err)
    }

    var te *gomailer.TransportError
    if errors.As(err, &te) && te.Code != 421 {
        t.Fatalf("want code 421, got %d", te.Code)
    }

    mailertest.AssertNotSent(t, rec) // a failed send records nothing
}
```

Because `FailNext` only affects a single send, you can drive composite routing
deterministically — fail the primary leaf and assert the backup recorded the
message:

```go
func TestFailoverFallsBack(t *testing.T) {
    primary := mailertest.NewRecordingTransport("primary")
    backup := mailertest.NewRecordingTransport("backup")

    fo, err := transport.NewFailoverTransport(
        []gomailer.Transport{primary, backup}, 0)
    if err != nil {
        t.Fatal(err)
    }

    primary.FailNext(errors.New("primary unavailable"))

    mailer := gomailer.NewMailer(fo)
    if err := mailer.Send(context.Background(), buildMessage(), nil); err != nil {
        t.Fatal(err)
    }

    mailertest.AssertNotSent(t, primary)
    mailertest.AssertEmailCount(t, backup, 1)
}
```

See [Transports](../concepts/transports.md) for how `Failover` and
`RoundRobin` rotate.

## Testing real transports against a fake SMTP server

When you want to exercise the actual ESMTP conversation — STARTTLS negotiation,
`MAIL`/`RCPT`/`DATA`, AUTH, dot-stuffing — point the real
[`smtp.Transport`](../concepts/transports.md) at an in-process TCP listener that
speaks just enough SMTP for the test. The `transport/smtp` module's own tests
follow exactly this pattern: a `net.Listen("tcp", "127.0.0.1:0")` server on an
ephemeral port that scripts canned replies per command and records the bytes it
received.

```go
import (
    "bufio"
    "context"
    "net"
    "strconv"
    "strings"
    "testing"

    smtp "github.com/shyim/go-mailer/transport/smtp"
)

func TestSMTPDelivery(t *testing.T) {
    ln, err := net.Listen("tcp", "127.0.0.1:0")
    if err != nil {
        t.Fatal(err)
    }
    defer ln.Close()

    // Minimal scripted SMTP server: greet, accept every command, swallow DATA.
    go func() {
        conn, err := ln.Accept()
        if err != nil {
            return
        }
        defer conn.Close()
        r := bufio.NewReader(conn)
        conn.Write([]byte("220 fake ESMTP\r\n"))
        inData := false
        for {
            line, err := r.ReadString('\n')
            if err != nil {
                return
            }
            switch {
            case inData:
                if strings.TrimRight(line, "\r\n") == "." {
                    inData = false
                    conn.Write([]byte("250 OK\r\n"))
                }
            case strings.HasPrefix(line, "EHLO"):
                conn.Write([]byte("250-fake\r\n250 AUTH PLAIN\r\n"))
            case strings.HasPrefix(line, "DATA"):
                conn.Write([]byte("354 send data\r\n"))
                inData = true
            case strings.HasPrefix(line, "QUIT"):
                conn.Write([]byte("221 bye\r\n"))
                return
            default: // MAIL, RCPT, ...
                conn.Write([]byte("250 OK\r\n"))
            }
        }
    }()

    host, portStr, _ := net.SplitHostPort(ln.Addr().String())
    port, _ := strconv.Atoi(portStr)

    tr := smtp.NewTransport(host, port, false).
        SetAllowPlaintextAuth(true) // only for a trusted, in-test relay
    defer tr.Close()

    sm, err := tr.Send(context.Background(), buildMessage(), nil)
    if err != nil {
        t.Fatalf("send failed: %v", err)
    }
    if sm.MessageID() == "" {
        t.Fatal("expected a Message-ID after a successful send")
    }
}
```

!!! warning
    A scripted listener like this speaks **cleartext** SMTP, so AUTH requires
    `SetAllowPlaintextAuth(true)`. gomailer refuses cleartext credential auth by
    default — never set this flag against a production relay. See the
    [production notes](../index.md) for the safe-by-default behaviours.

For the fully worked versions — including STARTTLS, IDNA-encoded envelopes,
rejected recipients, hung-server timeouts and dot-stuffing — read
`transport/smtp/smtp_test.go` in the repository; the `newFakeSMTPServer` helper
there is a more complete take on the pattern above.

## See also

- [Transports](../concepts/transports.md) — the transports you can record or fake.
- [Transports](../concepts/transports.md) — failover / round-robin behaviour.
- [Middleware](../concepts/middleware.md) — wrapping a recording transport to test hooks.
